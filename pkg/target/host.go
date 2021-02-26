package target

import (
	"bufio"
	"fmt"
	"net"
	"strconv"
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
	Available atomic.Bool
	Conn      Connection

	stop chan int

	Lg *zap.Logger
	Ms *metrics.Prom

	conf *conf.Main

	outRecs                   prometheus.Counter
	outRecsTotal              prometheus.Counter
	throttled                 prometheus.Counter
	throttledTotal            prometheus.Counter
	stateChanges              prometheus.Counter
	stateChangesTotal         prometheus.Counter
	oldConnectionRefresh      prometheus.Counter
	oldConnectionRefreshTotal prometheus.Counter
	processingDuration        prometheus.Histogram
}

// Connection contains all the attributes of the target host connection.
type Connection struct {
	net.Conn
	sync.Mutex
	LastConnUse time.Time
	W           *bufio.Writer
}

// New or updated target connection from existing net.Conn
// Requires Connection.Mutex lock
func (c *Connection) New(conn net.Conn, bufSize int) {
	c.Conn = conn
	c.LastConnUse = time.Now()
	c.W = bufio.NewWriterSize(conn, bufSize)
}

// Close the connection while mainaining correct internal state.
// Overrides c.Conn.Close().
// Use instead the default close.
func (c *Connection) Close() error {
	err := c.Conn.Close()
	c.Conn = nil

	return err
}

// String implements the Stringer interface.
func (h *Host) String() string {
	return net.JoinHostPort(h.Name, strconv.Itoa(int(h.Port)))
}

// NewHost builds a target host of appropriate type doing the polymorphic construction.
func NewHost(clusterName string, mainCfg conf.Main, hostCfg conf.Host, lg *zap.Logger, ms *metrics.Prom) Target {
	if hostCfg.GRPC {
		return NewHostGRPC(clusterName, mainCfg, hostCfg, lg, ms)
	}
	return NewHostTCP(clusterName, mainCfg, hostCfg, lg, ms)
}

// ConstructHost builds new host object from config.
func ConstructHost(clusterName string, mainCfg conf.Main, hostCfg conf.Host, lg *zap.Logger, ms *metrics.Prom) *Host {
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
		stop: make(chan int),

		conf: &mainCfg,

		outRecs:                   ms.OutRecs.With(promLabels),
		outRecsTotal:              ms.OutRecsTotal,
		throttled:                 ms.ThrottledHosts.With(promLabels),
		throttledTotal:            ms.ThrottledHostsTotal,
		processingDuration:        ms.ProcessingDuration,
		stateChanges:              ms.StateChangeHosts.With(promLabels),
		stateChangesTotal:         ms.StateChangeHostsTotal,
		oldConnectionRefresh:      ms.OldConnectionRefresh.With(promLabels),
		oldConnectionRefreshTotal: ms.OldConnectionRefreshTotal,
	}
	h.Available.Store(true)
	h.Lg = lg.With(zap.Stringer("target_host", &h))

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

// IsAvailable tells if the host is alive.
func (h *Host) IsAvailable() bool {
	return h.Available.Load()
}

// Stop brings streaming to a halt.
func (h *Host) Stop() {
	close(h.Ch)
}

// Stream launches the the sending to target host.
// Exits when queue is closed and sending is finished.
func (h *Host) Stream(wg *sync.WaitGroup) {
	if h.conf.TCPOutBufFlushPeriodSec != 0 {
		go h.flush(time.Second * time.Duration(h.conf.TCPOutBufFlushPeriodSec))
	}
	defer func() {
		wg.Done()
		close(h.stop)
	}()

	for r := range h.Ch {
		h.tryToSend(r)
	}

	// this line is only reached when the host channel was closed
	h.Conn.Lock()
	defer h.Conn.Unlock()
	h.tryToFlushIfNecessary()
}

