package target

import (
	"sync"
	"sync/atomic"

	"github.com/bookingcom/nanotube/pkg/rec"
)

// queueChunkLen is the number of records per queue chunk. The consumer takes
// one chunk at a time, so it is also the largest batch written per lock.
const queueChunkLen = 256

// maxFreeChunks bounds how many emptied chunks a queue keeps for reuse.
const maxFreeChunks = 4

type recChunk struct {
	recs   [queueChunkLen]*rec.RecBytes
	n      int
	next   *recChunk
	points *pointChunk
}

// recQueue is a bounded FIFO of records with many producers and one consumer.
// It holds at most limit records; records pushed beyond that are dropped,
// exactly like a non-blocking send on a channel of capacity limit. Unlike a
// channel buffer, its memory grows and shrinks with the number of queued
// records.
type recQueue struct {
	mu         sync.Mutex
	head       *recChunk
	tail       *recChunk
	n          int
	limit      int
	closed     bool
	waiting    bool
	free       []*recChunk
	freePoints []*pointChunk

	wake   chan struct{}
	output *tcpAccounting
}

func newRecQueue(limit int) *recQueue {
	return &recQueue{
		limit: limit,
		wake:  make(chan struct{}, 1),
	}
}

// push appends records in order while there is room and returns how many
// were dropped. Records pushed after close are dropped.
func (q *recQueue) push(rs []*rec.RecBytes) (dropped int) {
	q.mu.Lock()
	accepted := 0
	if !q.closed {
		accepted = min(len(rs), max(q.limit-q.n, 0))
	}
	for start := 0; start < accepted; {
		t := q.tail
		if t == nil || t.n == queueChunkLen || t.points != nil {
			t = q.newChunk()
		}
		n := copy(t.recs[t.n:], rs[start:accepted])
		t.n += n
		start += n
	}
	q.n += accepted
	q.accountAccepted(rs[:accepted])
	wake := q.waiting && accepted > 0
	if wake {
		q.waiting = false
	}
	q.mu.Unlock()

	q.wakeConsumer(wake)

	return len(rs) - accepted
}

// pushOne is push for a single record; it reports whether it was queued.
func (q *recQueue) pushOne(r *rec.RecBytes) bool {
	rs := [1]*rec.RecBytes{r}
	return q.push(rs[:]) == 0
}

// Requires q.mu.
func (q *recQueue) newChunk() *recChunk {
	var c *recChunk
	if n := len(q.free); n > 0 {
		c = q.free[n-1]
		q.free = q.free[:n-1]
	} else {
		c = &recChunk{}
	}
	if q.tail == nil {
		q.head = c
	} else {
		q.tail.next = c
	}
	q.tail = c

	return c
}

// pop removes the oldest chunk of queued records, blocking while the queue is
// empty. It returns nil once the queue is closed and drained. The chunk must
// be handed back with recycle after use.
func (q *recQueue) pop() *recChunk {
	q.mu.Lock()
	for q.head == nil {
		if q.closed {
			q.mu.Unlock()
			return nil
		}
		// Exactly one producer (or close) clears waiting and sends the
		// wake-up, so the channel never holds a stale token.
		q.waiting = true
		q.mu.Unlock()
		<-q.wake
		q.mu.Lock()
	}
	c := q.head
	q.head = c.next
	if q.head == nil {
		q.tail = nil
	}
	q.n -= c.n
	q.mu.Unlock()

	c.next = nil
	return c
}

// recycle releases the records of a popped chunk and keeps it for reuse.
func (q *recQueue) recycle(c *recChunk) {
	clear(c.recs[:c.n])
	if c.points != nil {
		clear(c.points[:c.n])
	}
	c.n = 0

	q.mu.Lock()
	if c.points != nil {
		if len(q.freePoints) < maxFreeChunks {
			q.freePoints = append(q.freePoints, c.points)
		}
		c.points = nil
	}
	if len(q.free) < maxFreeChunks {
		q.free = append(q.free, c)
	}
	q.mu.Unlock()
}

// close makes pop return nil once the queued records are consumed.
func (q *recQueue) close() {
	q.mu.Lock()
	q.closed = true
	wake := q.waiting
	q.waiting = false
	q.mu.Unlock()

	if wake {
		select {
		case q.wake <- struct{}{}:
		default:
		}
	}
}

