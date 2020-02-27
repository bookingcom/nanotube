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
	Cm   sync.RWMutex

	W    *bufio.Writer
	Wm   sync.Mutex
	stop chan int

	Lg                      *zap.Logger
	Ms                      *metrics.Prom
	SendTimeoutSec          uint32
	ConnTimeoutSec          uint32
	KeepAliveSec            uint32
	TCPOutBufFlushPeriodSec uint32
	MaxReconnectPeriodMs    uint32
	ReconnectPeriodDeltaMs  uint32

	outRecs            prometheus.Counter
	throttled          prometheus.Counter
	stateChanges       prometheus.Counter
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
		KeepAliveSec:            mainCfg.KeepAliveSec,
		outRecs:                 ms.OutRecs.With(promLabels),
		throttled:               ms.ThrottledHosts.With(promLabels),
		processingDuration:      ms.ProcessingDuration,
		stateChanges:            ms.StateChangeHosts.With(promLabels),
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
func (h *Host) Stream(wg *sync.WaitGroup, updateHostHealthStatus chan *HostStatus) {
	// TODO (grzkv) Maybe move (re)connection to a separate goroutine and communicate via a chan

	ticker := NewOptionalTicker(time.Duration(h.TCPOutBufFlushPeriodSec))
	defer ticker.Stop()

	defer func() {
		wg.Done()
		close(h.stop)
	}()

	go h.maintainHostConnection(updateHostHealthStatus)
	for {
		select {
		case r, ok := <-h.Ch:
			if !ok {
				return
			}
			// retry until successful
			for h.getHostConnection() != nil && h.updateHostConnWriteDeadline(updateHostHealthStatus) {
				// this may loose one record on disconnect
				_, err := h.Write([]byte(*r.Serialize()))

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
				h.closeUpdateHostConnection(updateHostHealthStatus)
			}
		case <-ticker.C:
			h.Flush(updateHostHealthStatus)
		}
	}
}

func (h *Host) closeUpdateHostConnection(updateHostHealthStatus chan *HostStatus) {
	err := h.closeConnection()
	if err != nil {
		// not retrying here, file descriptor may be lost
		h.Lg.Error("error closing the connection",
			zap.Error(err))
	}
	h.updateHostConnection(nil)
	h.updateHostHealthStatus(updateHostHealthStatus, false)
}

// Write does the remote write, i.e. sends the data
func (h *Host) Write(b []byte) (nn int, err error) {
	h.Wm.Lock()
	defer h.Wm.Unlock()
	return h.W.Write(b)
}

// Flush immediately flushes the buffer and performs a write
func (h *Host) Flush(updateHostHealthStatus chan *HostStatus) {
	h.Wm.Lock()
	defer h.Wm.Unlock()
	if h.W == nil || h.W.Buffered() == 0 {
		return
	}
	err := h.W.Flush()
	if err != nil {
		h.W = nil
		h.Lg.Error("error while flushing the host buffer", zap.Error(err), zap.String("host name", h.Name), zap.Uint16("host port", h.Port))
		// if flushing fails, the connection has to be re-established
		h.closeUpdateHostConnection(updateHostHealthStatus)
	}
}

// Connect connects to target host via TCP. If unsuccessful, sets conn to nil.
func (h *Host) Connect(updateHostHealthStatus chan *HostStatus, attemptCount int) {
	dialer := net.Dialer{
		Timeout:   time.Duration(h.ConnTimeoutSec) * time.Second,
		KeepAlive: time.Duration(h.KeepAliveSec) * time.Second,
	}
	conn, err := dialer.Dial("tcp", fmt.Sprintf("%s:%d", h.Name, h.Port))
	if err != nil {
		h.Lg.Warn("connection to host failed",
			zap.String("host", h.Name),
			zap.Uint16("port", h.Port))
		h.updateHostConnection(nil)
		if attemptCount == 1 {
			h.updateHostHealthStatus(updateHostHealthStatus, false)
		}

		return
	}

	h.updateHostConnection(conn)
	h.updateHostHealthStatus(updateHostHealthStatus, true)

	h.Wm.Lock()
	defer h.Wm.Unlock()
	h.W = bufio.NewWriterSize(conn, h.bufSize)
}

func (h *Host) updateHostHealthStatus(updateHostHealthStatus chan *HostStatus, status bool) {
	updateHostHealthStatus <- &HostStatus{Host: h, Status: status, sigCh: make(chan struct{})}
}

func (h *Host) updateHostConnection(conn net.Conn) {
	h.Cm.Lock()
	defer h.Cm.Unlock()
	h.Conn = conn
}

func (h *Host) getHostConnection() net.Conn {
	h.Cm.RLock()
	defer h.Cm.RUnlock()
	return h.Conn
}

func (h *Host) updateHostConnWriteDeadline(updateHostHealthStatus chan *HostStatus) bool {
	h.Cm.Lock()
	defer h.Cm.Unlock()
	if h.Conn == nil {
		return false
	}
	err := h.Conn.SetWriteDeadline(time.Now().Add(
		time.Duration(h.SendTimeoutSec) * time.Second))
	if err != nil {
		h.Lg.Warn("error setting write deadline. Renewing the connection.",
			zap.Error(err))
		h.closeUpdateHostConnection(updateHostHealthStatus)
		return false
	}
	return true
}

func (h *Host) closeConnection() error {
	h.Cm.Lock()
	defer h.Cm.Unlock()
	if h.Conn != nil {
		return h.Conn.Close()
	}
	return nil
}

func (h *Host) maintainHostConnection(updateHostHealthStatus chan *HostStatus) {
	for {
		for reconnectWait, attemptCount := uint32(0), 1; h.getHostConnection() == nil; {
			time.Sleep(time.Duration(reconnectWait) * time.Millisecond)
			if reconnectWait < h.MaxReconnectPeriodMs {
				reconnectWait = reconnectWait*2 + h.ReconnectPeriodDeltaMs
			}
			if reconnectWait >= h.MaxReconnectPeriodMs {
				reconnectWait = h.MaxReconnectPeriodMs
			}

			h.Connect(updateHostHealthStatus, attemptCount)
			attemptCount++
		}
	}
}
