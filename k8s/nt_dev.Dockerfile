FROM alpine:3.13
WORKDIR /nt

# nanotube bas to be built in advance
# build speedup!
COPY ./nanotube /nt/nanotube
COPY ./k8s/config /nt/config

# main listening port
EXPOSE 2003
# Prometheus port
EXPOSE 9090

ENTRYPOINT ["./nanotube", "-config", "config/config.toml"]
