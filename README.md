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
to build
3. Run it with

```
./nanotube -config config/config.toml
```

Command line options:

```
-config string
    Path to config file.
-validate
    Validate configuration files.
-version
    Print version info.
```

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

Get it back with:

```
seq 8080 8081 | xargs -I {port} curl "localhost:{port}/render/?target=test1.test&format=json&from=$(( $(date +%s) - 300 ))&until=$(date +%s)"
```

To test the second store (alone or in conjunction) change the metric path to `test2.test` or `test1.test2`.

Record structure
----------------

The only supported protocol is [line](https://graphite.readthedocs.io/en/latest/feeding-carbon.html#the-plaintext-protocol). The records have the structure:
```
path.path.path value datetime
```

Config
------

Please refer to the [sample config](config/config.toml) for examples and documentation.

Routing
-------

Is defined in the [rules config](config/rules.toml) that is in turn referred to in the [main config](config/config.toml). This is how it works:
- routing rules are applied to each incoming record in order;
    - if regex in a rule matches, the record is sent to the clusters in the `clusters` list;
        - if `continue` is `true` continue matching next rules, stop otherwise. `false` is the default;
    - if regex does not match, continue down the list of rules;
- multiple rules can be matched to each record;
- each record can be sent to a single cluster at most once. If two rules send it to same cluster, only one instance will be sent;
- cluster names must be from the set defined in the [clusters config](clonfig/clusters.toml);

#### Rewrites

Optionally, it is possible to apply the [rewrite rules](config/rewrite.toml). This is how they work:

- rewrites are applied before the routing;
- all rules are applied to each record one-by-one in order. The record may be modified along the way;
- rule matches if `from` matches;
    - then metric path is rewriten to `to` in place;
    - if `copy` is `true` the original metric is copied and sent directly to be routed skipping the following re-writes. `copy` is `false` be default.

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

Design details are in the design [doc](docs/design.md).


Acknowledgment
---------------------------

This program was originally developed for Booking.com. With approval from Booking.com, the code was generalized and published as Open Source on GitHub, for which the authors would like to express their gratitude.
