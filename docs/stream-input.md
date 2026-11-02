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
| `StreamInputMaxDictionaryBytes` | 67108864 | Decoder capacity plus each slot's conservative logical name/table charge |
| `StreamInputMaxRouteBytes` | 134217728 | Logical cached-plan allowance per connection |
| `StreamInputTotalBytes` | 3221225472 | Global fixed shared index plus connection admission reservations |

The dictionary limit accepts 4096–1073741824 bytes. The route limit accepts a
positive value up to 1073741824. Global admission reserves the entire dictionary
and route allowance before constructing each decoder, without eager allocation,
and reserves the fixed shared index once at startup. Each slot is charged the
full name, entry and index-growth allowance it can pin, plus a canonical entry
for a normalized alias. This conservative scheme remains fair after the producer
that first introduced a name disconnects. Physical shared storage is counted only
once. Each cached slot is also charged its full plan; cache eligibility is stored
in the reference's type without another per-slot allocation.

A full route allowance stops retaining new plans; those points still route
normally. Uncached slots hold name references only and cannot keep another
producer's plans alive after that producer leaves. Dictionary overflow rejects
the whole frame and closes the connection with an error, releasing any definitions
already admitted from that frame. Admission also applies to STATE replay.
The sender's `peer_dictionary_bytes` must not exceed the receiver dictionary
allowance, and its name cap must fit the receiver name cap. There is no negotiation.

Size total reservations for all simultaneous streams, plus the fixed empty index:
at defaults, 3GiB admits 15 connections at 192MiB each (the old exact 16-connection
boundary also needs the small index reservation). A single small client can use smaller explicit
limits; large producer pools require a larger total. This conservative policy
needs deliberate fleet sizing before enabling all clients. Frame/row buffers,
zstd contexts, queued records and overlapping processes need additional memory;
these settings are not an RSS cap. Configure input limits at startup/restart.

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
point counts. Dictionary accounting is stricter than the codec's peer estimate;
leave headroom in the sender's peer allowance for shared-entry/index metadata.
Decode, truncation and budget failures log errors with peer addresses. Decoded
rows also contribute to normal input counters and rate limiting. Malformed
frames emit no rows. Definitions bind only after complete frame validation.

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
