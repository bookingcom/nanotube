# Compressed frame pass-through

Status: proposed protocol, **not a shipping implementation**. The healthy-path
prototype was removed because it failed the baseline delivery-semantics gate.
Existing
v1 listeners and senders remain supported. No producer or store code was edited.

## Why a stream relay currently encodes again

A v1 input connection owns a dense name-ID space and timestamp/value predictors.
One compressed frame can contain names for many destination sets. Its IDs,
timestamp deltas and XOR values occupy separate columns; the first timestamp
prediction can depend on the previous row. A compressed byte range is therefore
neither a metric nor a destination fragment. Removing other rows also requires
maintaining their predictor histories for later input frames.

Outputs merge producers onto a fixed socket pool. Local ID 0 from two producers
can name different metrics. Even matching names may have different IDs and
predictors on each store connection. Equal codec/compression settings do not
make these dictionary histories interchangeable.

The existing relay already carries typed numbers through record queues; output
uses `TypedValues`, not decimal parsing. It still formats numeric lengths for
plaintext-equivalent accounting, allocates records, selects hosts/sockets,
resolves output identities, translates IDs and recompresses each replica.

## Ownership and partitioning

The configuration generator owns the store rings and rules. Publish one
versioned manifest to producers and relays, including indexed slots, replica
order, normalization, rules and supported rule semantics. Its 32-byte epoch
identifies that complete configuration. Producers execute the data-plane
classification; relays admit only configured route IDs under the same epoch.
There is no independently maintained producer ring.

A producer groups canonical names by their complete ordered destination replica
set before encoding. Each producer session and destination set has an independent
v1 encoder. The relay copies its frames to the named replicas. It never merges
their dictionaries. Equal rings of six slots that use the same hash
yield six sets, rather than 6 cubed.
Unequal rings can create more sets: bound channels and memory and retain a legacy
fallback. The existing router's equal-ring-size constraint needs extending
before an unequal-ring rollout.

Rules, normalization, continued rules, rewrites and blackholes must be evaluated
before encoding. The first production profile should permit only the precisely
supported, differential-tested rules. Unsupported configurations and stale epochs
use the existing v1 input path, where the relay applies its current rules. The
opaque path cannot independently verify hidden names against per-name rules;
it relies on conforming producers and rejects a mismatched manifest. Changing
rules requires a new epoch, not changing a live channel's interpretation.

## Proposed envelope (experimented with locally)

A dedicated connection starts with eight bytes `00 47 43 4d 0d 0a 1a 0a`, version
1, flags 0, and a 32-byte manifest epoch. This is distinct from v1's GCS magic.
All fixed integers are big endian. Every envelope has this 24-byte header:

| Offset | Field |
| --- | --- |
| 0 | Kind: OPEN=0, FRAME=1, CLOSE=2 |
| 1–3 | Reserved zero |
| 4–11 | Nonzero channel ID |
| 12–15 | Payload length |
| 16–23 | Sequence: OPEN=0; FRAME/CLOSE advance by one |

OPEN contains a uint32 route ID and the unchanged ten-byte v1 preamble. FRAME
contains exactly one complete v1 frame, including its 20-byte header and
compressed payload. CLOSE has no payload. The relay maps source-local channel
IDs to process-unique output IDs. A store keeps one v1 decoder per output
channel, even when several channels share a socket. RESET affects only that
channel. The inner frame bytes are unchanged, including STATE and RESET.

The relay checks envelope bounds, reserved bits, sequence, configured route,
clear v1 header bounds and wire CRC-32C. It never decompresses or inspects a
name, ID, timestamp or value. Limits cover owned payload bytes, source count,
channels per source, frame size and configured output socket counts. Credit
reservation is atomic before allocation. The rejected experiment shared frames across synchronous replica writes; one
slow write applied whole-source TCP backpressure, which is insufficient for
independent replica admission and recovery. Ordinary nanotube's
point queues, drop policy and routing are unaffected.

The rejected local producer partitioned the same logical model records. A test
receiver demultiplexed and used the real v1 decoder. Healthy-path unit tests
checked identical inner bytes and decoded fields. That evidence does not cover
queue overflow, lost acknowledgements or transparent store recovery; the
prototype must not be canaried.

