package codec

import (
	"runtime"
	"sync"

	"github.com/klauspost/compress/zstd"
)

// EncodeAll resets its history for every frame and is safe for concurrent use.
// Bound its independent workspaces by CPU concurrency, rather than retaining a
// compressor (including its history and entropy buffers) for every idle socket.
// This process-owned encoder is never closed by an individual connection.
var frameEncoder = sync.OnceValues(func() (*zstd.Encoder, error) { //nolint:gochecknoglobals // One bounded compression pool per process.
	return zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedFastest), zstd.WithEncoderConcurrency(runtime.GOMAXPROCS(0)), zstd.WithWindowSize(1<<20))
})

type frameWorkspace struct {
	raw  []byte
	cols [3][]byte
	tmp  []row
}

// A flush owns scratch only while sorting, building columns and compressing.
// No workspace is held across Add, transport writes or an application callback.
var frameWorkspaces = sync.OnceValue(func() *workspacePool { //nolint:gochecknoglobals // Process-owned, CPU-bounded frame scratch.
	n := runtime.GOMAXPROCS(0)
	pool := &workspacePool{free: make([]*frameWorkspace, 0, n)}
	pool.ready = sync.NewCond(&pool.mu)
	for range n {
		pool.free = append(pool.free, new(frameWorkspace))
	}
	return pool
})

type workspacePool struct {
	mu    sync.Mutex
	ready *sync.Cond
	free  []*frameWorkspace
}

func (p *workspacePool) take() *frameWorkspace {
	p.mu.Lock()
	defer p.mu.Unlock()
	for len(p.free) == 0 {
		p.ready.Wait()
	}
	n := len(p.free) - 1
	w := p.free[n]
	p.free = p.free[:n]
	return w
}

func (p *workspacePool) put(w *frameWorkspace) {
	w.trim()
	p.mu.Lock()
	p.free = append(p.free, w)
	p.ready.Signal()
	p.mu.Unlock()
}

func (w *frameWorkspace) trim() {
	if cap(w.raw) > DefaultMaxFrameSize {
		w.raw = nil
	}
	for i := range w.cols {
		if cap(w.cols[i]) > DefaultMaxFrameSize {
			w.cols[i] = nil
		}
	}
	if cap(w.tmp) > DefaultMaxRows {
		w.tmp = nil
	}
}

// Definitions need their bytes until Flush, so acquisition cannot block: a
// quiet socket must never hold a resource required to flush a busy one. Bound
// the idle cache; active arenas remain bounded by each encoder's frame limits.
var definitionArenas = sync.OnceValue(func() chan nameArena { //nolint:gochecknoglobals // Bounded idle definition scratch shared by connections.
	return make(chan nameArena, 4*runtime.GOMAXPROCS(0))
})

func takeDefinitionArena() nameArena {
	select {
	case arena := <-definitionArenas():
		return arena
	default:
		return nameArena{quantum: 64 << 10}
	}
}

func (e *Encoder) releaseDefinitionArena() {
	e.arena.reset()
	if e.arena.quantum != 64<<10 || len(e.arena.chunks) == 0 {
		return
	}
	arena := e.arena
	e.arena = nameArena{quantum: 64 << 10}
	if arena.bytes > DefaultMaxFrameSize {
		return
	}
	select {
	case definitionArenas() <- arena:
	default:
	}
}
