# Placement routing (experimental)

`type = "placement"` selects the version-one go-carbon placement document for
one cluster. Other cluster types retain their routing and queue behavior.

The exact post-parse, post-rewrite path selects a namespace by longest byte
prefix, frozen FNV-1a/jump slots for each bootstrap ring length, and the low
12 bits of CRC32C. The resulting immutable group selects a writer incarnation.
The host inventory binds that incarnation's explicit `write` endpoint; changing
host order or discovering an extra host never resizes a bootstrap ring.

## Bootstrap compatibility mode

The current TCP, multi-TCP, binary stream and gRPC outputs do not carry placement
epochs or durable per-stream ACKs. The only configured transport in this Draft is
an explicit **bootstrap compatibility bridge**. It discards the placement envelope
at the existing host queue boundary and retains that queue's overflow/drop
semantics. It cannot write to a receiver requiring stamped placement admission.
Owner changes and moving-group activation are refused on this bridge.

A fresh process accepts only map revision one. In-process SIGHUP adopts a complete
successor document using predecessor digest, revision +1 and canonical validation.
Read/source additions can keep write epochs unchanged; ownership/epoch changes
require activation receipts and an acknowledged transport. Malformed, stale,
partial or incompatible documents retain the previous snapshot and increment
`nanotube_placement_reload_rejections_total`; the process also logs the reason.
The map path is resolved relative to the process working directory. Replace the
file atomically before sending SIGHUP. Reload is atomic **per cluster**.

SIGUSR2 process replacement is refused while a placement cluster is configured:
there is no durable relay producer/epoch authority to coordinate a replacement.
SIGHUP does not change rules, rewrites, producer identity, host inventory or other
configuration. Clusters using the existing routing modes keep their signal and
restart behavior. Map revisions and write epochs must never be lowered to undo
a move. This Draft does not implement placement restart/replay.

A complete example uses the unmodified golden map in this repository, with four
explicit inventory endpoints; the second replica in that map is not dispatched
by this cluster. The bridge is suitable for offline routing comparison only.

```toml
[[cluster]]
name = "placement-example"
type = "placement"
placement_map = "internal/v2placement/testdata/map-v1.json"
placement_replica = "site-a"
placement_producer = "relay-1/inc-1"
placement_stream = "lane-0"
placement_hold_limit = 1024
placement_legacy_wire = true

[[cluster.hosts]]
name = "a"
port = 2003
[[cluster.hosts]]
name = "b"
port = 2003
[[cluster.hosts]]
name = "c"
port = 2003
[[cluster.hosts]]
name = "d"
port = 2003
```

Host indexes are unused in placement mode. Missing/duplicate configured endpoints
are errors. Both scalar and batch routing call the current placement gate;
placement records are not staged as old host destinations in a `target.Batch`.
The stream input's static jump-only host-plan shortcut rejects placement clusters
and falls back through this same gate. Existing jump expressions remain intact.

## Acknowledged transport/control integration seam

`internal/placement.Router` compiles the document once, then emits a per-destination
`Delivery` value containing the record pointer, PG, vshard, replica, store,
map digest/revision, write epoch, producer lane and sequence. It never mutates the
shared record to stamp it: replicas can use different epochs for the same record.
Steady-state routing and envelope construction allocate no memory per record.

The internal API is tested with a trusted simulated transport. No network adapter
or operational move-controller endpoint is supplied:

1. `BeginMove(group)` closes only that group's dispatch gate and returns the old
   epoch's final accepted sequence. New records enter its bounded in-memory hold.
   A hold overflow is rejected and metered; other groups keep dispatching.
2. The adapter drains the old lane to **durable** receiver acknowledgements and
   calls `Acknowledge(group, epoch, sequence)`. Enqueue/write/flush completion and
   an empty socket or queue are insufficient. Enqueue must be nonblocking and
   must not call Acknowledge synchronously.
3. The controller obtains the old owner's fence and new owner's coverage receipts
   for every producer lane, then calls `Adopt(nextMap, activations)`. The canonical
   validators check all changed epochs, and this relay also checks that the local
   lane's final/durable position exactly matches its barrier. Invalid receipts
   or unacknowledged in-flight records leave the current map and hold intact.
4. Successful adoption installs one immutable snapshot. Held records receive the
   new epoch when dispatched, in admission order. A full new sink keeps the held
   suffix; `RetryHeld(group)` retries without losing or overtaking that suffix.
   Stale ACKs from the old epoch are rejected.

The map schema deliberately has no `moving` phase field. `BeginMove` is a separate
control operation; inventing a field in the canonical JSON would break the shared
contract. A failed fence never implicitly cancels its hold. Transport inventory
changes are conservatively rejected, including same-epoch endpoint edits.

Hold counters and the pending gauge are exported per cluster:
`nanotube_placement_held_records_total`,
`nanotube_placement_dropped_records_total`, and
`nanotube_placement_pending_records`. Core router counters are atomics collected
at scrape time, without per-record Prometheus label lookups.

The first operational move still needs a durable, epoch-aware output queue,
stable authenticated producer identities, per-stream ACK/replay storage, an
actual control integration and restart reconciliation. The current hold is not
durable; process termination loses it. A relay canary must wait for those seams.

## Contract provenance and verification

`internal/v2placement` is a source copy of the public go-carbon placement v1
contract, developed in [go-carbon PR 1476](https://github.com/go-graphite/go-carbon/pull/1476).
Its MIT license is retained in that directory. It is intentionally outside
`vendor/`; contract updates must preserve fixtures and canonical digests.
The relay-specific compiler and dispatch/barrier state are in `internal/placement`.

The unmodified golden fixtures cover frozen 3/4/7-slot tuples, repeated owners,
alias prefixes, tagged and UTF-8 names, and equal vshards with different legacy
owners. The decoded canonical `map-v1.json` digest is:

```text
943319b0cc9be1b2061acf9db29fa03bbbe56d24e51112207b06e723d4988354
```

Tests cover scalar/batch legacy equality, the stream shortcut guard, malformed
and stale reload rejection, detached snapshots, concurrent routing/adoption,
barrier ordering, full holds/sinks, old-epoch ACK rejection and allocation counts.
`BenchmarkRouting` separates selector cost from the dispatch gate;
`BenchmarkPlacementCampaignJump` is also run unmodified against the fetched base.
These microbenchmarks do not establish line-rate CPU or throughput equivalence.