## Restart, failure and buffering

Today jump routing keeps the indexed target even if it is unavailable; it does
not rehash onto another store. Keep that behavior. Producer failover selects a
new relay but retains the same store set and manifest epoch.

The rejected prototype closed every source using a failed output socket
generation. Old DATA cannot enter a fresh store dictionary. Fresh sources OPEN
new channel IDs and redefine state. This is fail-closed dictionary handling,
not transparent store-outage recovery. It deliberately has no acknowledgements,
replay journal, periodic snapshots or inherited descriptor transfer. Do not use
its healthy-throughput result as evidence for those delivery guarantees.

A production implementation must retain buffering/recovery rather than assume
compressed DATA is independently replayable. Recommended extension:

1. Producers periodically provide bounded RESET/STATE checkpoints at an explicit
   channel sequence. The relay retains their compressed bytes and subsequent
   frames under a shared byte budget, with separate progress for each replica.
2. Stores acknowledge complete accepted frames, and preserve channel sequences
   and decoder snapshots during graceful handoff. This acknowledgement is cache
   acceptance, not a new durability promise.
3. On a fresh store connection, restore a checkpoint. Replay frames through the
   last acknowledged sequence in STATE_ONLY mode to reconstruct dictionaries and
   predictors without re-emitting accepted points; then deliver pending frames.
   The relay forwards these bytes without decoding. A lost final acknowledgement
   remains an ambiguous delivery unless the store retains deduplication state.
4. A slow replica cannot force unlimited history. Use per-channel byte/row
   credits and bounded producer queues. Prioritize control traffic; one blocked
   channel must not stop unrelated channels on the same source socket. The
   prototype currently has coarser, whole-source TCP backpressure.

The production version must extend the experimental OPEN: include a stable
producer UUID, producer session/stream epoch, route and manifest epoch. A relay's
process-local channel counter cannot identify history after a reconnect. Add
CHECKPOINT, RESUME, ACK/CREDIT and FRAME delivery-mode/range messages (a protocol
version bump, with unknown versions rejected). ACKs identify committed channel
sequence plus replica; STATE_ONLY is a receiver delivery mode, not a rewrite of
the compressed inner payload. Store-side channel ownership must fence simultaneous
writers when a channel moves between sockets/relays. Graceful ownership transfer
waits for the old writer or transfers its outstanding history and progress.

For relay crash recovery stronger than today's queue loss, the producer must keep
unacknowledged frames/checkpoints until end-to-end replica progress permits their
release. Relay-local journals alone cannot recover data after losing that relay.
Without producer retention, document the same abrupt-process-loss boundary as the
baseline and use a new producer stream epoch/dictionary after failover; do not
pretend a new OPEN restores missing history. Graceful reload still requires exact
journal/FD/progress transfer. Slow and dead replica policy must stay independent;
jump routing does not substitute a different indexed store on health changes.

A checkpoint must precede the first retained pending frame. A snapshot of the
producer's current tail cannot initialize older DATA. Discard history only when
all replicas have advanced beyond a usable new checkpoint. Dropping arbitrary
DATA frames breaks predictors; exhaustion must backpressure or explicitly reset
and replay from logical producer records.

For store handoff, snapshot every channel and its last committed sequence at a
frame boundary, then transfer partial envelopes and buffered bytes. For relay
handoff, transfer source/channel bindings, output descriptors and progress, or
use the checkpoint/resume protocol. These are coordinated producer/store/relay
changes, not something the present v1 receiver can infer. The existing nanotube
v1 handoff continues to serve today's senders and the in-relay optimization.

## Exact queue admission is also a protocol requirement

Today's host queue admits a prefix of each publication batch under one lock and
drops the suffix, independently per replica. The compressed format cannot remove
rows while preserving its dictionary and timestamp/XOR predictor histories.
Whole-frame drops, or backpressure replacing drops, do not reproduce this policy.
Partitioning also changes batch boundaries and can change admitted identities.

