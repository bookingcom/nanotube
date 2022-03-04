FROM golang:1.17.6-alpine as builder

WORKDIR /nt
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./test/sender

FROM alpine:3.13
WORKDIR /nt

COPY --from=builder /nt/sender /nt
COPY --from=builder /nt/k8s/in /nt/in

ENTRYPOINT ["./sender", "-data", "in", "-host", "localhost", "-port", "2003", "-rate", "10", "-cycle", "-retryTCP"]
