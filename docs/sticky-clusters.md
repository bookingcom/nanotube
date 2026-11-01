# Name-affine relay pools

Use `type = 'sticky'` for a pool of interchangeable TCP relays. Each normalized
metric path has one preferred relay, independent of client process, restart,
configuration order, value and timestamp. When it fails, only its names move to
other relays. This reduces the names held by each relay's binary stream
connection dictionaries and improves locality for plaintext compression.

```toml
[[cluster]]
name = 'relays'
type = 'sticky'
    [[cluster.hosts]]
    name = 'relay-a.example.net'
    port = 2003
    [[cluster.hosts]]
    name = 'relay-b.example.net'
    port = 2003
    [[cluster.hosts]]
    name = 'relay-c.example.net'
    port = 2003
```

`index` is unused. Use identical host strings and effective ports on every client:
a DNS alias and its IP address are different identities. Health observations may
briefly differ between clients. Rewrites and normalization must also agree.
TCP plaintext, zstd and binary `stream = true` output, including `mtcp`, work as
usual. Experimental gRPC targets are rejected because they do not expose reliable
connection health. Endpoints must be unique within a sticky pool.

## Placement and failover

The placement contract is FNV-1a-64 over the final normalized path (`PathHash`),
combined by XOR with FNV-1a-64 of `net.JoinHostPort(name, effectivePort)`. Rank each
endpoint by the SplitMix64 finalizer of that value, highest score first. Hashes
and constants are fixed across clients and restarts. Endpoint identity breaks score
ties. This is rendezvous hashing: adding a relay moves approximately `1/(N+1)`
of names to it; removing one moves only its approximately `1/N` share. No
process-local seed or availability-dependent renumbering enters placement.

Connection and write errors use the existing sender's availability flag. The
router skips unavailable relays immediately and uses the next healthy endpoint
in that name's ranking. A full queue also opens the relay's circuit immediately;
queue capacity remains bounded by `HostQueueSize`. Admission retries every other
healthy queue, then unavailable queues, before counting a drop. A batch staged
before a failure is rechecked on flush. No global record lock is added: queue
locks stay per relay, and the common routing path allocates nothing.

A background monitor samples every 100ms. A failed or full relay returns only
after the connection is available and its queue is at most half full for 2s.
Further failures restart that interval. Names then return to their preferred
relay. Independent sender goroutines continue reconnecting and replaying points
already accepted by their relay. Those points are not transferred to another
queue. If all relays are down, points can still queue for later delivery; if all
queues are full, excess points are dropped and counted as before.

TCP output has no receiver acknowledgement. A successful socket write does not
prove receipt, and replay after a partial write may duplicate points. Sticky
preserves the existing output replay behavior, shutdown bounds and per-relay
backpressure; it does not provide exactly-once delivery. Failover and recovery
can also change arrival order across relays.

`lb` retains its existing FNV modulo the available host list behavior. It already
has name affinity while that ordered list stays fixed, but different ordering or
health changes can remap most names. `jump` has no health failover and depends on
stable contiguous indices; it remains suitable for fixed store shards. Sticky
relays must be interchangeable: do not use it to bypass required store sharding.

## Metrics

Always exported, including with `LessMetrics`, when sticky pools exist:

| Metric | Meaning |
| --- | --- |
| `nanotube_sticky_records_total{cluster,upstream_host}` | Points admitted to each relay's queue |
| `nanotube_sticky_failover_records_total{cluster,upstream_host}` | Admitted points whose destination differs from their preferred relay |
| `nanotube_sticky_unhealthy_relays{cluster}` | Relays withheld by connection health, pressure or recovery hysteresis (100ms refresh) |

For these per-relay counters, `upstream_host` is the complete `host:port` endpoint.
They measure admission, not receipt. Existing output, TCP flush/pending/error,
stream and throttling metrics still apply. Final sticky drops are attributed to
the preferred endpoint in `nanotube_throttled_host_records`; a failed queue
attempt followed by successful failover is not counted as a drop.

## Local validation

Run the opt-in harness with four real relay subprocesses, 10,000 points/s, a
50,000-record queue per relay, 3s write timeout and 1s connect timeout. It kills
one relay after a 10,000-point warmup and restarts it after another 2s of input,
counting unique point IDs
at the receivers. Warmup is drained before the kill and fixed-width records make
the unacknowledged-write window comparable. Both modes use the same endpoints,
workload and output sender, and the harness checks sticky loss does not exceed lb:

```sh
NANOTUBE_FAILOVER_HARNESS=1 go test ./pkg/target \
  -run '^TestStickyFailoverHarness$' -v -count=3
# Also exercise TCP FIN's unacknowledged-write window rather than a reset:
NANOTUBE_FAILOVER_HARNESS=1 NANOTUBE_HARNESS_FIN=1 go test ./pkg/target \
  -run '^TestStickyFailoverHarness$' -v -count=3
# For an uncontrolled crash with writes in flight, add NANOTUBE_HARNESS_INFLIGHT=1.
GOMAXPROCS=1 go test ./pkg/target -run '^$' \
  -bench '^BenchmarkClusterCPU$' -benchmem -count=5
```

The CPU benchmark includes routing, actual queue admission, batch flush and
queue recycling for 3, 10 and 40 relays, with 4,096 repeated 120-byte names and
both cached and fresh path hashes. It excludes parsing and socket I/O to isolate the mode's
cost. It reports process CPU (`getrusage`) as `cpu-ns/record` and elapsed time as
`ns/record`; `ns/op` is a batch of 256 records. Measure on an idle
machine and compare medians. Tests also cover membership changes, deterministic
ranking, normalized paths, recovery hysteresis, queue overflow and batch parity.
