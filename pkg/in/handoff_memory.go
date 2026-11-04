package in

import (
	"context"
	"runtime"
	"runtime/debug"
)

// Only the retiring process scavenges. At GOGC=100 its unused heap can be as
// large as its live state, and the normal scavenger need not return dictionary
// pages before the successor allocates them. Batch collections by actual reader
// storage, rather than by socket count or the configured dictionary maximum.
type handoffMemory struct {
	threshold, released uint64
	free                func()
}

// PrepareHandoffMemory returns unused heap before fork/exec creates the child.
// It changes no connection state, so a failed replacement leaves input serving.
// Send also collects after readiness, then reclaims reader storage in batches.
func (h *TCPInput) PrepareHandoffMemory() {
	if h != nil {
		newHandoffMemory()
	}
}

func newHandoffMemory() *handoffMemory {
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	if stats.HeapAlloc < 64<<20 {
		return nil // Small reloads do not warrant process-wide collections.
	}
	m := &handoffMemory{
		threshold: max(8<<20, min(256<<20, stats.HeapAlloc/8)),
		free:      debug.FreeOSMemory,
	}
	m.free()
	return m
}

func (m *handoffMemory) release(n uint64) {
	if m == nil {
		return
	}
	m.released += n
	if m.released >= m.threshold {
		m.free()
		m.released = 0
	}
}

func (m *handoffMemory) finish() {
	if m != nil && m.released > 0 {
		m.free()
	}
}

func waitHandoffRelease(ctx context.Context, c *inputConn) error {
	select {
	case <-c.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
