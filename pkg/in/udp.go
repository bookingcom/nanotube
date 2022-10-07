package in

import (
	"bytes"
	"errors"
	"github.com/bookingcom/nanotube/pkg/ratelimiter"
	"net"
	"sync"
	"time"

	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/metrics"
	"go.uber.org/zap"
)

// ListenUDP listens for incoming UDP connections.
func ListenUDP(conn net.PacketConn, queue chan []byte, stop <-chan struct{}, connWG *sync.WaitGroup, ms *metrics.Prom, lg *zap.Logger) {
	go func() {
		<-stop
		lg.Info("Termination: Closing the UDP connection.")
		closeErr := conn.Close()
		if closeErr != nil {
			lg.Error("closing the incoming UDP connection failed", zap.Error(closeErr))
		}
	}()

	buf := make([]byte, 64*1024) // 64k is the max UDP datagram size
loop:
	for {
		select {
		case <-stop:
			break loop
		default:
			nRead, _, err := conn.ReadFrom(buf)
			if err != nil {
				if errors.Is(err, net.ErrClosed) {
					break
				}

				lg.Error("error reading UDP datagram", zap.Error(err))
				continue
			}

			lines := bytes.Split(buf[:nRead], []byte{'\n'})

			for i := 0; i < len(lines)-1; i++ { // last line is empty
				rec := make([]byte, len(lines[i]))
				copy(rec, lines[i])
				sendToMainQ(rec, queue, ms)
			}
		}
	}

	lg.Info("Termination: Stopped accepting UDP data.")
	connWG.Done()
}

// ListenUDPBuf is a buffered version of ListenUDP.
func ListenUDPBuf(conn net.PacketConn, queue chan [][]byte, stop <-chan struct{}, rateLimiters []ratelimiter.RateLimiter,
	connWG *sync.WaitGroup, cfg *conf.Main, ms *metrics.Prom, lg *zap.Logger) {
	go func() {
		<-stop
		lg.Info("Termination: Closing the UDP connection.")
		closeErr := conn.Close()
		if closeErr != nil {
			lg.Error("closing the incoming UDP connection failed", zap.Error(closeErr))
		}
	}()

	buf := make([]byte, 64*1024) // 64k is the max UDP datagram size
	qb := NewBatchChan(queue, int(cfg.MainQueueBatchSize), int(cfg.BatchFlushPerdiodSec), ms)
	defer qb.Close()

	var rlTickerChan <-chan time.Time
	if rateLimiters != nil && cfg.RateLimiterIntervalMs > 0 {
		intervalDuration := time.Duration(cfg.RateLimiterIntervalMs) * time.Millisecond
		ch, stopCh := newRateLimiterTicker(intervalDuration)
		defer stopCh()
		rlTickerChan = ch
	}
	retryDuration := time.Duration(cfg.RateLimiterRetryDurationMs) * time.Millisecond

	recCount := 0
loop:
	for {
		select {
		case <-stop:
			break loop
		case <-rlTickerChan:
			if rateLimiters != nil {
				rateLimit(rateLimiters, recCount, retryDuration)
				recCount = 0
			}
		default:
			nRead, _, err := conn.ReadFrom(buf)
			if err != nil {
				if errors.Is(err, net.ErrClosed) {
					break
				}

				lg.Error("error reading UDP datagram", zap.Error(err))
				continue
			}
			lines := bytes.Split(buf[:nRead], []byte{'\n'})

			packetRecCount := 0
			for i := 0; i < len(lines)-1; i++ { // last line is empty
				rec := make([]byte, len(lines[i]))
				copy(rec, lines[i])
				qb.Push(rec)
				packetRecCount++
			}
			if rateLimiters != nil {
				recCount += packetRecCount
				if recCount > cfg.RateLimiterPerReaderRecordThreshold {
					rateLimit(rateLimiters, recCount, retryDuration)
					recCount = 0
				}
			}
		}
	}

	qb.Flush()

	lg.Info("Termination: Stopped accepting UDP data.")
	connWG.Done()
}
