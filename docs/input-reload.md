# Accepted TCP input across reloads

A graceful SIGUSR2 reload transfers accepted plaintext, zstd plaintext and binary
stream connections to the ready replacement. Already parsed points stay in the
old routing/output pipeline and drain there. The successor parses only the
unparsed tail and new socket bytes, using its new routing configuration.

The existing listener inheritance and pipeline readiness exchange remain the
startup boundary. In a managed service, the service-manager ownership barrier
also completes before input moves. An unsuccessful startup keeps the original
serving. Once accepted input starts moving, transfer failures cannot terminate
the successor: it may already own other live input connections.

## Transfer and state

The predecessor advertises `NANOTUBE_INPUT_HANDOFF=1` to its child. Only a child
that sees this offer appends the `NTINPUT1` capability after the existing PID
readiness line. The old process closes its accept loop after capability agreement
and moves each connection separately. Connections not yet selected keep reading.

A private Unix socket carries two SCM_RIGHTS descriptors per connection: the
accepted TCP socket and a state pipe. The successor starts read-ahead immediately
and acknowledges ownership before the predecessor constructs a large snapshot.
The pipe carries raw bytes, without JSON or base64. State types are private to
handoff version 1; client wire protocols are unchanged.

- Plaintext stops between complete lines. Partial lines and buffered bytes precede
  new socket input. Interrupted reads never turn a partial line into a point.
- Stream input stops between validated frames. Its existing codec snapshot emits
  reset/state frames containing names and timestamp/value predictors, followed by
  the incomplete frame and other buffered wire bytes. Snapshot frames emit no
  points. Route-cache entries are rebuilt against the successor's configuration.
- Zstd retains streaming `Flush` behavior, including frames without a content
  size or final block. A frame-limited source journals the current compressed
  frame in an unlinked temporary file. The successor replays that compressed
  prefix, skips plaintext already returned by the predecessor's decoder, then
  consumes its unparsed plaintext tail and the rest of the compressed stream.
  Complete frames retire the previous journal. The existing 16 MiB decoder
  window/memory limits remain in effect; frame length is not newly restricted.

One connection's snapshot is constructed at a time. Stream snapshots temporarily
require memory proportional to that connection's bounded dictionary; they do not
retain a fleet-wide snapshot. The successor reserves its fixed connection allowance and admits retained
decoder, buffer, shared-name and route growth through the normal
`StreamInputTotalBytes` path. Lower per-connection limits or exhausted growth
admission can reject inherited input and trigger the logged fallback.

Read-ahead keeps at most sixteen 64 KiB blocks plus its current read/consumer
blocks in memory per inherited connection. Excess input spills to an unlinked
file while state generation/replay runs, so large dictionaries do not fill a
small ring and stop reading the client socket. After replay, the handler drains
read-ahead in order and returns to direct TCP reads. A subsequent reload first
pauses any previous read-ahead before sharing the socket again.

A reader has one second to reach a handoff boundary. Transfer deadlines are per
connection: five seconds plus one second per 16 MiB of configured dictionary or
estimated zstd replay work, whichever is larger. Socket and state-pipe writes
share that deadline. Normal routing backpressure still applies to parsed points.
Temporary storage must have headroom for a long open zstd frame and input arriving
during replay. Disk failures use the same explicit connection-close fallback;
no temporary files need to survive a process exit.

## Compatibility and failures

| Predecessor | Successor | Behavior |
| --- | --- | --- |
| Version 1 | Version 1 | Accepted sockets and decoder state move |
| Version 1 | Older binary | Missing capability is detected before any input is paused; normal connection-close drain |
| Older binary | Version 1 | No offer, so the child sends the original PID-only readiness message; normal connection-close drain |
| Version 1 | Unsupported handoff version | Reject before pausing input; normal connection-close drain |

Both binaries must support the same handoff version for lossless accepted-input
reloads. Upgrading from or rolling back to an older binary retains its previous
loss behavior. Neither crash recovery nor application acknowledgements are added.
Failed or ambiguous output writes retain the existing replay/duplicate semantics.

`nanotube_input_handoff_connections_total` counts successful transfers in the
predecessor. `nanotube_input_handoff_failures_total` counts transfer/admission
failures and compatibility fallbacks. `Reload:` ERROR messages bypass runtime
log sampling and identify the failed peer or remaining connections at risk.
They explicitly identify unparsed input/kernel receive tails as potentially lost;
the number of points in an unread compressed or partial frame is unknown. Parsed
points still use the ordinary drain. Successful lifecycle logs include state
bytes and handoff duration; INFO logs follow the normal sampling policy.

## Local validation

Use Go 1.27.0. `go test ./...` includes byte-cut tests for all input types,
continuous zstd flushes, repeated state handoff, a multi-megabyte dictionary and
read-ahead spill ordering. Race-check `pkg/in`, `pkg/metrics` and `cmd/nanotube`.
The larger dictionary test is opt-in:

```sh
NANOTUBE_HANDOFF_STRESS_NAMES=600000 go test ./pkg/in -run '^TestInputLargeHandoff$' -v
```

`test/stream/input_reload_e2e.py` reuses the real-store configuration, counters and
field oracle from the existing restart/client-pool harnesses. Supply prebuilt
relay, producer and stream-capable receiver binaries, and an explicit scratch
folder. It checks identities, float bits, acceptance counts, client write errors,
repeated replacements and version fallback. Its 20 ms sampler reports aggregate
relay CPU and overlapping-process RSS; reload CPU includes ordinary traffic in
the measured interval. The sampler converts Darwin Mach timebase ticks correctly.

```sh
go build -o "$scratch/relay" ./cmd/nanotube
go build -o "$scratch/producer" ./test/stream/reload-producer
python3 test/stream/input_reload_e2e.py \
  --scratch "$scratch" --nanotube "$scratch/relay" \
  --producer "$scratch/producer" --carbon "$scratch/go-carbon" \
  --mode mixed --lanes 100 --points 3000000 --chunk 128 --delay 30ms \
  --reloads 5 --expect-zero
```

The mixed workload keeps 40 stream, 10 zstd and 50 plaintext connections open.
Use `--successor` with an older prebuilt binary for rollback compatibility, or
start the older binary with the candidate as its successor to test first upgrade.
