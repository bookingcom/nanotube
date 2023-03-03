FROM golang:1.20.0-alpine as builder

RUN apk add git
WORKDIR /nt
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./test/sender

FROM alpine:3.13
WORKDIR /nt

COPY --from=builder /nt/sender /nt
COPY --from=builder /nt/k8s/in /nt/in

ENTRYPOINT ["./sender", "-data", "in", "-host", "localhost", "-port", "2003", "-rate", "100", "-cycle", "-retryTCP", "-promPort", "9090"]