func (h *Host) tryToSend(r *rec.Rec) {
	h.Conn.Lock()
	defer h.Conn.Unlock()

	// retry until successful
	for {
		h.ensureConnection()
		h.keepConnectionFresh()

		err := h.Conn.SetWriteDeadline(time.Now().Add(
			time.Duration(h.conf.SendTimeoutSec) * time.Second))
		if err != nil {
			h.Lg.Warn("error setting write deadline", zap.Error(err))
		}

		// this may loose one record on disconnect
		_, err = h.Conn.W.Write([]byte(r.Serialize()))

		if err == nil {
			h.outRecs.Inc()
			h.outRecsTotal.Inc()
			h.processingDuration.Observe(time.Since(r.Received).Seconds())
			h.Conn.LastConnUse = time.Now()
			break
		}

		h.Lg.Warn("error sending value to host. Reconnect and retry..", zap.Error(err))
		err = h.Conn.Close()
		if err != nil {
			// not retrying here, file descriptor may be lost
			h.Lg.Error("error closing the connection", zap.Error(err))
		}
	}
}

// Flush periodically flushes the buffer and performs a write.
func (h *Host) flush(d time.Duration) {
	t := time.NewTicker(d)
	defer t.Stop()

	for {
		select {
		case <-h.stop:
			return
		case <-t.C:
			h.Conn.Lock()
			h.tryToFlushIfNecessary()
			h.Conn.Unlock()
		}
	}
}

// Requires h.Conn.Mutex lock.
func (h *Host) tryToFlushIfNecessary() {
	if h.Conn.W != nil && h.Conn.W.Buffered() != 0 {
		if h.Conn.Conn == nil {
			h.ensureConnection()
		} else {
			h.keepConnectionFresh()
		}
		err := h.Conn.W.Flush()
		if err != nil {
			h.Lg.Error("error while flushing the host buffer", zap.Error(err), zap.String("host name", h.Name), zap.Uint16("host port", h.Port))
			h.Conn.Conn = nil
			h.Conn.W = nil
		}
		h.Conn.LastConnUse = time.Now()
	}
}

// Requires h.Conn.Mutex lock.
// This function may take a long time.
func (h *Host) keepConnectionFresh() {
	// 0 value = don't refresh connections
	if h.conf.TCPOutConnectionRefreshPeriodSec != 0 {
		if h.Conn.Conn != nil && (time.Since(h.Conn.LastConnUse) > time.Second*time.Duration(h.conf.TCPOutConnectionRefreshPeriodSec)) {
			h.oldConnectionRefresh.Inc()
			h.oldConnectionRefreshTotal.Inc()

			err := h.Conn.Close()
			if err != nil {
				h.Lg.Error("closing connection to target host failed", zap.String("host", h.Name))
			}
			h.ensureConnection()
		}
	}
}

// Tries to connect as long as Host.Conn.Conn == nil.
// Requires h.Conn.Mutex lock.
// This function may take a long time.
func (h *Host) ensureConnection() {
	for reconnectWait, attemptCount := uint32(0), 1; h.Conn.Conn == nil; {
		time.Sleep(time.Duration(reconnectWait) * time.Millisecond)
		if reconnectWait < h.conf.MaxHostReconnectPeriodMs {
			reconnectWait = reconnectWait*2 + h.conf.HostReconnectPeriodDeltaMs
		}
		if reconnectWait >= h.conf.MaxHostReconnectPeriodMs {
			reconnectWait = h.conf.MaxHostReconnectPeriodMs
		}

		h.connect(attemptCount)
		attemptCount++
	}
}

// Connect connects to target host via TCP. If unsuccessful, sets conn to nil.
// Requires h.Conn.Mutex lock.
func (h *Host) connect(attemptCount int) {
	conn, err := h.getConnectionToHost()
	if err != nil {
		h.Lg.Warn("connection to host failed")
		h.Conn.Conn = nil
		if attemptCount == 1 {
			if h.Available.CAS(true, false) { // CAS = compare-and-save
				h.stateChanges.Inc()
				h.stateChangesTotal.Inc()
			}
		}

		return
	}

	h.Conn.New(conn, h.conf.TCPOutBufSize)
	h.Available.Store(true)
}

func (h *Host) getConnectionToHost() (net.Conn, error) {
	dialer := net.Dialer{
		Timeout:   time.Duration(h.conf.OutConnTimeoutSec) * time.Second,
		KeepAlive: time.Duration(h.conf.KeepAliveSec) * time.Second,
	}
	conn, err := dialer.Dial("tcp", net.JoinHostPort(h.Name, fmt.Sprint(h.Port)))
	return conn, err
}
