package target

import (
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/rec"
	codec "github.com/bookingcom/nanotube/pkg/streamcodec"
	"github.com/dgryski/go-jump"
)

// TypedRoutes belongs to one immutable routing configuration. Common destination
// tuples share a bounded plan; no per-name vector or per-point map lookup exists.
type TypedRoutes struct {
	mu    sync.Mutex
	plans map[[3]*HostMTCP]*TypedRoute
}

// TypedRoute is valid only for static jump clusters whose outputs all share the
// same name table. Other policies retain the regular record pipeline.
type TypedRoute struct {
	hosts [3]*HostMTCP
	count int
	table *codec.NameTable
}

func (c *TypedRoutes) Resolve(route []ClusterTarget, hash uint64) *TypedRoute {
	if len(route) == 0 || len(route) > 3 {
		return nil
	}
	var key [3]*HostMTCP
	var table *codec.NameTable
	for i, target := range route {
		cluster, ok := target.(*Cluster)
		if !ok || cluster.Type != conf.JumpCluster || len(cluster.Hosts) == 0 {
			return nil
		}
		host, ok := cluster.Hosts[jump.Hash(hash, len(cluster.Hosts))].(*HostMTCP)
		if !ok || host.streamOptions == nil || host.streamNames == nil {
			return nil
		}
		if i == 0 {
			table = host.streamNames
		} else if table != host.streamNames {
			return nil
		}
		key[i] = host
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if p := c.plans[key]; p != nil {
		return p
	}
	if len(c.plans) >= 1024 {
		return nil
	}
	if c.plans == nil {
		c.plans = make(map[[3]*HostMTCP]*TypedRoute)
	}
	p := &TypedRoute{hosts: key, count: len(route), table: table}
	c.plans[key] = p
	return p
}

// Push keeps canonical plaintext byte accounting while handing typed columns to
// the same bounded host FIFO. Paths and hint owners remain pinned by Go pointers.
func (p *TypedRoute) Push(path []byte, hash uint64, value float64, ts int64, tokenSource *uint64, batch *Batch) {
	var token uint64
	if tokenSource != nil {
		token = atomic.LoadUint64(tokenSource)
	}
	if !p.table.Valid(token, hash) {
		token = p.table.Resolve(path, hash)
		if tokenSource != nil && uint32(token) != 0 {
			atomic.StoreUint64(tokenSource, token)
		}
	}
	point := streamPoint{name: path, hash: hash, global: token, value: value, timestamp: ts, plainBytes: rec.TypedPlainSize(path, value, ts)}
	batch.typed = append(batch.typed, point)
	pointer := &batch.typed[len(batch.typed)-1]
	for _, host := range p.hosts[:p.count] {
		batch.addTyped(host, pointer)
	}
}

type pointChunk [queueChunkLen]streamPoint

func (q *recQueue) pushTyped(points []*streamPoint) int {
	q.mu.Lock()
	accepted := 0
	if !q.closed {
		accepted = min(len(points), max(q.limit-q.n, 0))
	}
	bytes := 0
	for start := 0; start < accepted; {
		tail := q.tail
		if tail == nil || tail.n == queueChunkLen || tail.points == nil {
			tail = q.newChunk()
			if n := len(q.freePoints); n > 0 {
				tail.points = q.freePoints[n-1]
				q.freePoints = q.freePoints[:n-1]
			} else {
				tail.points = new(pointChunk)
			}
		}
		n := min(queueChunkLen-tail.n, accepted-start)
		for i, p := range points[start : start+n] {
			tail.points[tail.n+i] = *p
		}
		for _, p := range points[start : start+n] {
			bytes += p.plainBytes
		}
		tail.n += n
		start += n
	}
	q.n += accepted
	q.accountAcceptedBytes(accepted, bytes)
	wake := q.waiting && accepted > 0
	if wake {
		q.waiting = false
	}
	q.mu.Unlock()
	q.wakeConsumer(wake)
	return len(points) - accepted
}

func (b *Batch) addTyped(host *HostMTCP, point *streamPoint) {
	id := host.queueID()
	b.grow(id)
	if len(b.recs[id]) == 0 && len(b.points[id]) == 0 {
		b.used = append(b.used, id)
		b.targets[id] = host
	}
	b.note(id, true)
	b.points[id] = append(b.points[id], point)
}

func (h *HostMTCP) pushTypedBulk(points []*streamPoint) {
	if dropped := h.q.pushTyped(points); dropped > 0 {
		h.throttled.Add(float64(dropped))
		h.throttledTotal.Add(float64(dropped))
	}
}

func (s *streamSender) dispatchTyped(points []streamPoint) {
	if s.pointBatches == nil {
		s.pointBatches = make([][]streamPoint, len(s.connections))
	}
	for _, p := range points {
		i := s.connectionIndex(p.hash)
		s.pointBatches[i] = append(s.pointBatches[i], p)
	}
	for i, points := range s.pointBatches {
		if len(points) == 0 {
			continue
		}
		c := &s.connections[i]
		c.lock()
		for j, p := range points {
			if math.IsNaN(p.value) {
				s.ms.Dropped.Inc()
				s.q.accountStreamDrop(p.plainBytes)
				continue
			}
			s.sendPoint(c, p, j == 0)
		}
		*c.lastUse = time.Now()
		s.out.Add(float64(len(points)))
		s.outTotal.Add(float64(len(points)))
		c.unlock()
		clear(points)
		s.pointBatches[i] = points[:0]
	}
}
