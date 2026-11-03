# Stream input and relay clients

The existing TCP listener recognizes a leading-NUL go-carbon v1 preamble,
plaintext, and standard zstd-compressed plaintext. Stream uses its own zstd flag
for independent compressed frames. UDP and gRPC keep their existing input paths.

Each stream connection owns a decoder, predictors and name-ID slots. The relay
interns immutable input names in a 64-shard exact-match table; a slot holds a
reference, not another name allocation. Cached normalization/rewrite/routing plans
are shared by normalized name for the lifetime of the process configuration.
A warm ID reads its pinned plan without a table lookup, lock or name allocation;
regular cluster selection, health/failover, bounded queues, blackholes and rewrite
accounting still run for every point. Plans store cluster routes, never a frozen
host selection, so lb and sticky respond to health changes normally.

Decoded records carry typed numbers through stream output. Plaintext and gRPC
outputs adapt typed records at their established boundaries. Stream output
re-encodes into each destination's independent dictionary; input IDs never cross
connections. No name is copied on a warm cached point. Validated names are bound
to shared references through the codec's name resolver.
Queued records keep those bytes alive across reset and disconnect, independently
of the decoder's reusable arena. Normalization shares unchanged immutable bytes;
rewrites own changed paths. Distinct raw spellings keep their original bytes for
snapshots and share the same canonical routing plan. Invalid names are cached as
invalid; blackholes and rewrites producing multiple records retain their usual
per-point behavior. No input or output wire format changes.

The decoder releases every bound reference on RESET and close, including STATE
definitions restored during accepted-connection handoff. The last name reference
removes the table entry; the last cached-plan reference releases its plan arrays.
The table shrinks its index under churn and retains only its small fixed empty
index after all connections close. Names and paths are ordinary immutable Go
allocations, never reused arena cells, so output queues can safely outlive reset.

## Limits and metrics

Main settings, with zero selecting defaults:

| Setting | Default | Meaning |
| --- | ---: | --- |
| `StreamInputMaxFrameBytes` | 8388608 | Wire and expanded frame ceiling |
| `StreamInputMaxNames` | 1000000 | Dictionary names per connection |
| `StreamInputMaxDictionaryBytes` | 67108864 | Decoder capacity plus each slot's codec name estimate (name bytes + 64) |
| `StreamInputMaxRouteBytes` | 134217728 | Logical cached-plan allowance per connection |
| `StreamInputTotalBytes` | 3221225472 | Global connection allowances, decoder/frame buffers, and shared name/index/route storage |

The dictionary limit accepts 4096–1073741824 bytes. The route limit accepts a
positive value up to 1073741824. Global admission reserves 4096 bytes per stream
before constructing its decoder, and the fixed shared index once at startup.
Decoder slot and wire/raw/row/definition/staging buffer capacity is reserved
before growth. Shared names, index capacity
and cached plans reserve their physical charge once, before retention; additional
connections holding the same name or plan do not reserve another copy.
Per-connection dictionary and route limits remain independent abuse bounds, not
upfront global reservations. Each slot is logically charged the
codec's external-name estimate (name bytes plus 64), the same estimate senders
budget `peer_dictionary_bytes` against, so a sender that fits its peer allowance
is never rejected for sharing metadata. Physical shared storage is counted only
once; a name held by a single connection can exceed its charge by its entry and
index share (about 96 bytes), a name shared by two or more cannot. Global admission
charges those physical costs independently. Each cached
slot is also charged its full plan, and a cached normalized alias its canonical
entry, to the route allowance; cache eligibility is stored in the reference's
type without another per-slot allocation.

A full route allowance stops retaining new plans; those points still route
normally. Uncached slots hold name references only and cannot keep another
producer's plans alive after that producer leaves. Dictionary or global growth overflow rejects
the whole frame and closes the connection with an error, releasing any definitions
already admitted from that frame. Admission also applies to STATE replay.
The sender's `peer_dictionary_bytes` must not exceed the receiver dictionary
allowance, and its name cap must fit the receiver name cap. There is no negotiation.

Size total reservations for actual simultaneous decoder/buffer capacity and unique
names/plans, plus 4KiB per connection and the fixed empty index. Idle streams no
longer consume their maximum allowance. RESET releases dictionary slots, frame
buffers and name/plan references; close and successful handoff release the connection allowance
too. Handoff STATE replay passes the successor's normal growth admission.
The sender still gets its full logical peer allowance while total headroom lasts;
there is no guarantee that every connection can fill its maximum simultaneously.

