# go-carbon stream codec

Runtime source and correctness tests mirror
`github.com/go-graphite/go-carbon/receiver/stream/codec`; the original copy was
introduced from `cde09ec1887c5dd5cae4dff980715a22440b4dd5`.
Keep wire behavior compatible with that copy. This relay adds optional decoder
slot/buffer admission and bounded zstd-pool hooks (`SetDictionaryBudget`,
`SetBufferBudget`, `SetDecoderPool`); callers that do not install them retain the
existing behavior. RESET releases capacity when the admission hooks are installed. These local
resource-management hooks are a maintained delta until the receiver adopts them
or the codec is published independently. The v1 specification, sender peer
estimate and golden vector are unchanged. Capture and benchmark helpers are
mirrored too.

`NameTable` owns shared name identities, exact arena-backed bytes or two
independently seeded fingerprints, under a byte budget. `NewSharedEncoder`
keeps only connection-local global-to-wire ids and predictors. Global ids never
enter the wire. `HashedEncoder` remains an alias for API compatibility; no
parallel string map or mirrored known-name writer remains. The decoder can
bind validated definitions to external immutable series references while
keeping predictors and wire ids private to each connection.

Local tokens use a 32-bit shard/slot key and a 31-bit epoch; the remaining bit
belongs to the encoder's predictor. Epochs distinguish tables and slot reuse,
rather than consuming a process-wide generation for each new name. They never
wrap: exhaustion refuses retention and increments the table's exhaustion counter.
A failed resolution returns a table-specific zero-key hint, valid for that
input record, so replicas encode unshared definitions without retrying admission.
Live references are never reclaimed. Reclamation skips entirely referenced
shards, examines at most 64 slots per admission attempt, and uses backshift
deletion instead of rebuilding the index. Periodic sweeps release shard locks
between 64-slot chunks. Capacity and unshared-definition counters expose pressure.

Importing the full receiver module would change unrelated shared dependencies.
This small maintained copy uses the relay's existing vendored compression
library. It can be removed when the codec is published independently or those
dependency updates are deliberately adopted. Golden, differential, peer and
restart tests guard the shared implementation and integration boundaries.
