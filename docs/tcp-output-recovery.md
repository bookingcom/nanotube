# TCP output recovery and shutdown

Both single-connection and multi-connection TCP targets retain complete records
until the socket accepts the whole output buffer. A failed or short write closes
the connection and replays the complete buffer on a fresh connection. This also
applies to periodic and final flushes. Idle refresh flushes pending records before
replacing the connection. An oversized record is sent on its own; otherwise the
pending buffer does not exceed `TCPOutBufSize` (4096 when non-positive, preserving
the previous buffered-writer default).

Graphite TCP has no receiver acknowledgement. If a failed write reports a prefix
as written, replay can duplicate that prefix, including complete records. A
replayed connection always starts at a record boundary. Successful socket writes
do not prove receiver persistence or exactly-once delivery.

No new configuration is needed. Existing `TCPOutBufSize`,
`TCPOutBufFlushPeriodSec`, reconnect/backoff and send-timeout settings apply.
The application's `TermTimeoutSec` still bounds shutdown. Unavailable outputs
retain pending records until they recover or the process reaches that deadline;
they no longer make a failed final flush appear to complete gracefully.

## Metrics

The following TCP metrics have `cluster` and `upstream_host` labels. A record is
one target delivery: summing replicas is not a count of unique input records.
They follow the existing `LessMetrics` flag for detailed target metrics.

| Metric | Meaning |
| --- | --- |
| `nanotube_tcp_pending_records`, `nanotube_tcp_pending_bytes` | Accepted target data awaiting a complete successful write, including queued, in-flight and buffered data. |
| `nanotube_tcp_flushed_records_total` | Records completed by a full successful socket write. |
| `nanotube_tcp_socket_written_bytes_total` | All bytes reported written, including prefixes of failed writes and replays. |
| `nanotube_tcp_write_errors_total` | Failed or short writes requiring reconnection/replay. |
| `nanotube_tcp_uncertain_records_total`, `nanotube_tcp_uncertain_bytes_total` | Prefixes reported written on failed attempts; replay may duplicate these. Repeated failures count repeated attempts. |
| `nanotube_tcp_abandoned_records_total`, `nanotube_tcp_abandoned_bytes_total` | Pending data at the forced-exit snapshot, including uncertain in-flight writes; these are not proven receiver-loss counts. |

`nanotube_shutdown_timeouts_total` is available even with `LessMetrics` enabled.
The existing outgoing counters retain their buffer-admission semantics, and
queue-throttle counters continue to count rejected enqueues only.

Shutdown also logs `Shutdown: TCP output summary` and, for pending/error targets,
`Shutdown: TCP target summary`. Collect these structured logs: a final metric
update before exit may never be scraped. The deadline snapshot does not acquire
connection locks and can overlap an in-flight socket write. It excludes data
still in the input/routing stages; it is not a complete process-loss bound.
SIGKILL, host failure and lost logs cannot be accounted for reliably without
durable state or receiver acknowledgement.

Terminal TCP summaries bypass normal runtime log sampling so every pending/error
target is represented, even when more targets share the same log message than
the configured sampling allowance. Ordinary runtime messages remain sampled.

Healthy cold TCP targets attempt their first connection immediately. Failed
attempts and subsequent reconnects retain the configured backoff and jitter.
This removes an avoidable startup queue stall; it does not guarantee that a
bounded queue can absorb an arbitrary producer catch-up burst.

## Managed SIGUSR2 reload

A replacement now announces pipeline readiness over a private Unix socket after
signal handling, inherited input listeners, routing workers and output senders
are initialized. This does not imply that remote TCP receivers are healthy.
The original process keeps serving if the replacement does not become ready
within `TermTimeoutSec`, terminates the failed child with a bounded drain, and
restores the original pidfile.

On Linux under systemd, configure `NotifyAccess=main` and
`Environment=NANOTUBE_SYSTEMD=1` in the service unit. The explicit mode prevents a command-line process from claiming
ownership of an unrelated service whose environment it inherited. The
tracked old main process sends `MAINPID=<ready replacement>` and waits for the
notification barrier before draining. `Type=simple`, `Restart=always` and
`KillMode=control-group` can remain enabled. On systemd versions that mark the
replacement as a non-child main PID, the unit must use a synchronous `ExecStop`
that sends SIGTERM and waits for process exit. Otherwise a subsequent stop can
escalate to SIGKILL immediately, before the replacement drains. The service's
stop timeout must exceed the application termination deadline. An explicitly
enabled unit without a notify socket rejects SIGUSR2 safely; it can still use an ordinary systemd restart.
Install the unit change with a daemon reload and restart so the process receives
`NOTIFY_SOCKET`. A daemon reload alone does not change an existing process's
environment.

A notification preflight failure leaves the original service running. After an
ownership notification has been queued, the old process remains alive until
systemd processes the barrier; it must not kill the ready child on a late
acknowledgement because systemd may already own that child. SIGTERM/interrupt
still cancels the wait and begins the ordinary bounded drain. No vendor or
Graphite protocol changes are required. Non-systemd processes use the same
private readiness exchange without the ownership notification.

A typical systemd stop command is a synchronous shell command that checks
`MAINPID`, sends SIGTERM, and waits with `tail --pid` (100ms polling). Keep the
service manager's hard-kill backstop after that command; do not disable cgroup
cleanup. A 15-second service stop timeout leaves margin around a 10-second
application termination deadline.

Continue using ordinary service restarts until both the new binary and the
explicit unit mode are installed. An unmarked unit uses the non-systemd reload
path; it does not acquire ownership notification merely by inheriting
`INVOCATION_ID` or `NOTIFY_SOCKET` from another service.
