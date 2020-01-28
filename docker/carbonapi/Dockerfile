FROM golang:1.11

RUN go get github.com/bookingcom/carbonapi/cmd/carbonapi
RUN go install github.com/bookingcom/carbonapi/cmd/carbonapi

COPY ./docker/carbonapi/carbonapi.yaml /etc/

EXPOSE 7081
EXPOSE 8081

ENTRYPOINT [ "/go/bin/carbonapi",  "-config", "/etc/carbonapi.yaml" ]