Stream sockets use a 4KiB read buffer. Decoder frame/row buffers grow with observed
frames and rows rather than configured maxima. Compressed frames borrow zstd
contexts from a listener pool bounded by `GOMAXPROCS`; contexts/windows belong to
the pool, not idle sockets. The pool uses the configured frame/window ceiling.
Temporary growth copies, zstd contexts, queued records, stacks,
socket buffers and overlapping processes need additional memory; these settings
are not an RSS cap. Configure input limits at startup/restart. See the opt-in
`TestStreamAdmissionProcess`/`TestStreamAdmissionFixtures` helpers and
`test/stream_admission_model.py` for a separate-client process model.

Metrics have the `nanotube_` prefix:

- `stream_input_{connections,frames,points,errors,resets}_total`
- `stream_input_route_{hits,misses}_total`
- `stream_input_active_connections`, `stream_input_reserved_bytes`
- `stream_input_dictionary_bytes`, `stream_input_route_bytes`
- `stream_input_shared_names`, `stream_input_shared_bytes`

Dictionary bytes sum decoder capacities and deduplicated names/entries/index
capacities. Route bytes count shared plans once. Shared names count unique raw and
canonical entries; shared bytes are the names/entries/index subset of dictionary
bytes, excluding decoder slots and route arrays. Byte gauges conservatively charge
allocation rounding and route vector capacity, rather than measuring RSS. Unlike
the old per-ID caches, plan headers reside inside the shared name entries.
Gauges publish deltas at frame boundaries and close; after all clients disconnect,
only the fixed index remains. Route hits count points using cached plans; misses
count plan builds (including uncached fallback), so they are not complementary
point counts.
The reservation gauge changes immediately before retained storage grows and when
it is released; it includes each connection's fixed 4KiB allowance. It can exceed
dictionary plus route bytes by those allowances, frame buffers and unpublished deltas.
Decode, truncation and budget failures log errors with peer addresses. Decoded
rows also contribute to normal input counters and rate limiting. Malformed
frames emit no rows. Definitions bind only after complete frame validation.

The executable uses compact pinned-name decoder slots: a direct Go pointer,
two 64-bit predictors and two bitmaps, allocated in 128-entry pages. On 64-bit
Go these pages cost about 25 bytes per retained ID, with no doubled connection
array. The ordinary resolver and arbitrary caller-slot API remain available.
Admission still checks the original conservative peer estimate, so a producer
within its configured peer allowance remains compatible. Input frame staging
uses 32 bytes per compact row rather than the generic 56-byte row.
The aggregate input budget charges actual 3,200-byte pages plus the explicitly
sized page-pointer vector before growth, not the old geometric 40-byte slots.
Wire/raw/row/definition buffers keep their admission hooks. RESET drops pages,
vectors, buffers and staging; close/handoff release connection reservations back
to the shared table's fixed baseline. The sender-facing name + 64 estimate and
conservative logical slot estimate remain unchanged. Listener-owned zstd decode
contexts and process-owned output workspaces serve separate directions.

Without rewrite rules, the routing hash and a pointer to an immutable rule route
live inside the shared name entry. The retained output token increases its
allocation class from 64 to 80 bytes, charged once per shared identity. Ordinary routes need no
per-name plan allocation or route-budget charge. Continued rules that combine
destinations still charge their owned header/vector; rewritten paths retain
bounded independent plans. Normalization, health checks, route ordering and
uncached fallback retain their existing behavior.

Independent output frames share CPU-bounded compression and column/sort scratch.
No flush workspace is held while adding points, writing sockets or running an
application callback. Definition arenas are borrowed without blocking and
returned after their bytes have been copied into the completed frame. Their idle
cache is bounded; active definitions retain the existing frame bounds. The zstd
level and 1 MiB window are unchanged. Connection close releases only connection
state, while process-owned scratch survives for reuse.

Shared output indexes allocate lazily, with sparse probes to protect lookup CPU.
Retired entries carry their own free-list link. Table capacity and the full index
reservation remain bounded by the configured output budget. Output connections
already partition names by a stable mixed routing hash.

