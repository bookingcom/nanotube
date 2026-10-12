package in

import (
	"bufio"
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
	rateLimiters []*ratelimiter.SlidingWindow, cfg *conf.Main, connWG *sync.WaitGroup, ms *metrics.Prom, lg *zap.Logger) {
	var wg sync.WaitGroup

loop:
	for {
		connCh := make(chan net.Conn)
		errCh := make(chan error)
		go func() {
			conn, err := l.Accept()

			if err != nil {
				errCh <- err
			} else {
				ms.ActiveTCPConnections.Inc()
				ms.InConnectionsTotalTCP.Inc()

				connCh <- conn
			}
		}()

		select {
		case <-term:
			// stop accepting new connections on termination signal
			err := l.Close()
			if err != nil {
				lg.Error("failed to close listening TCP connection", zap.Error(err))
			}
			break loop
		case err := <-errCh:
			lg.Error("accepting connection failed", zap.Error(err))
		case conn := <-connCh:
			wg.Add(1)
			go readFromConnectionTCPBuf(&wg, conn, queue, rateLimiters, cfg, ms, lg)
		}
	}

	if l != nil {
		lg.Debug("Stopped accepting new TCP connections. Starting to close incoming connections...", zap.String("address", l.Addr().String()))
	}
	wg.Wait()
	if l != nil {
		lg.Debug("Finished previously accepted TCP connections.", zap.String("address", l.Addr().String()))
	}

	connWG.Done()
}

func readFromConnectionTCPBuf(wg *sync.WaitGroup, conn net.Conn, queue chan<- [][]byte, rateLimiters []*ratelimiter.SlidingWindow, cfg *conf.Main, ms *metrics.Prom, lg *zap.Logger) {
	defer wg.Done() // executed after the connection is closed
	defer func() {
		err := conn.Close()
		if err != nil {
			lg.Error("closing the incoming connection", zap.Error(err))
		}
		ms.ActiveTCPConnections.Dec()
	}()

	// what if client connects and does nothing? protect!
	err := conn.SetReadDeadline(time.Now().Add(
		time.Duration(cfg.IncomingConnIdleTimeoutSec) * time.Second))
	if err != nil {
		lg.Error("error setting read deadline",
			zap.Error(err),
			zap.String("sender", conn.RemoteAddr().String()))
	}

	scanForRecordsTCPBuf(conn, queue, rateLimiters, cfg, ms, lg)
}

// deadlineReader arms the idle deadline before every read from the
// connection, so a sender that goes silent is dropped after the timeout.
type deadlineReader struct {
	conn    net.Conn
	timeout time.Duration
	lg      *zap.Logger
}

func (d *deadlineReader) Read(p []byte) (int, error) {
	err := d.conn.SetReadDeadline(time.Now().Add(d.timeout))
	if err != nil {
		d.lg.Error("error setting read deadline",
			zap.Error(err),
			zap.String("sender", d.conn.RemoteAddr().String()))
	}
	return d.conn.Read(p)
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

func scanForRecordsTCPBuf(conn net.Conn, queue chan<- [][]byte, rateLimiters []*ratelimiter.SlidingWindow, cfg *conf.Main, ms *metrics.Prom, lg *zap.Logger) {
	sc := bufio.NewScanner(&deadlineReader{
		conn:    conn,
		timeout: time.Duration(cfg.IncomingConnIdleTimeoutSec) * time.Second,
		lg:      lg,
	})

	// The initial size bounds how much is read per syscall; the max token
	// size stays bufio.MaxScanTokenSize.
	buf := make([]byte, bufio.MaxScanTokenSize)
	sc.Buffer(buf, bufio.MaxScanTokenSize)
	qb := NewBatchChan(queue, int(cfg.MainQueueBatchSize), int(cfg.BatchFlushPerdiodSec), ms)
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
	for sc.Scan() {
		qb.Push(arena.copy(sc.Bytes()))

		if rateLimiters != nil {
			recCount++
			select {
			case <-rlTickerChan:
				ratelimiter.RateLimit(rateLimiters, recCount, retryDuration)
				recCount = 0
			default:
				if recCount > cfg.RateLimiterPerReaderRecordThreshold {
					ratelimiter.RateLimit(rateLimiters, recCount, retryDuration)
					recCount = 0
				}
			}
		}
	}

	qb.Flush()

}

func sendToMainQ(rec []byte, q chan<- []byte, ms *metrics.Prom) {
	select {
	case q <- rec:
		ms.InRecs.Inc()
	default:
		ms.ThrottledRecs.Inc()
	}
}
