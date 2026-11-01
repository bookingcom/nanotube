# Configuration examples and explanation

Here are the examples of configuration together with explanation how it works.

## Routing

Is defined in the [rules config](rules.toml) that is in turn referred to in the [main config](config.toml). This is how it works:

- Routing rules are applied to each incoming record in order;
  - If regex or prefix in a rule matches, the record is sent to the clusters in the `clusters` list;
    - If `continue` is set to `true`, continue matching next rules, stop otherwise. `false` is the default;
  - If no regex or prefix matches, continue down the list of rules;
- Multiple rules can be matched to each record;
- Each record is sent to a specific cluster only once. If two rules send it to same cluster, only one instance will be sent;
- Cluster names must be from the set in the [clusters config](clusters.toml).

### Rewrites

Optionally, it is possible to apply the [rewrite rules](rewrite.toml). This is how they work:

- rewrites are applied before the routing;
- all rules are applied to each record one-by-one in order. The record may be modified along the way;
- rule matches if `from` matches;
  - then metric path is rewritten to `to` in place;
  - if `copy` is `true` the original metric is copied and sent directly to be routed skipping the following re-writes. `copy` is `false` by default.

See [TCP output compression](../tcp-output-compression.md) for global zstd defaults, cluster/host canaries, metrics, and rollout order.
## Binary stream destinations

Set `stream = true` on an individual host to send go-carbon's binary stream
protocol over TCP. Plaintext remains the default. The destination must run a stream-aware
go-carbon TCP listener, which detects the preamble on its ordinary plaintext
port; stream output cannot share a host entry with `grpc = true`.
`mtcp` works with either TCP format. Each connection starts with a preamble and
an empty dictionary, including after reconnects.

The optional `[cluster.hosts.stream_options]` table sets `max_rows` (default
4096), `max_bytes` (default 1048576; a payload/replay-buffer target), `flush_period_ms` (default 1000), `zstd`
(default false), and `max_names` (default 4194304). Frames flush at their row or
byte bound, periodically, and on shutdown. The delay cannot exceed one second
or the positive main-config `TCPOutBufFlushPeriodSec`. A zero plaintext flush
period does not disable stream flushing. The name dictionary resets when a new
name would exceed its cap. Each MTCP connection has its own cap and memory cost.
There is no cluster-level transport default, matching the existing `grpc` and
`mtcp` configuration style.

Numeric fields use go-carbon's plaintext parsing: both are parsed as float64,
timestamps are truncated to int64, and invalid numbers or NaN values/timestamps
are dropped and counted in `nanotube_stream_records_dropped_total`. Codec size
rejects use the same counter. A single record may exceed the byte target, as with
plaintext output buffering; the codec's hard frame and name limits still apply.
`max_rows` cannot exceed the receiver's 65536-row limit. A non-default name cap
must fit the receiver's `stream-max-names` setting. `max_bytes` is at most 4 MiB;
frames and individual names also obey the codec's 8 MiB and 64 KiB hard limits.

Stream counters `nanotube_stream_frames_total`,
`nanotube_stream_names_defined_total`, `nanotube_stream_dictionary_resets_total`,
and `nanotube_stream_records_encoded_total` have `cluster` and `upstream_host`
labels. They count encoding attempts, including retries. The existing
`nanotube_tcp_socket_written_bytes_total` counts actual wire bytes, including
preambles, compressed frames, failed prefixes, and retries. Pending-byte gauges
continue to use equivalent plaintext bytes for consistent queue accounting.

Queue limits, reconnect backoff, idle refresh, and full-buffer replay retain the
TCP sender's behavior. Stream has no receiver acknowledgements: an uncertain
write may be replayed and produce duplicates, as with plaintext TCP.

In stream mode, a host with multiple TCP connections pins each metric name to
one connection using an independently seeded path hash. Queue chunks are grouped
by connection before sending, preserving each name's point order with one lock
per populated connection batch. Each dictionary holds only its assigned names.
Reconnecting a connection starts a fresh preamble and re-encodes only that
connection's pending share; the other dictionaries keep their ids and deltas.
The host's shared queue, availability and reconnect backpressure are unchanged.
Plaintext MTCP continues to distribute whole queue chunks round-robin.

Stream hosts ignore `TCPOutCompression` and cluster/host `compression` defaults.
Only `stream_options.zstd` controls stream frame compression; no outer zstd
wrapper is emitted. This lets stream and zstd-plaintext hosts share a cluster
with a global compression default. Invalid compression names still fail normal
configuration validation, even when a host selects stream.
