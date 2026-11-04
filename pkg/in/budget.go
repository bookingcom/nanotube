package in

import "sync"

// BatchBudget bounds TCP input retained while routing falls behind. Batches
// keep their reservation until processing completes, including during shutdown.
// Admission blocks TCP readers rather than dropping their parsed records.
// Other input transports need not participate in this budget.
type BatchBudget struct {
	mu          sync.Mutex
	changed     *sync.Cond
	limit, used uint64
	batches     map[*[]byte]uint64
}

// NewBatchBudget returns nil for the legacy, nonblocking queue policy.
func NewBatchBudget(limit uint64) *BatchBudget {
	if limit == 0 {
		return nil
	}
	b := &BatchBudget{limit: limit, batches: make(map[*[]byte]uint64)}
	b.changed = sync.NewCond(&b.mu)
	return b
}

func batchCharge(batch [][]byte) uint64 {
	// TCP input uses densely packed 64 KiB arenas. Two times the payload
	// covers unused tails; two extra arenas cover either edge of a batch.
	// Include the slice backing array, allocator rounding and reservation entry.
	charge := uint64(cap(batch))*32 + 2*recArenaChunkSize + 128
	for _, record := range batch {
		charge += 2 * uint64(len(record))
	}
	return charge
}

func (b *BatchBudget) acquire(batch [][]byte) {
	charge := batchCharge(batch)
	b.mu.Lock()
	defer b.mu.Unlock()
	// An oversized batch runs alone, so a large record/batch cannot deadlock
	// admission. The bound is max(limit, one batch), plus reader-local batches.
	for b.used != 0 && (charge > b.limit || b.used > b.limit-charge) {
		b.changed.Wait()
	}
	b.used += charge
	b.batches[&batch[0]] = charge
}

// Release accepts any input batch; uncharged input from other transports is
// ignored. Slice element addresses identify backing arrays without retaining a
// second copy or changing the input/worker channel representation.
func (b *BatchBudget) Release(batch [][]byte) {
	if b == nil || len(batch) == 0 {
		return
	}
	b.mu.Lock()
	if charge, ok := b.batches[&batch[0]]; ok {
		delete(b.batches, &batch[0])
		b.used -= charge
		b.changed.Broadcast()
	}
	b.mu.Unlock()
}

// Bytes is the current conservative reservation, not process RSS.
func (b *BatchBudget) Bytes() float64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return float64(b.used)
}
