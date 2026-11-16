# Experimental placement WAL

The opt-in placement output keeps its existing authenticated frames and durable
sequence scopes. Default jump output is unchanged. Input batches enter the WAL
at `Batch.Flush`; scalar admission waits for a shared commit. Only synced groups
may reach a store, and ACK positions are synced before a move can drain.

```toml
placement_commit_ms = 2
placement_commit_bytes = 262144
placement_segment_bytes = 4194304
```

Zero uses these defaults. The timer bounds a quiet scalar group's commit delay;
the byte threshold commits a busy group sooner. An input batch commits before
returning from its flush. A failed admission may have committed a valid prefix;
do not resubmit that prefix as new records. Transport retries retain their
original positions and payloads.

Checksummed, monotonically numbered WAL events contain appends, held payloads,
and ACK advances. Checkpoints contain the canonical map, hold state, queued
payloads, and committed positions. Rotation publishes a file-synced and
directory-synced checkpoint before deleting older segments, so unacknowledged
payloads remain recoverable. A torn final record is removed on recovery; a
complete record with a bad checksum fails closed. Persistence failure is sticky.

`placement_max_records` includes holds. `placement_max_bytes` bounds the live
checkpoint plus WAL; atomic checkpoint replacement additionally needs up to one
checkpoint of temporary disk space. Keep space for twice this limit. This is a
bounded checkpointing WAL, with queue-copy cost at rotation rather than at every
mutation. It does not provide independent disk latency for moving groups.

Version 1 snapshots are upgraded on reopen. Version 2 checkpoints deliberately
make snapshot-only binaries reject the spool; do not downgrade a nonempty spool.
Stop and reconcile the durable queue before changing the storage format.

Each store connection has at most 256 outstanding frames. The matching store
receiver must retain and recover at least that many exact retry digests per
stream before enabling batched sends. Authentication provides integrity, not
confidentiality. Throughput and allocations still require qualification.
