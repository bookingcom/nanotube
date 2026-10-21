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
	stop    chan struct{}
	done    chan struct{}
}

// NewBatchChan makes a new batched chan buffer.
// It also starts a flushing goroutine in the background if periodSec > 0.
func NewBatchChan(q chan<- [][]byte, bufSize int, periodSec int, ms *metrics.Prom) *BatchChan {
	qb := &BatchChan{
		q:       q,
		bufSize: bufSize,
		ms:      ms,
		period:  time.Second * time.Duration(periodSec),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}

	if periodSec > 0 {
		go qb.periodicFlush()
	} else {
		close(qb.done)
	}

	return qb
}

func (qb *BatchChan) periodicFlush() {
	defer close(qb.done)
	ticker := time.NewTicker(qb.period)
	defer ticker.Stop()
	for {
		select {
		case <-qb.stop:
			return
		case <-ticker.C:
			qb.Flush()
		}
	}
}

// Push pushes a single item to the batched channel.
func (qb *BatchChan) Push(rec []byte) {
	qb.m.Lock()

	if qb.buf == nil {
		qb.buf = make([][]byte, 0, qb.bufSize+1)
	}
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

	if len(qb.buf) == 0 {
		return
	}
	qb.sendToMainQBuf()

	// The sent batch is owned by the receiver now; Push allocates a new one.
	qb.buf = nil
}

// Close stops the periodic flusher and sends the final partial batch. It does
// not close the target channel. The owner must stop calling Push before Close.
// Must be called exactly once for every new instance.
func (qb *BatchChan) Close() {
	close(qb.stop)
	<-qb.done
	qb.Flush()
}

func (qb *BatchChan) sendToMainQBuf() {
	select {
	case qb.q <- qb.buf:
		qb.ms.InRecs.Add(float64(len(qb.buf)))
	default:
		qb.ms.ThrottledRecs.Add(float64(len(qb.buf)))
	}
}
