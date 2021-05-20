FROM golang:1.16.3-alpine as builder

WORKDIR /nt
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./test/sender

FROM alpine:3.13
WORKDIR /nt

COPY --from=builder /nt/sender /nt

ENTRYPOINT ["./sender", "-config", "config/config.toml"]
