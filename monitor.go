package main

import (
	"io/ioutil"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// The monitoring loop
func monitorWebsite(
	loadTime *prometheus.SummaryVec, responseStatus *prometheus.GaugeVec, errorCounter *prometheus.CounterVec, wrongOutboundIPCounter *prometheus.CounterVec,
	url string, egressIPs []string, interval int,
) {
	go func() {
		for {
			now := time.Now()
			// Sends an HTTP GET to the website
			resp, err := http.Get(url)
			if err != nil {
				log.Error(err)
				errorCounter.With(prometheus.Labels{"error": err.Error()}).Inc()
				time.Sleep(time.Duration(interval) * time.Millisecond)
				continue
			}
			elapsed := time.Since(now).Seconds()
			status := resp.StatusCode
			defer resp.Body.Close()
			bodyBytes, _ := ioutil.ReadAll(resp.Body)
			outboundIP := string(bodyBytes)

			// Updates Prometheus with the elapsed time
			loadTime.With(prometheus.Labels{"outbound_ip": outboundIP}).Observe(elapsed)
			responseStatus.With(prometheus.Labels{"outbound_ip": outboundIP}).Set(float64(status))

			// Check if outboundIP is in egressIPs
			var validIP bool
			for _, ip := range egressIPs {
				if outboundIP == ip {
					validIP = true
				}
			}
			if !validIP {
				wrongOutboundIPCounter.With(prometheus.Labels{"outbound_ip": outboundIP}).Inc()
			}

			log.WithFields(log.Fields{
				"outbound_ip":       outboundIP,
				"status":            status,
				"load_time":         elapsed,
				"wrong_outbound_ip": !validIP,
			}).Info("Got IP adress")
			time.Sleep(time.Duration(interval) * time.Millisecond)
		}
	}()
}

func GetOutboundIP() net.IP {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()
	localAddr := conn.LocalAddr().(*net.UDPAddr)
	return localAddr.IP
}

func GetVarOrDefault(varName string, defaultValue string) string {
	result := os.Getenv(varName)
	if result == "" {
		result = defaultValue
		log.Infof("Environment Variable [%s] not set - setting supplied default [%s]\n", varName, result)
	}
	return result
}

func GetSliceVarOrDefault(varName string, defaultValues []string) []string {
	envVar := GetVarOrDefault(varName, strings.Join(defaultValues, ","))
	return strings.Split(envVar, ",")
}

func main() {
	from := ""
	scrapePort := GetVarOrDefault("SCRAPE_PORT", "9100")
	interval := GetVarOrDefault("MONITOR_INTERVAL", "1000")
	url := GetVarOrDefault("MONITOR_URL", "")
	subsystem := GetVarOrDefault("SUBSYSTEM", "website")
	componentName := GetVarOrDefault("COMPONENT_NAME", "simple_http_monitor_docker_hub")
	egressIPs := GetSliceVarOrDefault("EGRESS_IPS", []string{})

	// 1 Sec timeout for the EC2 info site (if it's not there, the default timeout is 30 sec...)
	client := http.Client{
		Timeout: 1 * time.Second,
	}

	// Get the Availability Zone from the EC2 info site
	response, err := client.Get("http://169.254.169.254/latest/meta-data/placement/availability-zone")
	// If the info site does not answer (not an EC2 instance, i.e. running on your laptop) set `from=UNKNOWN`
	if err != nil {
		log.Warn("Could not find AZ. Trying to find the local IP")
		localAddress := GetOutboundIP()
		log.Infof("Found local IP address. Setting `from=%s`\n", localAddress)
		from = localAddress.String()
	} else {
		//if we got an answer from EC2 info site, and we know the AZ, set `from=AZ`
		defer response.Body.Close()
		bodyBytes, _ := ioutil.ReadAll(response.Body)
		from = string(bodyBytes)
	}

	// create and register a new `Summary` with Prometheus
	var responseTimeSummary = prometheus.NewSummaryVec(prometheus.SummaryOpts{
		Namespace:   "monitoring",
		Subsystem:   subsystem,
		Name:        componentName + "_load_time",
		Help:        componentName + " Load Time",
		ConstLabels: prometheus.Labels{"from": from},
		Objectives:  map[float64]float64{0.5: 0.05, 0.9: 0.01, 0.99: 0.001},
		MaxAge:      0,
		AgeBuckets:  0,
		BufCap:      0,
	},
		[]string{"outbound_ip"},
	)
	prometheus.Register(responseTimeSummary)
	// create and register a new `Gauge` with prometheus for the response statuse
	responseStatus := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace:   "monitoring",
		Subsystem:   subsystem,
		Name:        componentName + "_response_status",
		Help:        componentName + " response HTTP status",
		ConstLabels: prometheus.Labels{"from": from},
	},
		[]string{"outbound_ip"},
	)
	// create and register a new `Counter` with prometheus for the errors
	errorCounter := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace:   "monitoring",
		Subsystem:   subsystem,
		Name:        componentName + "_error",
		Help:        componentName + " error",
		ConstLabels: prometheus.Labels{"from": from},
	},
		[]string{"error"},
	)
	err = prometheus.Register(errorCounter)
	if err != nil {
		log.Fatal(err)
	}
	// create and register a new `Counter` with prometheus for wrong outbound IP
	wrongOutboundIPCounter := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace:   "monitoring",
		Subsystem:   subsystem,
		Name:        componentName + "_wrong_outbound_ip",
		Help:        componentName + " wrong outbound ip",
		ConstLabels: prometheus.Labels{"from": from},
	},
		[]string{"outbound_ip"},
	)
	err = prometheus.Register(wrongOutboundIPCounter)
	if err != nil {
		log.Fatal(err)
	}
	// Start the monitoring loop
	log.Infof("Starting to to monitor [%s], interval [%s]\n", url, interval)
	intervalStr, err := strconv.Atoi(interval)
	monitorWebsite(responseTimeSummary, responseStatus, errorCounter, wrongOutboundIPCounter, url, egressIPs, intervalStr)

	// Start the server, and set the /metrics endpoint to be served by the promhttp package
	log.Infof("Starting to serve metrics on port [%s]\n", scrapePort)
	http.Handle("/metrics", promhttp.Handler())
	log.Fatal(http.ListenAndServe(":"+scrapePort, nil))
}
