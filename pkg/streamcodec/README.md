# go-carbon stream codec

Runtime source and correctness tests mirror
`github.com/go-graphite/go-carbon/receiver/stream/codec`; the original copy was
introduced from `cde09ec1887c5dd5cae4dff980715a22440b4dd5`.
Update both copies together. The v1 specification and golden vector are
unchanged. Capture and benchmark helpers are mirrored too.

`NameTable` owns shared name identities, exact arena-backed bytes or two
independently seeded fingerprints, under a byte budget. `NewSharedEncoder`
keeps only connection-local global-to-wire ids and predictors. Global ids never
enter the wire. `HashedEncoder` remains an alias for API compatibility; no
parallel string map or mirrored known-name writer remains. The decoder can
bind validated definitions to external immutable series references while
keeping predictors and wire ids private to each connection.

Importing the full receiver module would change unrelated shared dependencies.
This small maintained copy uses the relay's existing vendored compression
library. It can be removed when the codec is published independently or those
dependency updates are deliberately adopted. Golden, differential, peer and
restart tests guard the shared implementation and integration boundaries.
