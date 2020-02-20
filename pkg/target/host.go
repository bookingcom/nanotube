package target

import (
	"bufio"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/metrics"
	"github.com/bookingcom/nanotube/pkg/rec"

	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"
)

// Host represents a single target hosts to send records to.
type Host struct {
	Name string
	Port uint16
	// TODO (grzkv): Replace w/ circular buffer
	Ch   chan *rec.Rec
	Conn net.Conn

	W    *bufio.Writer
	stop chan int

	Lg                      *zap.Logger
	Ms                      *metrics.Prom
	SendTimeoutSec          uint32
	ConnTimeoutSec          uint32
	TCPOutBufFlushPeriodSec uint32
	MaxReconnectPeriodMs    uint32
	ReconnectPeriodDeltaMs  uint32

	outRecs            prometheus.Counter
	throttled          prometheus.Counter
	processingDuration prometheus.Histogram
	bufSize            int
}

//NewHost build new host object from config
func NewHost(clusterName string, mainCfg conf.Main, hostCfg conf.Host, lg *zap.Logger, ms *metrics.Prom) *Host {
	targetPort := mainCfg.TargetPort
	if hostCfg.Port != 0 {
		targetPort = hostCfg.Port
	}

	promLabels := prometheus.Labels{
		"cluster":       clusterName,
		"upstream_host": hostCfg.Name,
	}
	return &Host{
		Name: hostCfg.Name,
		Port: targetPort,
		Ch:   make(chan *rec.Rec, mainCfg.HostQueueSize),
		Lg:   lg,
		stop: make(chan int),

		SendTimeoutSec:          mainCfg.SendTimeoutSec,
		ConnTimeoutSec:          mainCfg.OutConnTimeoutSec,
		TCPOutBufFlushPeriodSec: mainCfg.TCPOutBufFlushPeriodSec,
		outRecs:                 ms.OutRecs.With(promLabels),
		throttled:               ms.ThrottledHosts.With(promLabels),
		processingDuration:      ms.ProcessingDuration,
		bufSize:                 mainCfg.TCPOutBufSize,
		MaxReconnectPeriodMs:    mainCfg.MaxHostReconnectPeriodMs,
		ReconnectPeriodDeltaMs:  mainCfg.MaxHostReconnectPeriodMs,
	}
}

// Push adds a new record to send to the host queue.
func (h *Host) Push(r *rec.Rec) {
	select {
	case h.Ch <- r:
	default:
		h.throttled.Inc()
	}
}

// OptionalTicker is a ticker that only starts if passed non-zero duration
type OptionalTicker struct {
	C <-chan time.Time
	t *time.Ticker
}

// NewOptionalTicker makes a new ticker that only runs if duration supplied is non-zero
func NewOptionalTicker(d time.Duration) *OptionalTicker {
	var ticker OptionalTicker
	if d != 0 {
		ticker.t = time.NewTicker(d)
		ticker.C = ticker.t.C
	}

	return &ticker
}

// Stop halts the ticker if it was ever started
func (t *OptionalTicker) Stop() {
	if t != nil {
		t.Stop()
	}
}

// Stream launches the the sending to target host.
// Exits when queue is closed and sending is finished.
func (h *Host) Stream(wg *sync.WaitGroup) {
	// TODO (grzkv) Maybe move (re)connection to a separate goroutine and communicate via a chan

	ticker := NewOptionalTicker(time.Duration(h.TCPOutBufFlushPeriodSec))
	defer ticker.Stop()

	defer func() {
		wg.Done()
		close(h.stop)
	}()

	for {
		select {
		case r, ok := <-h.Ch:
			if !ok {
				return
			}
			// retry until successful
			for {
				h.reconnectIfNecessary()

				err := h.Conn.SetWriteDeadline(time.Now().Add(
					time.Duration(h.SendTimeoutSec) * time.Second))
				if err != nil {
					h.Lg.Warn("error setting write deadline. Renewing the connection.",
						zap.Error(err))
				}

				// this may loose one record on disconnect
				_, err = h.Write([]byte(*r.Serialize()))

				if err == nil {
					h.outRecs.Inc()
					h.processingDuration.Observe(time.Since(r.Received).Seconds())
					break
				}

				h.Lg.Warn("error sending value to host. Reconnect and retry..",
					zap.String("target", h.Name),
					zap.Uint16("port", h.Port),
					zap.Error(err),
				)
				err = h.Conn.Close()
				if err != nil {
					// not retrying here, file descriptor may be lost
					h.Lg.Error("error closing the connection",
						zap.Error(err))
				}

				h.Conn = nil
			}
		case <-ticker.C:
			h.Flush(time.Second * time.Duration(h.TCPOutBufFlushPeriodSec))
		}
	}
}

// reconnect makes sure the host stays connected
func (h *Host) reconnectIfNecessary() {
	for reconnectWait := uint32(0); h.Conn == nil; {
		time.Sleep(time.Duration(reconnectWait) * time.Millisecond)
		if reconnectWait < h.MaxReconnectPeriodMs {
			reconnectWait = reconnectWait*2 + h.ReconnectPeriodDeltaMs
		}
		if reconnectWait >= h.MaxReconnectPeriodMs {
			reconnectWait = h.MaxReconnectPeriodMs
		}

		h.Connect()
	}
}

// Write does the remote write, i.e. sends the data
func (h *Host) Write(b []byte) (nn int, err error) {
	return h.W.Write(b)
}

// Flush immediately flushes the buffer and performs a write
func (h *Host) Flush(d time.Duration) {
	if h.W != nil {
		if h.W.Buffered() != 0 {
			err := h.W.Flush()
			if err != nil {
				h.Lg.Error("error while flushing the host buffer", zap.Error(err), zap.String("host name", h.Name), zap.Uint16("host port", h.Port))
				// if flushing fails, the connection has to be re-established
				h.Conn = nil
				h.W = nil
			}
		}
	}
}

// Connect connects to target host via TCP. If unsuccessful, sets conn to nil.
func (h *Host) Connect() {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", h.Name, h.Port),
		time.Duration(h.ConnTimeoutSec)*time.Second)

	if err != nil {
		h.Lg.Warn("connection to host failed",
			zap.String("host", h.Name),
			zap.Uint16("port", h.Port))
		h.Conn = nil
		h.W = nil

		return
	}

	h.Conn = conn

	h.W = bufio.NewWriterSize(conn, h.bufSize)
}
