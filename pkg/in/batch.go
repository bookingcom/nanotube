package in

import (
	"sync"
	"time"

	"github.com/bookingcom/nanotube/pkg/metrics"
)

// BatchChan represents a buffer to send records into a chan in batches.
// BatchChan accumulates records in a buffer. When buffer is full, it sends it as a batch to a chan.
// It also periodically flushes the buffer to prevent losing data when it's not enough to fill the buffer.
type BatchChan struct {
	q       chan<- [][]byte
	buf     [][]byte
	bufSize int
	ms      *metrics.Prom
	m       sync.Mutex
	period  time.Duration
	stop    chan bool
}

// NewBatchChan makes a new batched chan buffer.
// It also starts a flushing goroutine in the background if periodSec > 0.
func NewBatchChan(q chan<- [][]byte, bufSize int, periodSec int, ms *metrics.Prom) *BatchChan {
	qb := &BatchChan{
		q:       q,
		bufSize: bufSize,
		ms:      ms,
		period:  time.Second * time.Duration(periodSec),
		stop:    make(chan bool),
	}

	if periodSec > 0 {
		go qb.periodicFlush()
	}

	return qb
}

func (qb *BatchChan) periodicFlush() {
	for {
		select {
		case <-qb.stop:
			return
		default:
			time.Sleep(qb.period)
			qb.Flush()
		}
	}
}

// Push pushes a single item to the batched channel.
func (qb *BatchChan) Push(rec []byte) {
	qb.m.Lock()

	qb.buf = append(qb.buf, rec)

	if len(qb.buf) > qb.bufSize {
		qb.m.Unlock()
		qb.Flush()
	} else {
		qb.m.Unlock()
	}
}

// Flush immediately sends buffered items to the target chan.
func (qb *BatchChan) Flush() {
	qb.m.Lock()
	defer qb.m.Unlock()

	qb.sendToMainQBuf()

	qb.buf = [][]byte{}
}

// Close closes the batch channel. It does not close the target channel.
// Must be called exactly once for every new instance.
func (qb *BatchChan) Close() {
	close(qb.stop)
}

func (qb *BatchChan) sendToMainQBuf() {
	select {
	case qb.q <- qb.buf:
		qb.ms.InRecs.Add(float64(len(qb.buf)))
	default:
		qb.ms.ThrottledRecs.Add(float64(len(qb.buf)))
	}
}