A compatible design needs original parent-batch ordering and row ranks in clear
metadata, a row count for capacity admission, and per-replica delivery ranges.
"Original order" means the virtual baseline v1 row/ID order, not merely raw
producer call order; new partition-local IDs may sort rows differently. Exact
pending-byte accounting for partial admissions also needs producer-supplied byte
prefix/range totals (including baseline NaN/filter handling). The byte/range
metadata has a real wire/CPU cost and is absent from the healthy prototype.
The relay reserves capacity at the original publication boundary and forwards the
unchanged inner frame plus the accepted ranges. The store decodes *all* rows to
advance predictors, emitting only admitted rows. Rejected rows still consume
transport/decode CPU. A completely dropped frame must still be replayed in
STATE_ONLY mode before any dependent frame. A full checkpoint is the only safe
way to skip that history. Queue bytes, pending counters and NaN/error handling
also need an agreed contract. Today NaNs reach queues and are discarded by the
stream sender; declaring only accepted row counts would change this accounting.

This metadata and admission contract has not been implemented or proved against
the baseline relay. Per-name order is the useful externally guaranteed order; simultaneous
producers have no deterministic total order. The required oracle must use fixed
publication batches and paused consumers for overflow, then compare exact
accepted records per destination and replica. Until that passes, the path stays
disabled by absence, regardless of its CPU measurements.

## Resharding and compatibility

Drain the old epoch's acknowledged work to its original targets. OPEN new
channels with RESET under the new epoch; never reuse old predictors on a new
replica set. Retain the old manifest until outstanding work drains. Resharding
must preserve indexed-slot semantics, including holes, and tolerate a mixed
manifest rollout by rejecting/falling back rather than silently misrouting.

Rollout order: receiver multiplex support and aggregate channel admission;
relay listener with explicit manifest; one allowlisted producer; exact receiver
comparison and restart/failure tests; then wider producer enablement. Preserve
the old listeners throughout. Rollback disables producer multiplex output and
opens fresh legacy v1 streams. No stored metric format changes.

## Direct-to-store alternative

A producer-side library that already has a store router, target workers,
bounded buffers, deadlines and direct-send paths can send directly.
Direct sending removes relay CPU and the network hop, but moves replication
and output connections to producers. Producer egress becomes three copies;
producers need store discovery, versioned rules, independent replica progress,
buffer admission, retry, shutdown and restart behavior. Their epoch completion
must not wait indefinitely for one unavailable store. Reusing the same
partitioned frames across replica connections can avoid encoding three times.

Independent producer queues also change which points win admission compared with
one shared relay host FIFO. Exact overflow parity requires a common ordered
admission/credit authority, or an explicitly approved delivery-policy change;
merely matching the sum of byte limits is insufficient. The current hard
delivery-semantics gate does not allow that policy change.

This is attractive where those producer-side responsibilities already exist.
Multiplexed pass-through retains central fan-out and a central buffering/recovery
boundary, while producer egress stays at one copy. Neither option removes store
point decoding or cache insertion.

## Effort estimates and qualification

These are engineering estimates, not measured schedules. The healthy relay
prototype was rejected; finishing admission and recovery is additional work. Production producer partitioning and protocol
support, including retained definitions needed for snapshots: about 2–3 engineer
weeks across the shared producer library and both integrations. Receiver channel
admission, snapshots and acknowledgements: about 2–3 weeks. Relay replay/credits,
FD handoff and adversarial recovery tests: about 2–3 weeks. Work can overlap;
allow 6–9 engineer weeks in total before treating the new path as a production
replacement. Existing direct routing reduces the direct-send implementation
work, but does not remove its reliability or rollout gates.

Steady producer encoding still runs once per point. Partitioning adds channel
selection and can reduce frame fullness; compression may change. A planning
range of 0–30% extra producer encoding cost requires measurement on the
actual emitters. Store point decoding and cache work still run for all three
replicas. Additional demultiplexing is per frame, but more channel dictionaries
change fixed overhead, ID capacity rounding and admission. With name-sticky
producers, this does not inherently multiply the number of live per-name
predictors. Current receiver per-socket fixed reservations must become bounded
aggregate per-channel growth accounting before rollout.


