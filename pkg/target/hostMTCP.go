package target

import (
	"bufio"
	"fmt"
	"math/rand"
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

// HostMTCP represents a single target hosts to send records in multimple TCP connections to.
type HostMTCP struct {
	Name      string
	Port      uint16
	Ch        chan *rec.RecBytes
	Available atomic.Bool
	NumMTCP   int
	MTCPs     []MultiConnection

	stop chan int

	Lg *zap.Logger
	Ms *metrics.Prom

	conf *conf.Main

	jitterEnabled        bool
	rnd                  *rand.Rand
	minJitterAmplitudeMs uint32

	outRecs                   prometheus.Counter
	outRecsTotal              prometheus.Counter
	throttled                 prometheus.Counter
	throttledTotal            prometheus.Counter
	stateChanges              prometheus.Counter
	stateChangesTotal         prometheus.Counter
	oldConnectionRefresh      prometheus.Counter
	oldConnectionRefreshTotal prometheus.Counter
	targetState               prometheus.Gauge
}

// MultiConnection contains all the attributes of the target host connection.
type MultiConnection struct {
	net.Conn
	sync.Mutex
	LastConnUse time.Time
	W           *bufio.Writer
}

// New or updated target connection from existing net.Conn
// Requires Connection.Mutex lock
func (c *MultiConnection) New(conn net.Conn, bufSize int) {
	c.Conn = conn
	c.LastConnUse = time.Now()
	c.W = bufio.NewWriterSize(conn, bufSize)
}

// Close the connection while mainaining correct internal state.
// Overrides c.Conn.Close().
// Use instead the default close.
func (c *MultiConnection) Close() error {
	err := c.Conn.Close()
	c.Conn = nil

	return err
}

// String implements the Stringer interface.
func (h *HostMTCP) String() string {
	return net.JoinHostPort(h.Name, strconv.Itoa(int(h.Port)))
}

// NewHostMTCP builds a target host of appropriate type
func NewHostMTCP(clusterName string, mainCfg conf.Main, hostCfg conf.Host, lg *zap.Logger, ms *metrics.Prom) Target {
	return ConstructHostMTCP(clusterName, mainCfg, hostCfg, lg, ms)
}

// ConstructHostMTCP builds new host object from config.
func ConstructHostMTCP(clusterName string, mainCfg conf.Main, hostCfg conf.Host, lg *zap.Logger, ms *metrics.Prom) *HostMTCP {
	targetPort := mainCfg.TargetPort
	if hostCfg.Port != 0 {
		targetPort = hostCfg.Port
	}

	promLabels := prometheus.Labels{
		"cluster":       clusterName,
		"upstream_host": hostCfg.Name,
	}
	h := HostMTCP{
		Name: hostCfg.Name,
		Port: targetPort,
		Ch:   make(chan *rec.RecBytes, mainCfg.HostQueueSize),
		stop: make(chan int),

		conf: &mainCfg,

		jitterEnabled:        mainCfg.TargetConnectionJitter,
		minJitterAmplitudeMs: mainCfg.TargetConnectionJitterMinAmplitudeMs,

		outRecs:                   ms.OutRecs.With(promLabels),
		outRecsTotal:              ms.OutRecsTotal,
		throttled:                 ms.ThrottledHosts.With(promLabels),
		throttledTotal:            ms.ThrottledHostsTotal,
		stateChanges:              ms.StateChangeHosts.With(promLabels),
		stateChangesTotal:         ms.StateChangeHostsTotal,
		oldConnectionRefresh:      ms.OldConnectionRefresh.With(promLabels),
		oldConnectionRefreshTotal: ms.OldConnectionRefreshTotal,
		targetState:               ms.TargetStates.With(promLabels),
	}
	h.NumMTCP = hostCfg.MTCP
	h.MTCPs = make([]MultiConnection, h.NumMTCP)

	h.rnd = rand.New(rand.NewSource(time.Now().UnixNano()))
	h.Ms = ms
	h.Lg = lg.With(zap.Stringer("target_host", &h))

	if mainCfg.TCPInitialConnCheck {
		h.setAvailability(false)
		go func() {
			h.LockMTCP()
			defer h.UnlockMTCP()
			h.ensureConnection()
		}()
	} else {
		h.setAvailability(true)
	}

	return &h
}

// Push adds a new record to send to the host queue.
func (h *HostMTCP) Push(r *rec.RecBytes) {
	select {
	case h.Ch <- r:
	default:
		h.throttled.Inc()
		h.throttledTotal.Inc()
		h.Lg.Warn("host queue is full", zap.Int("queue_size", len(h.Ch)))
	}
}

// IsAvailable tells if the host is alive.
func (h *HostMTCP) IsAvailable() bool {
	return h.Available.Load()
}

// Stop brings streaming to a halt.
func (h *HostMTCP) Stop() {
	close(h.Ch)
}

// LockMTCP locks all TCP connections
func (h *HostMTCP) LockMTCP() {
	for c := range h.MTCPs {
		h.MTCPs[c].Lock()
	}
}

// UnlockMTCP unlocks all TCP connections
func (h *HostMTCP) UnlockMTCP() {
	for c := range h.MTCPs {
		h.MTCPs[c].Unlock()
	}
}

// Stream launches the sending to target host.
// Exits when queue is closed and sending is finished.
func (h *HostMTCP) Stream(wg *sync.WaitGroup) {
	if h.conf.TCPOutBufFlushPeriodSec != 0 {
		go h.flush(time.Second * time.Duration(h.conf.TCPOutBufFlushPeriodSec))
	}
	defer func() {
		wg.Done()
		close(h.stop)
	}()

	for r := range h.Ch {
		i := 0
		h.tryToSend(r, &h.MTCPs[i])
		i++
	LOOP:
		// For control maximum size of batch
		for i < h.NumMTCP {
			select {
			case r := <-h.Ch:
				h.tryToSend(r, &h.MTCPs[i])
				i++
			default:
				break LOOP
			}
		}
	}

	// this line is only reached when the host channel was closed
	h.LockMTCP()
	defer h.UnlockMTCP()
	h.tryToFlushIfNecessary()
}

func (h *HostMTCP) tryToSend(r *rec.RecBytes, conn *MultiConnection) {
	conn.Lock()
	defer conn.Unlock()

	// retry until successful
	for {
		h.ensureConnection()
		h.keepConnectionFresh()

		err := conn.Conn.SetWriteDeadline(time.Now().Add(
			time.Duration(h.conf.SendTimeoutSec) * time.Second))
		if err != nil {
			h.Lg.Warn("error setting write deadline", zap.Error(err))
		}

		_, err = conn.W.Write(r.Serialize())

		if err == nil {
			h.outRecs.Inc()
			h.outRecsTotal.Inc()
			//h.processingDuration.Observe(time.Since(r.Received).Seconds())
			conn.LastConnUse = time.Now() // TODO: This is not the last time conn was used. It is used when buffer is flushed.
			break
		}

		h.Lg.Warn("error sending value to host. Reconnect and retry..", zap.Error(err))
		err = conn.Close()
		if err != nil {
			// not retrying here, file descriptor may be lost
			h.Lg.Error("error closing the connection", zap.Error(err))
		}
	}
}

// Flush periodically flushes the buffer and performs a write.
func (h *HostMTCP) flush(d time.Duration) {
	t := time.NewTicker(d)
	defer t.Stop()

	for {
		select {
		case <-h.stop:
			return
		case <-t.C:
			for c := range h.MTCPs {
				h.MTCPs[c].Lock()
				h.tryToFlushIfNecessary()
				h.MTCPs[c].Unlock()
			}
		}
	}
}

// Requires h.Conn.Mutex lock.
func (h *HostMTCP) tryToFlushIfNecessary() {
	for c := range h.MTCPs {
		if h.MTCPs[c].W != nil && h.MTCPs[c].W.Buffered() != 0 {
			if h.MTCPs[c].Conn == nil {
				h.ensureConnection()
			} else {
				h.keepConnectionFresh()
			}
			err := h.MTCPs[c].W.Flush()
			if err != nil {
				h.Lg.Error("error while flushing the host buffer", zap.Error(err), zap.String("target_name", h.Name), zap.Uint16("target_port", h.Port))
				h.MTCPs[c].Conn = nil
				h.MTCPs[c].W = nil
			}
			h.MTCPs[c].LastConnUse = time.Now()
		}
	}
}

// Requires h.Conn.Mutex lock.
// This function may take a long time.
func (h *HostMTCP) keepConnectionFresh() {
	// 0 value = don't refresh connections
	if h.conf.TCPOutConnectionRefreshPeriodSec != 0 {
		for c := range h.MTCPs {
			if h.MTCPs[c].Conn != nil && (time.Since(h.MTCPs[c].LastConnUse) > time.Second*time.Duration(h.conf.TCPOutConnectionRefreshPeriodSec)) {
				h.oldConnectionRefresh.Inc()
				h.oldConnectionRefreshTotal.Inc()

				err := h.MTCPs[c].Close()
				if err != nil {
					h.Lg.Error("closing connection to target host failed", zap.String("host", h.Name))
				}
				h.ensureConnection()
			}
		}
	}
}

// Tries to connect as long as Host.Conn.Conn == nil.
// Requires h.Conn.Mutex lock.
// This function may take a long time.
func (h *HostMTCP) ensureConnection() {
	for c := range h.MTCPs {
		for waitMs, attemptCount := uint32(0), 1; h.MTCPs[c].Conn == nil; {

			if h.jitterEnabled {
				jitterAmplitude := waitMs / 2
				if h.minJitterAmplitudeMs > jitterAmplitude {
					jitterAmplitude = h.minJitterAmplitudeMs
				}
				waitMs = waitMs/2 + uint32(h.rnd.Intn(int(jitterAmplitude*2)))
			}

			time.Sleep(time.Duration(waitMs) * time.Millisecond)

			if waitMs < h.conf.MaxHostReconnectPeriodMs {
				waitMs = waitMs*2 + h.conf.HostReconnectPeriodDeltaMs
			}
			if waitMs >= h.conf.MaxHostReconnectPeriodMs {
				waitMs = h.conf.MaxHostReconnectPeriodMs
			}

			h.connect(attemptCount)
			attemptCount++
		}
	}
}

// Connect multiple TCP connects to target host via TCP. If unsuccessful, sets conn to nil.
// Requires h.Conn.Mutex lock.
func (h *HostMTCP) connect(attemptCount int) {
	for c := range h.MTCPs {
		conn, err := h.getConnectionToHost()
		if err != nil {
			h.Lg.Warn("connection to target host failed")
			h.MTCPs[c].Conn = nil
			if attemptCount == 1 {
				if h.Available.Load() {
					h.setAvailability(false)
					h.stateChanges.Inc()
					h.stateChangesTotal.Inc()
				}
			}
			return
		}

		h.MTCPs[c].New(conn, h.conf.TCPOutBufSize)
		h.setAvailability(true)
	}
}

func (h *HostMTCP) getConnectionToHost() (net.Conn, error) {
	dialer := net.Dialer{
		Timeout:   time.Duration(h.conf.OutConnTimeoutSec) * time.Second,
		KeepAlive: time.Duration(h.conf.KeepAliveSec) * time.Second,
	}
	conn, err := dialer.Dial("tcp", net.JoinHostPort(h.Name, fmt.Sprint(h.Port)))
	return conn, err
}

func (h *HostMTCP) setAvailability(val bool) {
	h.Available.Store(val)
	boolVal := 0.0
	if val {
		boolVal = 1.0
	}
	h.targetState.Set(boolVal)
}