For a fresh-process local model, build the executable's test binary and run
`TestRelayMemoryModel` with `RELAY_MEMORY_NAMES=260000` and
`RELAY_MEMORY_FIXTURES` pointing inside an explicit scratch directory. It creates
80 producer streams with 20 producers sharing each name, scales the
224/256 MiB peer/input allowances and 8 GiB output budget, and retains all 330
zstd output encoders. A separate process generates fixtures; another validates
receiver fields, routes, counts and checksums. CPU excludes fixture loading and
includes frame decoding, routing, output encoding/writes and automatic GC during
processing. Samples distinguish pre-GC RSS and live heap; `RELAY_MEMORY_SCAVENGE=1`
adds a separate forced-scavenging diagnostic. Default runs preserve residency
across phases.
The full-name epochs exercise RESET/churn; steady passes replay resident IDs.
Input frames use zstd by default (`RELAY_MEMORY_INPUT_ZSTD=0` selects raw).
Correlated producer dictionaries preserve 20-way sharing among retained names;
`RELAY_MEMORY_PHASE_SPREAD=wide` stresses a larger distinct retained-name set.
`RELAY_MEMORY_NAME_BYTES` overrides the default 60–120 byte name target.
`RELAY_MEMORY_POOL=1 RELAY_MEMORY_NAMES=263500` models a synthetic producer pool at 2% scale:
85-lane / 3.1M names-per-lane / 20-way-sharing shape (13.175M distinct names at
full scale), with 1 GiB input peer allowance scaled to the fixture. Use counts
divisible by 85. The same 33 hosts × ten output streams remain unscaled. It
reports exact input reservation components and checks return to the fixed shared
baseline after close; decoder and staging storage are actually allocated.
Optional `RELAY_MEMORY_PROFILE` writes heap profiles under scratch. Use Linux for
RSS/CPU conclusions: Darwin's page-reuse accounting and syscalls differ.

These changes do not set GC environment variables. Operators can retain the
normal `GOGC` policy and set a per-process `GOMEMLIMIT` with space for both sides
of a reload and non-Go memory. A Go memory limit is soft; measure its CPU cost
before tightening it toward the live heap.

## Client configuration

Both lb and sticky can use per-host `stream = true`. A cluster switch enables all
its TCP hosts. Numeric host options override cluster defaults; zstd is enabled if
true at either scope. A false cluster switch leaves existing per-host opt-ins in
effect. Stream and gRPC are mutually exclusive, including inherited settings.

```toml
[[cluster]]
name = "relays"
type = "sticky"
stream = true
[cluster.stream_options]
zstd = true
max_rows = 256
flush_period_ms = 25
# Example client bounds; match the receiver before raising the peer allowance.
dictionary_bytes = 1048576
peer_dictionary_bytes = 8388608
[[cluster.hosts]]
name = "127.0.0.1"
port = 2003
[[cluster.hosts]]
name = "127.0.0.2"
port = 2003
```

Sticky rendezvous affinity partitions names among relays. Output uses the same
TCP queue, health, retry and reconnect implementation as existing stream targets.
Every reconnect starts a fresh preamble/dictionary and re-encodes pending logical
records. A decoder error closes the connection; it never silently falls back to
plaintext. Install compatible receivers first. Disable the switch and restart
the client to return to plaintext.

For lb/sticky relay clients, omitted `max_bytes` uses the existing
`TCPOutBufSize` (4096 if non-positive), bounding retained logical replay batches
the same way as plaintext. Explicit `max_bytes` overrides this policy. Jump and
toall store clusters retain their established 1MiB default. Smaller batches can
reduce compression, especially for cold/churning names; larger explicit frames
change the in-flight point window. Matching this bound does not add receiver
acknowledgements or prove equal crash loss.

## Restart and delivery limits

USR2 transfers accepted plaintext, zstd and stream sockets and their unparsed
state to a compatible ready replacement. Parsed records drain through the old
routing/output pipeline. See [accepted-input reloads](input-reload.md) for the
versioned exchange, memory/admission bounds, temporary-file use and fallback.

A reload involving an older binary uses the existing connection-close drain and
can lose unread input. These transports have no application acknowledgement or
crash exactly-once guarantee. Failed output writes can replay an ambiguous prefix.
Compression still changes the number of points in flight during an ungraceful
failure.

The original real-process pool oracle and the repeated-input reload oracle check
identities/values and duplicate acceptance independently at two real stores. The
store-handoff case fences source submission; relay reload tests interrupt active
persistent clients. Use the active tests when assessing graceful reload behavior.

The input identity also owns an eight-byte-aligned atomic output token. Records
for one resulting path borrow it, including canonical aliases and uncached
single-path routes. Rewritten multi-path records retain independent hints.
Output generation checks still validate each hint; failed table admission stays
record-local so later records retry. Queued records keep the owning allocation
alive. Record size/allocation class and canonical plaintext byte accounting stay
unchanged. No per-point routing-counter batching is introduced.

`NANOTUBE_RELAY_MODEL=1` enables the real 330-socket steady/burst/RESET CPU
model. `RELAY_MODEL_NAMES`, `RELAY_MODEL_CYCLES` and the two fixture-archive
paths control its local scale. Its direct input adapter uses compact slots and
the listener-style decode pool; output uses the production sender and queues.
The receiver reports actual wire bytes in addition to field/route digests.

`RELAY_MODEL_ORDERED=1` is a bandwidth control: it publishes input frames in a
fixed order while retaining the real concurrent output queues/sockets. Compare
this with the ordinary eight-worker run to distinguish codec bytes from changed
interleaving of the twenty partial values and timer-driven frame boundaries.
