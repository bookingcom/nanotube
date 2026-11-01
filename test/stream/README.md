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

The larger Unix CPU benchmark compares zstd plaintext and zstd stream through
the same per-host output harness: 250,000 distinct 80-140 byte names with shared
prefixes, ten connections, four changing integer/short-decimal samples per name,
one timestamp epoch, and shuffled input. Routing hashes are prepared before
measurement. Both paths include queueing, accounting, compression and writes to
a memory sink, excluding network/receiver CPU. Dictionary and buffer warmup is
excluded. `cpu-ns/record` measures process user+system CPU; `ns/record` measures
elapsed time. Run serially on an otherwise quiet machine:

```sh
go test ./pkg/target -run '^$' -bench '^BenchmarkStreamOutputCPU$' -cpu=1 -benchtime=3s -count=5
```

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

The complete relay executable, two separate go-carbon executables and real
Whisper persistence can also be exercised through restarts:

```sh
python3 test/stream/restart_e2e.py --go-carbon /path/to/go-carbon --scratch /path/to/scratch
```

Compressed Whisper enables out-of-order storage so independent routing workers
can deliver every timestamp even when their queue arrival order differs.

The default run sends the same three million synthetic points to both stores,
first with stream/plaintext and then with stream/zstd-plaintext. It restarts both
stores with accepted-socket handoff after the first third and reloads nanotube
with USR2 after the second third. The producer pauses at these boundaries; its
store connections remain open across go-carbon's restart, and it reconnects to
the replacement relay after graceful draining. Byte-level receiver tests cover
interruptions inside preambles and frames.

The oracle checks every name/value/timestamp through the stores' render API and
compares SHA-256 checksums with the source. Independent receiver acceptance counts
must equal the source count, including across the handoff: combined with every
source point being present, this detects duplicates hidden by Whisper's last
write semantics. Transparent localhost proxies count actual application wire
bytes, including preambles and reload connection setup. Queues hold at least the
complete bounded input, and generation is paced to avoid testing queue overflow.
Generated configurations, data files and binaries are removed on exit; only the
printed aggregate results need to be retained. This proves healthy restart/reload
delivery; failed writes can still duplicate points without acknowledgements.
