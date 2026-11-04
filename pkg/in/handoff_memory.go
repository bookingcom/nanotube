package in

import (
	"runtime"
	"runtime/debug"
	"sync"
)

// Only the retiring process scavenges. At GOGC=100 its unused heap can be as
// large as its live state, and the normal scavenger need not return dictionary
// pages before the successor allocates them. Only readers with nonzero state
// need a completion observer; plaintext/zstd final queue flushes never gate
// transfer or reclamation of other readers.
type handoffMemory struct {
	threshold, released uint64
	collections         int
	free                func()
	completed           chan uint64
	readers             sync.WaitGroup
	done                chan struct{}
}

// PrepareHandoffMemory returns unused heap before fork/exec creates the child.
// It changes no connection state, so a failed replacement leaves input serving.
// Send also collects after readiness, then reclaims reader storage in batches.
func (h *TCPInput) PrepareHandoffMemory() {
	if h != nil {
		var stats runtime.MemStats
		runtime.ReadMemStats(&stats)
		if stats.HeapAlloc >= 64<<20 {
			debug.FreeOSMemory()
		}
	}
}

func newHandoffMemory() *handoffMemory {
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	if stats.HeapAlloc < 64<<20 {
		return nil // Small reloads do not warrant process-wide collections.
	}
	debug.FreeOSMemory()
	return startHandoffMemory(max(8<<20, stats.HeapAlloc/16), debug.FreeOSMemory)
}

func startHandoffMemory(threshold uint64, free func()) *handoffMemory {
	m := &handoffMemory{threshold: threshold, free: free, completed: make(chan uint64), done: make(chan struct{})}
	go func() {
		defer close(m.done)
		for n := range m.completed {
			m.released += n
			// Five batches plus pre-fork, post-readiness and final collection:
			// at most eight explicit collections, independent of socket count.
			if m.released >= m.threshold && m.collections < 5 {
				m.free()
				m.collections++
				m.released = 0
			}
		}
		if m.released > 0 {
			m.free()
		}
	}()
	return m
}

func (m *handoffMemory) track(c *inputConn) {
	if m == nil || c.releaseBytes == 0 {
		return
	}
	// Called after moved, which publishes releaseBytes. done also fences all
	// deferred cleanup. Neither waiting for it nor sending to the collector
	// happens on the transfer goroutine.
	m.readers.Add(1)
	go func() {
		defer m.readers.Done()
		<-c.done
		m.completed <- c.releaseBytes
	}()
}

func (m *handoffMemory) finish() {
	if m != nil {
		go func() {
			m.readers.Wait()
			close(m.completed)
		}()
	}
}
