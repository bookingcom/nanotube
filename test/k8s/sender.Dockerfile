FROM golang:1.23.4-alpine3.21 as builder

RUN apk add git
WORKDIR /nt
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./test/sender

FROM alpine:3.21
WORKDIR /nt

RUN set -x \
    && apk update \
    && apk upgrade \
    && apk add --no-cache ca-certificates \
    && rm -rf /var/cache/* /var/log/* /tmp/*

COPY --from=builder /nt/sender /nt
COPY --from=builder /nt/k8s/in /nt/in

RUN     adduser graphite --disabled-password
USER    graphite

ENTRYPOINT ["./sender", "-data", "in", "-host", "localhost", "-port", "2003", "-rate", "100", "-cycle", "-retryTCP", "-promPort", "9090"]
