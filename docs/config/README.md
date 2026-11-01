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
(default false), `max_names` (default 4194304), and `dictionary_bytes`
(default 12582912; connection id-map and predictor capacity), and
`peer_dictionary_bytes` (default 67108864; estimated decoded dictionary capacity
at the receiver, including names). Frames flush at their row or
byte bound, periodically, and on shutdown. The delay cannot exceed one second
or the positive main-config `TCPOutBufFlushPeriodSec`. A zero plaintext flush
period does not disable stream flushing. A v1 reset flushes pending rows before clearing only that connection’s ids
and predictors when its byte or name cap is reached.
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
one connection using an avalanche remix of its cached routing hash. Queue chunks are grouped
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

### Stream dictionary memory

`StreamDictionaryBytes` in the main config defaults to 1073741824 (1 GiB) for
all stream destinations together. Half bounds one shared name table; the other
half is divided equally over every configured stream TCP socket, including idle
sockets. Each connection uses the smaller of this share and its
`stream_options.dictionary_bytes`. This bounds total dictionary capacity as
hosts, replicas, connections and names change. Frames, compression, replay and
input queues have separate limits. Changing the global budget or comparison
mode requires an application restart; destination shares can change on reload.

The shared table resolves each record once before replica fan-out, using the
cached routing hash as its index key. `StreamFullNames = false` (default) keeps
two independently seeded 64-bit fingerprints instead of retained name bytes.
Under the uniform independent hash model, the probability of any collision is
at most `n*(n-1)/2^129`: about 1.47e-23 for 100 million names and 1.47e-21 for one
billion. This is a statistical identity policy, not a cryptographic guarantee.
`StreamFullNames = true` keeps one arena-backed copy and compares complete names
for exact identity. Both policies have the same byte budget and v1 wire format.

Connection maps store four bytes per allocated hash slot plus a 24-byte state
slot per wire id (generation-checked global token and timestamp/value predictors).
An eight-bit hash filter rejects misses before loading that state; exact global
ids still disambiguate collisions. The map grows above 7/8 occupancy. Global
slots have generation checks, so queued hints cannot alias reused ids. Resets
and reconnects release only their own shared references. Unreferenced global
names become reclaimable after one minute; pressure may reclaim them sooner.
Pressure scans inspect 64 slots per insertion, with occasional in-place
index compaction to remove accumulated tombstones. If every global slot is referenced,
new definitions can bypass shared retention until connection resets free space.
No point is dropped for this condition. Names used in new wire definitions are
copied only into bounded frame scratch and released after flushing.

`stream_options.peer_dictionary_bytes` independently bounds the encoder's
estimate of the decoded dictionary, including long names and capacity growth.
Zero selects 64 MiB; explicit values must be between 1048576 (1 MiB) and
1073741824 (1 GiB) bytes. This is a per-host setting applied to every connection;
it does not allocate that amount on the relay or raise its local byte budget.
The encoder resets before either byte limit or the name cap would be exceeded.

For high-churn pools, raise the receiver's `[tcp].stream-dictionary-bytes` first
and restart it, then set the sender's `peer_dictionary_bytes` to no more than
that value. For example, `peer_dictionary_bytes = 268435456` matches a 256 MiB
receiver limit. Also size `stream-dictionary-total-bytes` for the maximum number
of simultaneous stream connections times the per-connection receiver allowance,
with room for overlapping connections during restarts. The receiver reserves the
full allowance per stream. Neither receiver limit reloads on SIGHUP.
These are dictionary limits; frame/compression buffers and cache need additional
memory. There is no limit negotiation in the v1 protocol. If the sender's cap
exceeds the receiver's, an over-budget definition causes a decode error and the
receiver closes the connection before emitting rows from that frame. Pending
sender writes can then reconnect/replay, so a mismatch can cause repeated
failures and uncertain delivery. Destination settings take effect on relay
reload as connections are recreated; global relay memory changes need a restart.

`nanotube_stream_name_table_retained_bytes`,
`nanotube_stream_name_table_retained_names`,
`nanotube_stream_name_table_name_bytes` (zero in fingerprint mode), and
`nanotube_stream_name_table_evictions_total` describe the shared table.
`nanotube_stream_connection_dictionary_bytes`,
`nanotube_stream_connection_map_bytes`, and
`nanotube_stream_connection_names` sum live connections per destination.
Reset counters remain cumulative. The additional
`nanotube_stream_dictionary_resets_by_reason_total{reason=~"peer_bytes|local_bytes|names"}`
counter uses the same destination labels and identifies the binding limit. If
several limits bind at once, peer bytes take precedence, then local bytes, then
names; the reason counters sum to the existing reset counter. Frequent-reset
warnings include both byte limits and counts for each reset reason in that
encoded batch. Shared-table exhaustion also produces rate-limited warnings.

An opt-in churn soak uses the real enqueue, dispatch, frame, compression and
write path with nine hosts, three aligned site replicas and ten sockets per host:

```sh
GOTOOLCHAIN=go1.27.0 GOMAXPROCS=1 GOGC=100 \
  STREAM_CHURN=stream STREAM_CHURN_SCALE=100 STREAM_CHURN_MINUTES=240 \
  go test ./pkg/target -run '^TestStreamChurn$' -v -count=1 -timeout=0
```

Scale divides names, records and byte budgets, while preserving socket and
replica counts. Set scale to 1 for the full traffic shape, `STREAM_CHURN_PHASE=29`
to include the larger burst immediately, or `STREAM_CHURN=plain` for the matched
zstd-plaintext baseline. Fixture construction is outside output process-CPU
measurements. Per-minute output includes RSS, heap, retained capacities, name
counts, definitions, resets and wire bytes. Synthetic results still need a
representative target-architecture canary before a production CPU claim.

`STREAM_CHURN_ROLLUP=1` selects a fixed active pool of 10.8 million names
(about 360k per socket at scale 1), copied across three sites. Cohorts repeat
every 1–60 modeled minutes. Minute zero primes the pool; every quarter hour,
one eighth changes generation and emits four nearby updates. Run 61 minutes
to include all repeat periods and four bursts. `STREAM_CHURN_PEER` sets the
peer cap in bytes, independently of scale. For example, compare 67108864,
134217728, 268435456 and 536870912 with `STREAM_CHURN_TOTAL=4294967296` and
`STREAM_CHURN_BUDGET=16777216`. The latter two local budgets divide by scale.
`STREAM_CHURN_RECEIVER=/path/to/churn_receiver` enables the independent
40-connection receiver with the matching per-connection limit;
`STREAM_CHURN_RECEIVER_LOG=/path/to/receiver.jsonl` receives its measurements.
Use counting sockets without the receiver child for relay CPU comparisons so
pipe writes do not distort output-path cost. Repeat at scale 1 for actual RSS.
