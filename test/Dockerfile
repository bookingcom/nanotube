FROM golang:1.15
LABEL authors="Booking.com"
LABEL name="nanotube-test-receiver"
LABEL vendor="Booking.com"

RUN mkdir /nanotube
WORKDIR /nanotube

COPY . .

RUN apt-get -y update && apt-get -y install bzip2 jq

RUN make nanotube
RUN make test/sender/sender
RUN make test/receiver/receiver

CMD make local-end-to-end-test
