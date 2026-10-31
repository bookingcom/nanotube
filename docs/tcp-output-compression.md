# TCP output compression

A single setting in the main configuration enables zstd for every TCP target,
including hosts using multiple TCP connections. gRPC hosts ignore these options.

```toml
TCPOutCompression = "zstd"
TCPOutZstdLevel = "fastest"
TCPOutZstdFrameSize = 1048576
```

`TCPOutCompression` defaults to `""` (plaintext). `"none"` explicitly selects
plaintext. `TCPOutZstdLevel` accepts `"fastest"` (default) or `"default"`.
`TCPOutZstdFrameSize` defaults to 1 MiB and accepts up to 16 MiB; zero selects the
default. Invalid codecs, presets, and frame sizes are configuration errors.

A cluster can override the global codec and a host can override the cluster.
An omitted value inherits. Both `"none"` and `"zstd"` are explicit overrides:

```toml
[[cluster]]
name = "example"
type = "toall"
compression = "none"

[[cluster.hosts]]
name = "127.0.0.1"
port = 2003
compression = "zstd" # canary one host

[[cluster.hosts]]
name = "127.0.0.2"
port = 2003
# Inherits plaintext from the cluster.
```

With compression enabled, the plaintext output buffer uses the frame size in
place of `TCPOutBufSize`. Each flush emits an independent zstd frame using a
reused per-connection encoder with concurrency 1. No codec allocations occur
per frame after warmup. A single oversized record is split into multiple bounded
frames; receivers must support lines spanning frames. `TCPOutBufFlushPeriodSec`
still bounds how long plaintext waits in the output buffer. A timer flush may
emit a shorter frame. Zero disables periodic output flushing, as before; retain
a positive period when low-traffic latency matters. Ingress batching remains
controlled independently by `BatchFlushPerdiodSec` and `MainQueueBatchSize`.

Queueing, drop policy, retries, refresh, reconnect and final draining retain the
existing [TCP output recovery](tcp-output-recovery.md) behavior. Failed writes
retain complete plaintext records for replay. Every connection and retry begins
with a fresh frame, without a shared dictionary or encoder history. TCP has no
application acknowledgement, so a fully or partially delivered failed write can
still produce duplicates on replay. Compression does not provide exactly-once
semantics for ambiguous transport failures.

All counters have `cluster` and `upstream_host` labels:

- `nanotube_tcp_socket_written_bytes_total`: actual wire bytes, including failed
  prefixes and replays. Its meaning is unchanged.
- `nanotube_tcp_plaintext_bytes_total`: plaintext bytes completed by successful
  buffer writes, whether compressed or uncompressed.
- `nanotube_tcp_zstd_frames_total`: frames fully written without a write error,
  including replayed frames.

Compare rates of plaintext and socket-written bytes for the ratio; retries can
affect the ratio because the wire counter includes uncertain failed attempts.
Use detailed metrics (`LessMetrics = false`) to expose these counters.

## Benchmarking and validation

Use vendored dependencies and the Makefile build conventions. Optional sample
benchmarks read a local gzip fixture without printing names or committing data:

```sh
ZSTD_SAMPLE=/path/to/private/sample.gz go test ./pkg/target -run '^$' \
  -bench BenchmarkZstdSampleEncode -benchmem -benchtime=1x -count=3
ZSTD_SAMPLE=/path/to/private/sample.gz go test ./pkg/target -run '^$' \
  -bench BenchmarkZstdTargetWrite -benchmem -count=5
go test -race ./pkg/conf ./pkg/target ./pkg/metrics -timeout 150s
```

The codec benchmark covers fastest/default at 256 KiB, 1 MiB and 4 MiB and checks
each frame against its source bytes. The target-path benchmark cycles through
8192 captured records in batches of 256, or uses synthetic records if no sample
is supplied. Encoding throughput is per core; divide the desired plaintext
bytes/s by encoding bytes/s to estimate the additional relay cores. Measure on
the intended CPU architecture before provisioning from a laptop result.

The go-carbon checkout includes an opt-in `zstdintegration` test with a real
nanotube binary, mixed targets, single/multiple connections, refresh and process
handoff. Its `test/zstd/smoke.py` additionally runs both full executables and a
USR2 application restart. The receiver needs no configuration change.

Rollout order: deploy zstd-capable go-carbon everywhere; deploy nanotube with
compression disabled; canary with a host/cluster override; then enable each pool
or use `TCPOutCompression = "zstd"` globally. Remove any `"none"` overrides when
switching all pools. Monitor decode/write errors, pending records, CPU, and the
wire ratio. Disable sender compression before rolling receivers back.
