FROM golang:1.12

RUN go get github.com/bookingcom/carbonapi/cmd/carbonzipper
RUN go install github.com/bookingcom/carbonapi/cmd/carbonzipper

COPY ./docker/zipper/carbonzipper.yaml /etc/

EXPOSE 7000
EXPOSE 8000

ENTRYPOINT [ "/go/bin/carbonzipper",  "-config", "/etc/carbonzipper.yaml" ]