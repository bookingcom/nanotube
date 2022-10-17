# Nanotube: data router for Graphite

![CI](https://github.com/bookingcom/nanotube/workflows/CI/badge.svg)
[![Go Report Card](https://goreportcard.com/badge/github.com/bookingcom/nanotube)](https://goreportcard.com/report/github.com/bookingcom/nanotube)


This is the router (or relay, or reverse-proxy) for Graphite. It routes incoming records according to the specified rules. The Nanotube is designed for high-load systems. It is used at Booking.com to route up to a million incoming records/sec on a single box with a typical production config.

## Build and run

1. Clone the repo.
2. Navigate to it and run
`make`
to build.
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
-confighash
    Print config hash info.
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

See [here](docs/record_parsing.md) for more details.

The support of [Open Telemetry gRPC](https://github.com/open-telemetry/opentelemetry-proto/blob/main/opentelemetry/proto/metrics/v1/metrics.proto) is experimental.

## Zero-downtime reload

Nanotube supports zero-downtime reload that can be triggered with `USR2` signal. It will update the binary and try to load updated config. If the config is invalid, the old instance will keep running.

## Tags support

Tags are not supported. See [here]](https://github.com/bookingcom/nanotube/issues/4) for details.

## Design

Design details are in the design [doc](docs/design.md).

## OpenTelemetry

We support OpenTelemetry by using [v0.7.0 of protobuf protocol](https://github.com/open-telemetry/opentelemetry-proto/releases/tag/v0.7.0).

## K8s support

Nanotube can run as a daemonset in k8s. It can play a role of the submission sidecar by injecting ports into tagged pods. See [Nanokube doc](test/k8s/README.md) for more info.

## Acknowledgment

This program was originally developed for Booking.com. With approval from Booking.com, the code was generalized and published as Open Source on GitHub, for which the authors would like to express their gratitude.
