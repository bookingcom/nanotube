FROM golang:1.15
LABEL authors="Booking.com"
LABEL name="nanotube-test-receiver"
LABEL vendor="Booking.com"

RUN mkdir /nanotube
WORKDIR /nanotube

COPY cmd cmd
COPY pkg pkg
COPY test test
COPY Makefile go.mod go.sum .

RUN apt-get update && apt-get install bzip2

RUN make nanotube
RUN make test/sender/sender
RUN make test/receiver/receiver

CMD make local-end-to-end-test
