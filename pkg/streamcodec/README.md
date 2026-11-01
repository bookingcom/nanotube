# go-carbon stream codec

Copied from `github.com/go-graphite/go-carbon/receiver/stream/codec` at
`cde09ec1887c5dd5cae4dff980715a22440b4dd5`. Original source files and golden
vectors are unchanged. `observed.go` adds counter observations around `Add`.
`hashed.go` adds an optional `HashedEncoder` for callers that already have a
stable 64-bit name hash. It verifies names byte for byte and follows collision
chains, delegating definitions, resets and wire encoding to the copied encoder.
Only its known-name append bypasses the string map; differential tests pin this
small fast path against `Encoder.Add`, including implicit frame boundaries,
forced collisions and resets. The extra index costs a uint64-to-id map plus one
uint32 collision link per name. Hashes and collision links never go on the wire.

Importing the whole module upgraded the relay's Prometheus, OpenTelemetry,
compression, protobuf, networking, syscall, and other shared dependencies.
Keeping this small codec copy avoids that unrelated runtime change. Compression
uses the relay's existing vendored version. The copied golden-vector and
round-trip tests pin wire compatibility; update the codec and vectors together.
The copy can be removed when the codec is published as an independent module
or the relay has deliberately adopted the shared dependency updates.
