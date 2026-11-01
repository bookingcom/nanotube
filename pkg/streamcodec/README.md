# go-carbon stream codec

Copied from `github.com/go-graphite/go-carbon/receiver/stream/codec` at
`cde09ec1887c5dd5cae4dff980715a22440b4dd5`. Original source files and golden
vectors are unchanged. `observed.go` adds counter observations around `Add`
without changing its encoding or maintaining a second dictionary.

Importing the whole module upgraded the relay's Prometheus, OpenTelemetry,
compression, protobuf, networking, syscall, and other shared dependencies.
Keeping this small codec copy avoids that unrelated runtime change. Compression
uses the relay's existing vendored version. The copied golden-vector and
round-trip tests pin wire compatibility; update the codec and vectors together.
The copy can be removed when the codec is published as an independent module
or the relay has deliberately adopted the shared dependency updates.