## Acceptance and rollout gate

Implement and land receiver channel isolation first, behind an off-by-default
listener. Add snapshot/sequence restoration and filtered-row emission before
production use. Then implement relay admission, bounded journals, credits and
FD handoff. Add producer partitioning and snapshot support to the shared producer
library, with both integrations consuming that same implementation. Establish
exact old/new receiver equality on adversarial and randomized inputs including
partial writes, RESET pressure, reconnects, overflow and reloads. Only then enable
one producer/channel on a test relay; retain the legacy fallback for unsupported
rules or epochs. Canaries and deployment are separate decisions.

A store crash after acceptance but before an ACK is fundamentally ambiguous
without deduplication state surviving the crash. Today's v1 relay also cannot
guarantee exactly-once delivery after a partial successful write. The new
protocol must document and test a policy at least as strong as baseline; it must
not claim exactly-once delivery merely because it adds frame sequence numbers.

## Capacity costs that the opaque relay does not remove

A relay currently merges all producer dictionaries into a few outgoing
connection dictionaries. With N source channels per shard, the proposed
receiver instead has about N dictionaries per store. For strictly name-sticky
sources the aggregate live predictor count stays near three copies per name;
shared names and retained historical definitions can multiply it. Measured relay input
shows about 12 references per shared name. That is not
proof of 12 simultaneous writers, but prevents assuming disjoint dictionary
histories. Channel count, total dictionary bytes and checkpoint bytes need
independent aggregate receiver admission. The current receiver reserves its
configured allowance per TCP connection; copying that reservation per channel
would exhaust the store budget.

Producer RESET is now visible directly at every store. The ordinary relay can
keep an existing output definition across an input RESET. The partitioned model
sent roughly twice as many output bytes in its RESET phase as the ordinary
model. Budget receiver definition/normalization work and network peaks, not
only healthy relay memcpy CPU. Encoder workspaces also multiply with the number
of channels, even when the total number of producer-side names does not.

A bounded relay journal needs checkpoint bytes plus compressed pending bytes and
admission metadata. At an example 3 MB/s per relay, 60 seconds of one-copy
history costs 0.18 GB times the load multiplier, before checkpoints and buffers.
Keeping one copy with independent replica cursors avoids three journal payload
copies. Exceeding the bound must follow the agreed point admission/drop policy;
TCP backpressure alone is not an interchangeable overflow policy. A producer
checkpoint after a stretch with no admitted rows can allow discarded history to
be skipped, but only after older admitted frames have drained.

Checkpoint frequency is a capacity tradeoff, not a free once-per-second operation.
For snapshot sizes C_raw/C_wire and interval I, producer snapshot processing is
C_raw/I bytes/s and extra producer-to-relay traffic is C_wire/I. A relay retains
roughly C_wire plus rate × I history per channel family. Longer intervals reduce
producer work but increase cold STATE_ONLY replay at stores; replay must catch up
while normal traffic continues. Healthy stores need not receive every checkpoint.
The 0–30% producer planning range above covers healthy partitioned encoding only;
checkpoint/recovery work is additional and unmeasured.

Receiver channel state must have a bounded logical lifetime across a socket
reconnect, keyed by the stable producer/session/route identity, or be restored
from a producer checkpoint. The receiver's per-TCP `defer d.Close()` cannot provide
that resume behavior. Retain idle logical channels for a bounded grace/lease
period and charge them to aggregate admission; explicitly close/drain completed
sessions. New relay failover must not silently allocate an independent dictionary
under the same logical identity while the old writer is still active.

An alternative to store multiplexing is one existing-v1 TCP connection per
producer/destination channel. Healthy forwarding then needs no store wire-format
change: TCP itself isolates dictionaries. With many producer streams this multiplies the
connection count by the number of destination channels, before extra lanes or
reconnect overlap. Per-connection reservations and
aggregate store limits must be retuned; the store configuration is not
part of the relay. This does not solve baseline overflow or failed-write replay.
We prefer explicit logical channels on a bounded socket pool for the full design,
but dedicated sockets are the smallest healthy-path isolation experiment.
