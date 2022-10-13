FROM golang:1.18.0-alpine as builder

RUN apk add git
WORKDIR /nt
COPY . .
# TODO: Add version embedding.
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./cmd/nanotube

FROM alpine:3.13
WORKDIR /nt

COPY --from=builder /nt/nanotube /nt
COPY ./k8s/config /nt/config

# main listening port
EXPOSE 2003
# Prometheus port
EXPOSE 9090

ENTRYPOINT ["./nanotube", "-config", "config/config.toml"]
