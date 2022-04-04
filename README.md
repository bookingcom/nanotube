# Nanotube: data router for Graphite

![CI](https://github.com/bookingcom/nanotube/workflows/CI/badge.svg)
[![Go Report Card](https://goreportcard.com/badge/github.com/bookingcom/nanotube)](https://goreportcard.com/report/github.com/bookingcom/nanotube)


This is the router (or relay, or reverse-proxy) for Graphite. It routes incoming records according to the specified rules. The Nanotube is designed for high-load systems. It is used at Booking.com to route up to a million incoming records/sec on a single box with a typical production config.

## Build and run

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

### Go version

The only supported Go version is `1.18`.

### Supported platforms

are *Linux* and *Darwin*.

## Record structure

The main supported protocol is [line](https://graphite.readthedocs.io/en/latest/feeding-carbon.html#the-plaintext-protocol). The records have the structure:

```
path.path.path value datetime
```

The support of [Open Telemtry gRPC](https://github.com/open-telemetry/opentelemetry-proto/blob/main/opentelemetry/proto/metrics/v1/metrics.proto) is experimental.

## Config

Please refer to the [sample config](config/config.toml) for examples and documentation.

## Routing

Is defined in the [rules config](config/rules.toml) that is in turn referred to in the [main config](config/config.toml). This is how it works:

- Routing rules are applied to each incoming record in order;
  - If regex or prefix in a rule matches, the record is sent to the clusters in the `clusters` list;
    - If `continue` is `true` continue matching next rules, stop otherwise. `false` is the default;
  - If no regex or prefix matches, continue down the list of rules;
- Multiple rules can be matched to each record;
- Each record is sent to a single cluster only once. If two rules send it to same cluster, only one instance will be sent;
- Cluster names must be from the set in the [clusters config](config/clusters.toml).

### Rewrites

Optionally, it is possible to apply the [rewrite rules](config/rewrite.toml). This is how they work:

- rewrites are applied before the routing;
- all rules are applied to each record one-by-one in order. The record may be modified along the way;
- rule matches if `from` matches;
  - then metric path is rewriten to `to` in place;
  - if `copy` is `true` the original metric is copied and sent directly to be routed skipping the following re-writes. `copy` is `false` be default.

## Record validation and normalization

Nanotube validates every record it receives. Validation checks performed are:

- does the record consist of three fields
- is value in a record a valid real number
- is time in the record a valid epoch

It is possible to turn on record normalization with the `NormalizeRecords` option in general configuration. It is on by default. Normalizations performed are mainly for backward compatibility with `carbon-c-relay`, namely:

- Double or more subsequent dots in path are converted to a single dot: *a.b..c* -> *a.b.c*
- Prefix and trailing dots in path are removed: *.a.b.c.* -> *a.b.c*
- Spaces in record are normalized to a single space character: *a.b.c␣␣␣1.23\t1234567* -> *a.b.c␣1.23␣1234567*
- Characters not in the set `[0-9a-zA-Z-_:#]` are replaced with `_`. Tags have their own allowed chars but are not supported for now.

## Zero-downtime reload

Nanotube supports zero-downtime reload that can be triggered with `USR2` signal. It will update the binary and try to load updated config. If the config is invalid, the old instance will keep running.

## Tags support

[Tags are not supported for now.](https://github.com/bookingcom/nanotube/issues/4)

## Design

Design details are in the design [doc](docs/design.md).

## OpenTelemetry

We support OpenTelemetry by using [v0.7.0 of protobuf protocol](https://github.com/open-telemetry/opentelemetry-proto/releases/tag/v0.7.0).


## Acknowledgment

This program was originally developed for Booking.com. With approval from Booking.com, the code was generalized and published as Open Source on GitHub, for which the authors would like to express their gratitude.
