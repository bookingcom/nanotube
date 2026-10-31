# Route cache

`RouteCacheBytes = 0` (the default) disables the route cache. To enable it,
set a total byte budget in the main configuration, for example:

```toml
RouteCacheBytes = 2147483648 # 2 GiB, shared by all processing workers
```

This is a starting budget for a few million names. Capacity depends on name
length and the number of rewrite outputs and destinations. Values between 1 and
65535 are rejected. The cache allocates its fixed index at startup and fills
entries on demand. It does not reserve the entire budget up front.

The cache keys on the exact raw path, before normalization. A hit reuses the
normalized path, the original FNV-1a path hash, and the ordered set of matching
cluster destinations for every rewrite output. Values, timestamps and received
times are parsed and generated for every record. Record buffers own their bytes;
cache entries never retain an input batch or share mutable path buffers with
outgoing records. Invalid records are handled through the regular error path.

Rewrites still execute on every record, preserving copy and chained rewrite
behavior and rewrite counters. Matching routing rules on a cache miss preserves
rule order, `continue`, matching rules with no destinations, and destination
deduplication. Blackhole counters increment for each delivery. Routing-regex
duration observations cover cache misses, since hits skip those executions.

Only cluster destinations are saved. Every record still goes through the
cluster's regular push method, including jump selection, host availability,
load balancing, batching and output accounting. The FNV-1a hash and the jump
algorithm are unchanged. Configuration is immutable within a running pipeline;
managed reload starts a replacement process with a new empty cache. The old
process drains with its old configuration and cache.

The cache has 64 locked shards and a fixed eight-way associative index. A random
index hash accelerates lookup; equality also compares the complete raw path.
CLOCK eviction and a fixed limit on eviction probes bound insertion work.
An entry that cannot fit is simply processed without retention. Bucket
collisions or uneven shard occupancy can evict entries before the total budget
is full. There is no TTL: reuse distance and the working set determine hits.

The budget includes the index, cache metadata, entry objects, retained raw and
normalized bytes, rewrite plans and route slices, with conservative allowances
for Go allocation rounding. Shared route slices are charged per entry, so this
can overestimate retained memory. It is a bound on accounted cache storage, not
on process RSS: records, other pipeline buffers, evicted entries still referenced
by workers and garbage awaiting collection are additional memory. During reload,
both processes can retain their own cache until the old process exits.

Metrics are available even with reduced metrics enabled:

| Metric | Meaning |
| --- | --- |
| `nanotube_route_cache_hits_total` | Valid field layouts whose raw path was found |
| `nanotube_route_cache_misses_total` | Raw path lookups that were absent, including normalization errors |
| `nanotube_route_cache_evictions_total` | Entries removed for bucket or byte pressure |
| `nanotube_route_cache_bytes` | Conservatively accounted retained bytes, including fixed storage |

Size the budget from hit ratio, evictions, bytes and process memory under the
actual workload. Disable by setting the budget to zero and reloading.

## Local verification

`BenchmarkRouteCapture` accepts a private gzip line-protocol capture through
`NANOTUBE_ROUTE_CAPTURE`. It derives prefix fixtures without printing or saving
names, builds 45 prefix/regex rules and 45 logical jump destinations with three
replica rings each, and exercises parsing, rewriting, rule resolution and jump
selection. Each ring has 64 discard hosts; output queues and network I/O are
excluded. Setup, decompression and rule construction are outside the timer.

Use `-benchtime=1x -count=3 -benchmem` for cold full-capture passes. Set
`NANOTUBE_ROUTE_CACHE_BYTES=2147483648` to enable caching. Set
`NANOTUBE_ROUTE_STICKY=1` for a replay of every distinct raw name at intervals
from 10 to 60 simulated seconds, with an initial fill followed by 120 simulated
seconds of measured traffic. Simulated time preserves reuse distance without
sleeping. Cache hit ratio excludes that initial fill. The cache has no TTL.

The benchmark reports `ns/record`, `records/pass`, cache bytes and hit percent.
Divide the standard `B/op` and `allocs/op` by `records/pass` to obtain per-record
allocation costs. For CPU profiles use `-tagfocus=phase=parse-route` with pprof
to exclude fixture preparation; background GC samples have no phase label.
