# Placement routing (experimental)

`type = "placement"` opts one cluster into the go-carbon virtual-shard map.
Existing jump routing and output queues keep their established behavior.

The exact post-parse, post-rewrite name selects its namespace by longest byte
prefix, frozen FNV-1a/jump slots for each bootstrap ring length, and the low
12 bits of CRC32C. That stable identity selects a placement group and replica.
The map's store IDs bind explicit `write` endpoints from the host inventory;
reordering hosts does not resize the frozen bootstrap ring.

## Configuration

This complete example uses the shared bootstrap fixture. It routes the first
replica; another cluster can route the second replica independently.

```toml
[[cluster]]
name = "placement-example"
type = "placement"
placement_map = "internal/v2placement/testdata/map-v1.json"
placement_replica = "site-a"

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

All non-blackhole stores in the map must have configured hosts, including future
move destinations. Missing and duplicate endpoints fail construction before any
connection checks start. Store inventory and endpoints cannot change in a map
reload; configure them before a move. Ordinary TCP, multi-TCP, binary stream and
gRPC outputs keep their existing transport and bounded queue/drop behavior.

## Moving a group

A replica's optional `moving_to` field names a second store incarnation. The
`writer` remains the donor. For example, this fragment means dual delivery:

```json
{"id":"site-a","write_epoch":1,"writer":"a","moving_to":"d"}
```

This is a fragment of a replica, not a complete placement document. Each record
selects both destinations from one immutable snapshot. The scalar path pushes
the same immutable record pointer to both queues; the batch path stages it for
both hosts and preserves each host's order. Other groups continue with one
owner. Blackhole donors/destinations, unknown destinations and self-moves are
rejected. No per-record placement envelopes, sequences, ACKs, disk spool, holds
or fences are used by this relay.

The controller owns this sequence:

1. Publish a successor with `writer=A, moving_to=B`. Relays dual-write the group.
2. A pins a hard-link snapshot of its immutable parts. Copy by checksum to B;
   B installs copied parts as older than parts built from its live input.
3. Incrementally copy A's new parts until the delta is small.
4. Switch reads to B.
5. Publish the next write map with `writer=B`, `write_epoch=E+1` and no
   `moving_to`. Wait for every relay to adopt it and drain old output.
6. Copy one final incremental set from A, preserving copied-part precedence.
7. Collect A's retained copy later, after the store/controller's safety checks.

This relies on the storage contract that reapplying the same `(name, slot)`
point is idempotent and rollups are recomputed from slots. The store must enforce
copied-part precedence. Relay queues may still drop on overflow. A final copy can fill missing slots,
but cannot repair a dropped same-slot overwrite when B already has an older
live-written value: copied parts must stay older than B's live parts. Dual writing
and socket flushes alone do not establish complete target history. Relay drain,
final copying and store/read readiness belong to the move controller. This
change supplies routing, not a controller or a store installation protocol.

## Map adoption

Atomically replace the configured map file, then send SIGHUP. The relay validates
the complete document, exact predecessor digest, revision +1, frozen routing,
store inventory and monotone write epochs before publishing one atomic pointer
per cluster. A writer change must promote the previously declared `moving_to`
and clear that field. An unchanged writer retains its epoch; an active move
cannot be retargeted directly. Clearing `moving_to` with the same writer cancels
dual routing. Further moves use successive maps and epochs.

Malformed, stale or incompatible maps retain the active snapshot, log the error
and increment `nanotube_placement_reload_rejections_total`. SIGHUP changes only
the placement map. SIGUSR2 uses ordinary process replacement; startup accepts a
valid current map at any revision. The configuration file is the restart source
of truth: there is no persisted relay epoch floor, so operators must preserve
its latest revision. Never restore an old map as rollback.

Selections already in flight or staged in a batch can finish under the old map
after adoption. SIGHUP success is not a relay-drain certificate. Drain both
processing and output before the final incremental copy. The stream input's
static jump-only host-plan shortcut rejects placement clusters and falls back
to the same scalar/batch routing logic.

## Contract provenance and verification

`internal/v2placement` is an MIT-licensed source copy of the public go-carbon
placement contract from [PR 1476](https://github.com/go-graphite/go-carbon/pull/1476).
`moving_to,omitempty` is an additive relay extension awaiting store/controller
integration; an older strict decoder rejects documents containing it. Coordinate
that schema adoption before enabling moving maps. When absent, canonical bytes,
digests and routing vectors remain identical. The historical receipt helpers in
that contract are unused by relay routing; relay adoption uses topology and
successor validation plus the dual-owner checks described above.

The unmodified fixtures cover frozen 3/4/7-slot tuples, aliases, repeated owners,
tagged and UTF-8 names and equal vshards with different legacy owners. Canonical
`map-v1.json` digest:

```text
943319b0cc9be1b2061acf9db29fa03bbbe56d24e51112207b06e723d4988354
```

Tests cover compiled golden selectors, legacy equality, dual delivery in scalar
and staged batches, final promotion, restart from the current map, detached
snapshots, invalid/stale adoption, reload metrics and concurrent routing/adoption.
`BenchmarkPlacementCampaignOwners` measures single, one-group and all-group dual
routing with scalar and real batch staging. `scripts/bench_placement_legacy.py`
pairs the current legacy path with the fetched base in alternating windows.
These isolate routing CPU and allocations; they do not measure network throughput.

Dual-writing a group adds exactly one logical record per input record in that
group. If its byte-weighted traffic share is f, total logical egress rises from
B to B(1+f). Compression, fresh stream dictionaries and transport framing affect
actual NIC bytes. Leave headroom before starting a move on a saturated relay.

Keep this change Draft until the store move protocol and schema support are ready.
