FROM golang:1.16.3-alpine as builder

WORKDIR /nt
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./cmd/nanotube

FROM scratch
WORKDIR /nt

COPY --from=builder /nt/nanotube /nt
COPY ./config /nt/config

ENTRYPOINT ["./nanotube", "-config", "config/config.toml"]
