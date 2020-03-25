![CI](https://github.com/bookingcom/nanotube/workflows/CI/badge.svg)
[![Go Report Card](https://goreportcard.com/badge/github.com/bookingcom/nanotube)](https://goreportcard.com/report/github.com/bookingcom/nanotube)


Nanotube: data router for Graphite
=================================

This is the router (or relay, or reverse-proxy) for Graphite. It routes incoming records according to the specified rules. The Nanotube is designed for high-load systems. It is used at Booking.com to route up to `400k` incoming and `1.2M` outgoing records/sec on a single box.

Build and run
-------------

1. Get this repository with
`go get -u github.com/bookingcom/nanotube`
2. Navigate to it and run
`make`
3. Run it with

`./nanotube -config config/config.toml -clusters config/clusters.toml -rules config/rules.toml`

Running with docker-compose
-----------------------------------

Run the full setup including input stack `nanotube` and `go-carbon` with:

```
docker-compose up
```

You can feed in sample data with:

```
echo "test1.test 5 $(date +%s)" | nc -c localhost 2003
```
As many netcat implementations exist, a parameter may be needed to instruct it to close the socket once data is sent. Such param will usually be -q0, -c or -N. Refer to your implementation man page.

Get it back with:

```
seq 8080 8081 | xargs -I {port} curl "localhost:{port}/render/?target=test1.test&format=json&from=$(( $(date +%s) - 300 ))&until=$(date +%s)"
```

To test the second store (alone or in conjunction) change the metric path to `test2.test` or `test1.test2`.

Record structure
----------------

The only supported protocol is [line](https://graphite.readthedocs.io/en/latest/feeding-carbon.html#the-plaintext-protocol). The records should follow the structure:
```
path.path.path value datetime
```

Config
------

Please refer to the sample configs in the _config_ folder for examples and documentation.

Record validation and normalization
-----------------------------------

Nanotube validates every record it receives. Validation checks performed are:

- does the record consist of three fields
- is value in a record a valid real number
- is time in the record a valid epoch

It is possible to turn on record normalization with the `NormalizeRecords` option in general configuration. It is on by default. Normalizations performed are mainly for backward compatibility with `carbon-c-relay`, namely:

- Double or more subsequent dots in path are converted to a single dot: _a.b..c_ -> _a.b.c_
- Prefix and trailing dots in path are removed: _.a.b.c._ -> _a.b.c_
- Spaces in record are normalized to a single space character: _a.b.c␣␣␣1.23<tab>1234567_ -> _a.b.c␣1.23␣1234567_
- Characters not in the set `[0-9a-zA-Z-_:#]` are replaced with `_`. Tags have their own allowed chars but are not supported for now.

Tags support
------------

Tags are not supported for now. See https://github.com/bookingcom/nanotube/issues/4

Design
------

Please refer to the design doc _docs/design.md_.


Acknowledgment
---------------------------

This program was originally developed for Booking.com. With approval from Booking.com, the code was generalized and published as Open Source on GitHub, for which the authors would like to express their gratitude.
