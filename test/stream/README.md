# Stream output integration and measurements

Unit and race tests run with the repository's normal `go test` commands.
The localhost integration test additionally needs a stream-enabled go-carbon
checkout and Python 3 (standard library only):

```sh
python3 test/stream/e2e.py --go-carbon /path/to/go-carbon --scratch /path/to/scratch
```

It builds both binaries, starts isolated go-carbon processes with persistence
disabled, sends through the real nanotube target code, and queries carbonlink.
It checks exact float64 bits, truncated timestamps and multiplicities through
idle reconnects and small dictionary caps, with one, two and ten connections and zstd
on/off. All generated names and listeners are local. Uncertain failed TCP writes
can duplicate delivered frames; deterministic failed-prefix replay is tested in
unit tests, and this integration check uses healthy idle reconnects.

Synthetic output benchmarks include numeric parsing, frame encoding, queue
accounting and mocked socket writes, with 1024 names sampled once per simulated
second, long shared dotted prefixes, counters and changing gauges:

```sh
go test ./pkg/target -run '^$' -bench BenchmarkStreamOutput -benchmem -count=3
```

They compare plaintext round-robin output with stream output at one and ten
connections. Stream benchmarks warm the name dictionaries first. `wire-B/record`
includes headers; `ns/record` measures the sender loop; allocations are reported
per queue chunk (up to 256 records).

A private gzip sample can be measured in one cold pass without retaining the
whole input in memory or printing any metric names:

```sh
NANOTUBE_STREAM_SAMPLE=/path/to/sample.gz \
NANOTUBE_STREAM_SAMPLE_RESULT=/path/to/aggregate-results.json \
go test ./pkg/target -run '^TestStreamSampleReport$' -v -timeout 170s
```

The report compares one connection and ten pinned connections, with zstd on/off,
and includes initial name definitions and preambles. Only an incomplete final
line is excluded and its byte count is reported. Keep samples and generated
reports outside the repository.
