FROM golang as build
RUN mkdir /monitor
ADD . /monitor
WORKDIR /monitor
RUN CGO_ENABLED=0 GOOS=linux go build -o monitor .


FROM scratch
COPY --from=build /monitor/monitor /monitor
ENTRYPOINT ["/monitor"]
