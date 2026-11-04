package in

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/metrics"
	"github.com/bookingcom/nanotube/pkg/ratelimiter"
	"go.uber.org/zap"
)

// AcceptAndListenTCPBuf is batched version of AcceptAndListenTCP.
func AcceptAndListenTCPBuf(l net.Listener, queue chan<- [][]byte, term <-chan struct{},
	rateLimiters []*ratelimiter.SlidingWindow, cfg *conf.Main, connWG *sync.WaitGroup, ms *metrics.Prom, lg *zap.Logger, budget *BatchBudget, stream ...StreamFactory) {
	defer connWG.Done()
	NewTCPInput(queue, term, rateLimiters, cfg, ms, lg, budget, stream...).Serve(l, term)
}

// recArena hands out copies of records carved from larger chunks, so that
// copying a record does not cost an allocation. Records are never written
// after being handed out; a chunk is freed once no record references it.
type recArena struct {
	chunk []byte
}

const recArenaChunkSize = 64 * 1024

func (a *recArena) copy(b []byte) []byte {
	if len(b) > recArenaChunkSize/4 {
		return append([]byte(nil), b...)
	}
	if len(b) > cap(a.chunk)-len(a.chunk) {
		a.chunk = make([]byte, 0, recArenaChunkSize)
	}
	off := len(a.chunk)
	a.chunk = append(a.chunk, b...)
	return a.chunk[off:len(a.chunk):len(a.chunk)]
}

func scanInput(c *inputConn, queue chan<- [][]byte, term <-chan struct{}, rateLimiters []*ratelimiter.SlidingWindow, cfg *conf.Main, ms *metrics.Prom, lg *zap.Logger, budget *BatchBudget, stream ...StreamFactory) {
	reader, source := c.reader()

	wire := reader
	var compressed *zstdInput
	inheritedMode := byte(0)
	if c.prefix != nil {
		var err error
		inheritedMode, err = reader.ReadByte()
		if err != nil {
			lg.Error("Reload: missing input state header", zap.Error(err))
			return
		}
	}
	switch inheritedMode {
	case 1:
		var err error
		reader, compressed, err = inheritedZstd(wire)
		if err != nil {
			ms.InputHandoffFailures.Inc()
			lg.Error("Reload: zstd input state rejected", zap.Error(err))
			return
		}
		defer compressed.Close()
	case 0:
		first, _ := reader.Peek(1)
		if len(first) > 0 && first[0] == 0 {
			if len(stream) > 0 && stream[0] != nil {
				readStream(reader, term, rateLimiters, cfg, ms, lg.With(zap.String("peer", c.RemoteAddr().String())), stream[0](), c)
			} else {
				ms.ErrorRecs.Inc()
				lg.Error("stream input has no routing sink", zap.String("peer", c.RemoteAddr().String()))
			}
			return
		}

		// Stream framing reads exact lengths and needs only a small read buffer.
		// Plaintext retains its established 64 KiB line ceiling.
		reader = plaintextReader(reader, source)
		wire = reader
		marker, _ := reader.Peek(4)
		if bytes.Equal(marker, []byte{0x28, 0xb5, 0x2f, 0xfd}) {
			var err error
			compressed, err = newZstdInput(wire)
			if err != nil {
				ms.ErrorRecs.Inc()
				lg.Error("zstd input failed", zap.Error(err))
				return
			}
			defer compressed.Close()
			reader = bufio.NewReaderSize(compressed, bufio.MaxScanTokenSize)
		}
	default:
		ms.InputHandoffFailures.Inc()
		lg.Error("Reload: invalid input state mode")
		return
	}

	var budgets []*BatchBudget
	if budget != nil {
		budgets = append(budgets, budget)
	}
	qb := NewBatchChan(queue, int(cfg.MainQueueBatchSize), int(cfg.BatchFlushPerdiodSec), ms, budgets...)
	defer qb.Close()

	var rlTickerChan <-chan time.Time
	if rateLimiters != nil && cfg.RateLimiterIntervalMs > 0 {
		intervalDuration := time.Duration(cfg.RateLimiterIntervalMs) * time.Millisecond
		rateLimiterUpdateTicker := time.NewTicker(intervalDuration)
		rlTickerChan = rateLimiterUpdateTicker.C
		defer rateLimiterUpdateTicker.Stop()
	}
	retryDuration := time.Duration(cfg.RateLimiterRetryDurationMs) * time.Millisecond
	recCount := 0

	var arena recArena
	var readErr error
	for {
		if c.moving() {
			c.detach(func(w io.Writer) error { return writeInputPrefix(w, c, reader, wire, nil, compressed) }, zstdReplayWork(compressed))
			return
		}
		record, err := reader.ReadSlice('\n')
		if c.moving() && err != nil {
			c.detach(func(w io.Writer) error { return writeInputPrefix(w, c, reader, wire, record, compressed) }, zstdReplayWork(compressed))
			return
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			readErr = errors.New("TCP input line exceeds 64 KiB")
			break
		}
		if len(record) > 0 {
			record = bytes.TrimSuffix(record, []byte{'\n'})
			record = bytes.TrimSuffix(record, []byte{'\r'})
			qb.Push(arena.copy(record))
		}
		if err != nil {
			readErr = err
			break
		}

		if rateLimiters != nil {
			recCount++
			select {
			case <-rlTickerChan:
				if !ratelimiter.RateLimitWithStop(rateLimiters, recCount, retryDuration, term) {
					return
				}
				recCount = 0
			default:
				if recCount > cfg.RateLimiterPerReaderRecordThreshold {
					if !ratelimiter.RateLimitWithStop(rateLimiters, recCount, retryDuration, term) {
						return
					}
					recCount = 0
				}
			}
		}
	}
	if err := readErr; err != nil && !errors.Is(err, io.EOF) {
		select {
		case <-term:
			return
		default:
		}
		// An idle client hitting IncomingConnIdleTimeoutSec is a normal close, not a read error.
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			lg.Info("TCP input connection idle, closing", zap.String("peer", c.RemoteAddr().String()))
			return
		}
		ms.ErrorRecs.Inc()
		lg.Error("TCP input scan failed", zap.Error(err))
	}

}

// Upgrade protocol detection without nesting buffered readers: otherwise a
// near-64KiB line can leave bytes hidden in the inner buffer during handoff.
func plaintextReader(r *bufio.Reader, source io.Reader) *bufio.Reader {
	buffered, _ := r.Peek(r.Buffered())
	return bufio.NewReaderSize(io.MultiReader(bytes.NewReader(bytes.Clone(buffered)), source), bufio.MaxScanTokenSize)
}

func sendToMainQ(rec []byte, q chan<- []byte, ms *metrics.Prom) {
	select {
	case q <- rec:
		ms.InRecs.Inc()
	default:
		ms.ThrottledRecs.Inc()
	}
}
