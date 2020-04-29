FROM golang:1.14

WORKDIR /go/src/github.com/bookingcom/nanotube
COPY . .
RUN make
COPY config/ /etc/nanotube/config
COPY docker/nanotube/config/ /etc/nanotube/config

EXPOSE 2003

ENTRYPOINT [ "/go/src/github.com/bookingcom/nanotube/nanotube", "-config", "/etc/nanotube/config/config.toml","-clusters", "/etc/nanotube/config/clusters.toml","-rules", "/etc/nanotube/config/rules.toml"]
