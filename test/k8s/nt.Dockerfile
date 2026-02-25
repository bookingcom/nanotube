FROM golang:1.25.7-alpine3.23 AS builder

WORKDIR /nt
COPY . .
# TODO: Add version embedding.
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./cmd/nanotube

FROM alpine:3.23.3

RUN set -x \
    && apk update \
    && apk upgrade \
    && apk add --no-cache ca-certificates \
    && rm -rf /var/cache/* /var/log/* /tmp/*

WORKDIR /nt

COPY --from=builder /nt/nanotube /nt
COPY ./test/k8s/config /nt/config

RUN     adduser graphite --disabled-password
USER    graphite

# main listening port
EXPOSE 2003
# Prometheus port
EXPOSE 9090

ENTRYPOINT ["./nanotube", "-config", "config/config.toml"]
