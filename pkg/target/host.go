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
	"go.uber.org/atomic"
	"go.uber.org/zap"
)

// Host represents a single target hosts to send records to.
type Host struct {
	Name string
	Port uint16
	// TODO (grzkv): Replace w/ circular buffer
	Ch        chan *rec.Rec
	Conn      net.Conn
	Available atomic.Bool

	W    *bufio.Writer
	CWm  sync.Mutex
	stop chan int

	Lg                        *zap.Logger
	Ms                        *metrics.Prom
	SendTimeoutSec            uint32
	ConnTimeoutSec            uint32
	KeepAliveSec              uint32
	MaxReconnectPeriodMs      uint32
	ReconnectPeriodDeltaMs    uint32
	ConnectionLossThresholdMs uint32
	TCPOutBufFlushPeriodSec   uint32

	outRecs            prometheus.Counter
	outRecsTotal       prometheus.Counter
	throttled          prometheus.Counter
	throttledTotal     prometheus.Counter
	stateChanges       prometheus.Counter
	stateChangesTotal  prometheus.Counter
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
	h := Host{
		Name: hostCfg.Name,
		Port: targetPort,
		Ch:   make(chan *rec.Rec, mainCfg.HostQueueSize),
		Lg:   lg,
		stop: make(chan int),

		SendTimeoutSec:            mainCfg.SendTimeoutSec,
		ConnTimeoutSec:            mainCfg.OutConnTimeoutSec,
		KeepAliveSec:              mainCfg.KeepAliveSec,
		MaxReconnectPeriodMs:      mainCfg.MaxHostReconnectPeriodMs,
		ReconnectPeriodDeltaMs:    mainCfg.MaxHostReconnectPeriodMs,
		ConnectionLossThresholdMs: mainCfg.ConnectionLossThresholdMs,
		TCPOutBufFlushPeriodSec:   mainCfg.TCPOutBufFlushPeriodSec,
		outRecs:                   ms.OutRecs.With(promLabels),
		outRecsTotal:              ms.OutRecsTotal,
		throttled:                 ms.ThrottledHosts.With(promLabels),
		throttledTotal:            ms.ThrottledHostsTotal,
		processingDuration:        ms.ProcessingDuration,
		stateChanges:              ms.StateChangeHosts.With(promLabels),
		stateChangesTotal:         ms.StateChangeHostsTotal,
		bufSize:                   mainCfg.TCPOutBufSize,
	}
	h.Available.Store(true)

	return &h
}

// Push adds a new record to send to the host queue.
func (h *Host) Push(r *rec.Rec) {
	select {
	case h.Ch <- r:
	default:
		h.throttled.Inc()
		h.throttledTotal.Inc()
	}
}

// Stream launches the the sending to target host.
// Exits when queue is closed and sending is finished.
func (h *Host) Stream(wg *sync.WaitGroup) {
	// TODO (grzkv) Maybe move (re)connection to a separate goroutine and communicate via a chan

	if h.TCPOutBufFlushPeriodSec != 0 {
		go h.Flush(time.Second * time.Duration(h.TCPOutBufFlushPeriodSec))
	}
	defer func() {
		wg.Done()
		close(h.stop)
	}()

	for r := range h.Ch {
		h.tryToSend(r)
	}

	// this line is only reached when the host channel was closed
	h.CWm.Lock()
	defer h.CWm.Unlock()
	h.tryToFlushIfNecessary()
}

func (h *Host) tryToSend(r *rec.Rec) {
	h.CWm.Lock()
	defer h.CWm.Unlock()

	// retry until successful
	for {
		for reconnectWait, attemptCount := uint32(0), 1; h.Conn == nil; {
			time.Sleep(time.Duration(reconnectWait) * time.Millisecond)
			if reconnectWait < h.MaxReconnectPeriodMs {
				reconnectWait = reconnectWait*2 + h.ReconnectPeriodDeltaMs
			}
			if reconnectWait >= h.MaxReconnectPeriodMs {
				reconnectWait = h.MaxReconnectPeriodMs
			}

			h.Connect(attemptCount)
			attemptCount++
		}

		err := h.Conn.SetWriteDeadline(time.Now().Add(
			time.Duration(h.SendTimeoutSec) * time.Second))
		if err != nil {
			h.Lg.Warn("error setting write deadline", zap.Error(err))
		}

		// this may loose one record on disconnect
		_, err = h.W.Write([]byte(r.Serialize()))

		if err == nil {
			h.outRecs.Inc()
			h.outRecsTotal.Inc()
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
			h.Lg.Error("error closing the connection", zap.Error(err))
		}

		h.Conn = nil
	}
}

// Flush periodically flushes the buffer and performs a write.
func (h *Host) Flush(d time.Duration) {
	t := time.NewTicker(d)
	defer t.Stop()

	for {
		select {
		case <-h.stop:
			return
		case <-t.C:
			h.CWm.Lock()
			h.tryToFlushIfNecessary()
			h.CWm.Unlock()
		}
	}
}

// Requires mCW mutex lock.
func (h *Host) tryToFlushIfNecessary() {
	if h.Conn != nil && h.W != nil && h.W.Buffered() != 0 {
		err := h.W.Flush()
		if err != nil {
			h.Lg.Error("error while flushing the host buffer", zap.Error(err), zap.String("host name", h.Name), zap.Uint16("host port", h.Port))
			h.Conn = nil
			h.W = nil
		}
	}
}

// Connect connects to target host via TCP. If unsuccessful, sets conn to nil.
func (h *Host) Connect(attemptCount int) {
	conn, err := h.getConnectionToHost()
	if err != nil {
		h.Lg.Warn("connection to host failed",
			zap.String("host", h.Name),
			zap.Uint16("port", h.Port))
		h.Conn = nil
		if attemptCount == 1 {
			if h.Available.Load() {
				h.stateChanges.Inc()
				h.stateChangesTotal.Inc()
			}
			h.Available.Store(false)
		}

		return
	}

	h.Conn = conn
	h.Available.Store(true)

	h.W = bufio.NewWriterSize(conn, h.bufSize)
}

func (h *Host) getConnectionToHost() (net.Conn, error) {
	dialer := net.Dialer{
		Timeout:   time.Duration(h.ConnTimeoutSec) * time.Second,
		KeepAlive: time.Duration(h.KeepAliveSec) * time.Second,
	}
	conn, err := dialer.Dial("tcp", net.JoinHostPort(h.Name, fmt.Sprint(h.Port)))
	return conn, err
}

func (h *Host) checkUpdateHostStatus() {
	conn, _ := h.getConnectionToHost()
	if conn != nil {
		h.Available.Store(true)
		_ = conn.Close()
	} else {
		if h.Available.Load() {
			h.stateChanges.Inc()
			h.stateChangesTotal.Inc()
		}
		h.Available.Store(false)
	}
}