// queuedTarget is implemented by hosts backed by a recQueue, which can take
// records in bulk.
type queuedTarget interface {
	Target
	queueID() int
	pushBulk(rs []*rec.RecBytes)
}

var lastQueueID atomic.Int64 //nolint:gochecknoglobals // process-wide source of host queue ids

func nextQueueID() int {
	return int(lastQueueID.Add(1))
}

func targetQueue(t Target) *recQueue {
	switch h := t.(type) {
	case *HostTCP:
		return h.q
	case *Host:
		return h.q
	case *HostMTCP:
		return h.q
	default:
		return nil
	}
}

// Batch stages records per target host so that each host queue is locked once
// per batch instead of once per record. A Batch belongs to one goroutine.
// Records reach the hosts only on Flush; the order per host is preserved.
type batchRun struct {
	typed bool
	stop  int
}

type Batch struct {
	targets   []queuedTarget
	recs      [][]*rec.RecBytes
	points    [][]*streamPoint
	used      []int
	typed     []streamPoint
	runs      [][]batchRun
	lastTyped []bool
}

// NewBatch makes an empty Batch.
func NewBatch() *Batch {
	return &Batch{}
}

func (b *Batch) add(t Target, r *rec.RecBytes) {
	qt, ok := t.(queuedTarget)
	if !ok {
		t.Push(r)
		return
	}

	id := qt.queueID()
	b.grow(id)
	if len(b.recs[id]) == 0 && len(b.points[id]) == 0 {
		b.used = append(b.used, id)
		b.targets[id] = qt
	}
	b.note(id, false)
	b.recs[id] = append(b.recs[id], r)
}

// Flush hands all staged records to their hosts.
func (b *Batch) Flush() {
	for _, id := range b.used {
		if len(b.recs[id]) > 0 && len(b.points[id]) > 0 {
			// Mixed kinds preserve the original single atomic host-queue admission.
			// Only this uncommon case materializes legacy records before queueing.
			merged := make([]*rec.RecBytes, 0, len(b.recs[id])+len(b.points[id]))
			plain, typed := 0, 0
			appendRun := func(run batchRun) {
				if run.typed {
					for _, p := range b.points[id][typed:run.stop] {
						r := rec.NewTyped(p.name, p.value, p.timestamp)
						r.SetPathHash(p.hash)
						r.SetStreamToken(p.global)
						merged = append(merged, r)
					}
					typed = run.stop
				} else {
					merged = append(merged, b.recs[id][plain:run.stop]...)
					plain = run.stop
				}
			}
			for _, run := range b.runs[id] {
				appendRun(run)
			}
			end := len(b.recs[id])
			if b.lastTyped[id] {
				end = len(b.points[id])
			}
			appendRun(batchRun{b.lastTyped[id], end})
			b.targets[id].pushBulk(merged)
		} else if len(b.recs[id]) > 0 {
			b.targets[id].pushBulk(b.recs[id])
		} else if len(b.points[id]) > 0 {
			b.targets[id].(*HostMTCP).pushTypedBulk(b.points[id])
		}
		clear(b.points[id])
		b.points[id] = b.points[id][:0]
		b.runs[id] = b.runs[id][:0]
		b.lastTyped[id] = false
		clear(b.recs[id])
		b.recs[id] = b.recs[id][:0]
	}
	clear(b.typed)
	b.typed = b.typed[:0]
	b.used = b.used[:0]
}

func (q *recQueue) wakeConsumer(wake bool) {
	if wake {
		select {
		case q.wake <- struct{}{}:
		default:
		}
	}
}
func (b *Batch) grow(id int) {
	if id >= len(b.recs) {
		b.recs = append(b.recs, make([][]*rec.RecBytes, id+1-len(b.recs))...)
		b.points = append(b.points, make([][]*streamPoint, id+1-len(b.points))...)
		b.targets = append(b.targets, make([]queuedTarget, id+1-len(b.targets))...)
		b.runs = append(b.runs, make([][]batchRun, id+1-len(b.runs))...)
		b.lastTyped = append(b.lastTyped, make([]bool, id+1-len(b.lastTyped))...)
	}
}

func (b *Batch) note(id int, typed bool) {
	if b.lastTyped[id] == typed {
		return
	}
	n := len(b.recs[id])
	if b.lastTyped[id] {
		n = len(b.points[id])
	}
	if n > 0 {
		b.runs[id] = append(b.runs[id], batchRun{b.lastTyped[id], n})
	}
	b.lastTyped[id] = typed
}
