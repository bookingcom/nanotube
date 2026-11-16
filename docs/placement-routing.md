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
epochs or durable per-stream ACKs. Using them with placement requires
an explicit **bootstrap compatibility bridge**. The authenticated durable output
is described below. It discards the placement envelope
at the existing host queue boundary and retains that queue's overflow/drop
semantics. It cannot write to a receiver requiring stamped placement admission.
Owner changes and moving-group activation are refused on this bridge.

The bridge accepts only map revision one on fresh process startup. In-process SIGHUP adopts a complete
successor document using predecessor digest, revision +1 and canonical validation.
Read/source additions can keep write epochs unchanged; ownership/epoch changes
require activation receipts and an acknowledged transport. Malformed, stale,
partial or incompatible documents retain the previous snapshot and increment
`nanotube_placement_reload_rejections_total`; the process also logs the reason.
The map path is resolved relative to the process working directory. Replace the
file atomically before sending SIGHUP. Reload is atomic **per cluster**.

SIGUSR2 process replacement is refused while a placement cluster is configured:
automated producer/epoch handoff between live processes is not implemented.
SIGHUP does not change rules, rewrites, producer identity, host inventory or other
configuration. Clusters using the existing routing modes keep their signal and
restart behavior. Map revisions and write epochs must never be lowered to undo
a move. Restart/replay is supported only by the durable output described below.

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
The selector and gate with a non-persisting sink allocate no memory per record.
The durable output copies numeric payloads and allocates during persistence.

The internal API is tested with both a simulated transport and the authenticated
durable output below. An operational move-controller endpoint is not supplied:

1. `BeginMove(group)` closes only that group's dispatch gate and returns the old
   epoch's final accepted sequence. New records enter its bounded hold. The durable
   output persists this hold; the plain router alone keeps it in memory.
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

The durable output below supplies persisted holds, stable authenticated identities
and per-stream ACK/replay storage. An operational move still needs controller
integration, restart reconciliation across all participants and throughput
qualification before a relay canary.

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

## Authenticated durable output experiment

A placement cluster can select the epoch-aware output instead of the explicit
legacy bridge. This is an experimental correctness path: the current spool
fsyncs a bounded complete snapshot per mutation and the store drains its journal
per frame. It has no batching or line-rate qualification. Keep the ordinary
`jump` mode for existing configurations until those costs are addressed.

```toml
[[cluster]]
name = "example"
type = "placement"
placement_map = "/var/lib/relay/placement.json"
placement_replica = "site-a"
placement_producer = "relay-a/inc-1"
placement_stream = "lane-0"
placement_hold_limit = 1024
placement_spool = "/var/lib/relay/spool/site-a-lane-0"
placement_max_records = 16384
placement_max_bytes = 268435456
placement_key_file = "/etc/relay/producer.key"
```

The key file contains 32..4096 raw bytes, including any newline. Provision a
unique secret per producer incarnation to that relay and its store endpoints;
protect the file with filesystem permissions. HMAC-SHA256 authenticates the
complete bounded `V2A1` frame and its exact ACK, including producer/lane, group,
replica, store incarnation, map digest/revision, epoch, vshard, stream position,
name, timestamp and IEEE value bits. This protocol provides authentication and
integrity; use a trusted network or a TLS dial wrapper for confidentiality.
It uses a dedicated placed store endpoint, without a legacy fallback. Configured
legacy `hosts` are unused by this output; endpoints come from the canonical map.
Blackhole stores are rejected by the durable output.

One spool root belongs to one persistent producer/lane/replica and one process.
The advisory lock prevents overlapping incarnations. A successful Push means
its immutable numeric payload and original envelope, or its moving-group hold,
were committed with file and directory fsync. Held and unacknowledged records
share the total record bound; a full record bound rejects and meters admission.
The byte bound covers the map, barriers and queued payloads together. Exhausting
that bound or failing persistence stops this output until restart/reconciliation.
A checksum detects snapshot corruption. No secret is persisted in the spool.

Each store has an independent worker and connection. Lost sockets, wrong ACKs
and uncertain acceptance leave the original frame on disk for bounded retries.
Only an exact authenticated durable ACK advances the per-group/epoch/lane
frontier and removes that frame. The ACK position and queue removal are synced
in the same snapshot. Never turn queue emptiness or a socket flush into a fence.
A process restart requires the configured map, producer/lane and hold limit to
match the committed spool; advanced revisions are accepted only from that
existing state. Capacity may be raised without changing identities.

The controller APIs are `Cluster.BeginPlacementMove(group)`,
`Cluster.PlacementDrainStatus(group)` and `Cluster.AdoptPlacement(doc, receipts)`.
The first persists the hold before returning its final old-epoch position; the
second reports whether that position has durable ACK coverage. Wait for every
producer lane, quiesce the store's placed receiver, then use the store's checked
fence API and obtain actual target coverage. Adoption validates the canonical
fence/coverage receipts and atomically persists the new map, epoch reset and
ordered held suffix. Unacknowledged old-epoch records are retried to the old
owner until drained; held records are first stamped for the new owner at E+1.
Unrelated store workers continue while the moving group is held. Metadata-only
adoption also requires a drained queue, and cannot change an active barrier's
map identity. Transport inventory changes and SIGUSR2 process replacement remain
rejected. There is no operational HTTP/controller endpoint in this experiment.

The matching store API is `Store.ReceivePlacedTCP(listener, config, resolver)`
on the v2 branch of go-carbon. `Config.PlacementKeys` maps authenticated producer
incarnations to independent secrets. A journaled L2 store accepts raw points;
L3 requires its canonical policy resolver. One daemon root currently owns one
PG; a host with several PG roots needs a dispatching PlacedSink. The receiver
owns producer methods until `Server.Wait` completes. `Store.DurableRelayEnds`
and `Store.FenceRelayPlacement` then check the complete final census against
positions recovered from the very same stamped data journal. Store ACKs follow
catalog and journal sync, and do not independently attest target copy coverage.
Validated historical map authority permits original journal replay after map
adoption while live old-map intake remains closed.

`internal/v2wire` and its Python-calculated `testdata/frame-v1.hex` are copied
byte for byte in both repositories. The ordinary gates include SIGKILL spool
reopen, lost ACK replay, advanced-map restart, bounded admission, old/new epoch
ordering, independent stores, store SIGKILL and duplicate/stale rejection. The
opt-in `TestExternalStoreACK` additionally runs this relay against the store's
loopback `TestRelayACKSurvivesStoreSIGKILL` helper; both roots must be retained
when repeating it across a store kill. These are offline correctness receipts,
not power-loss, throughput, complete move-controller or deployment evidence.
