FROM golang:1.12

WORKDIR /go/src/github.com/bookingcom/nanotube
COPY . .
COPY ./docker/nanotube/config/ /etc/nanotube/config

RUN make

EXPOSE 2003

ENTRYPOINT [ "/go/src/github.com/bookingcom/nanotube/nanotube", "-config", "/etc/nanotube/config/config.toml","-clusters", "/etc/nanotube/config/clusters.toml","-rules", "/etc/nanotube/config/rules.toml"]
