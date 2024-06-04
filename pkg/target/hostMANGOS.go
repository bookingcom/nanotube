package target

import (
	"fmt"
	"go.nanomsg.org/mangos/v3"
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

	// register transports
	_ "go.nanomsg.org/mangos/v3/transport/all"
)

// Host represents a single target hosts to send records to.
type HostMANGOS struct {
	Name      string
	Port      uint16
	Ch        chan *rec.RecBytes
	Available atomic.Bool
	Conn      ConnectionMANGOS

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

// Connection contains all the attributes of the target host connection.
type ConnectionMANGOS struct {
	mangos.Socket
	sync.Mutex
	LastConnUse time.Time
}

// New or updated target connection from existing net.Conn
// Requires Connection.Mutex lock
func (c *ConnectionMANGOS) New(socket mangos.Socket) {
	c.Socket = socket
	c.LastConnUse = time.Now()
}

// Close the connection while mainaining correct internal state.
// Overrides c.Conn.Close().
// Use instead the default close.
func (c *ConnectionMANGOS) Close() error {
	err := c.Close()
	c = nil

	return err
}

// NewHostMANGOS constructs a new MANGOS target host.
func NewHostMANGOS(clusterName string, mainCfg conf.Main, hostCfg conf.Host, lg *zap.Logger, ms *metrics.Prom) *HostMANGOS {
	return ConstructHostMANGOS(clusterName, mainCfg, hostCfg, lg, ms)
}

// String implements the Stringer interface.

func (h *HostMANGOS) String() string {
	return net.JoinHostPort(h.Name, strconv.Itoa(int(h.Port)))
}

// ConstructHostMANGOS builds new host object from config.
func ConstructHostMANGOS(clusterName string, mainCfg conf.Main, hostCfg conf.Host, lg *zap.Logger, ms *metrics.Prom) *HostMANGOS {
	targetPort := mainCfg.TargetPort
	if hostCfg.Port != 0 {
		targetPort = hostCfg.Port
	}

	promLabels := prometheus.Labels{
		"cluster":       clusterName,
		"upstream_host": hostCfg.Name,
	}
	h := HostMANGOS{
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
	h.rnd = rand.New(rand.NewSource(time.Now().UnixNano()))
	h.Ms = ms
	h.Lg = lg.With(zap.Stringer("target_host", &h))

	h.setAvailability(false)
	go func() {
		h.Conn.Lock()
		defer h.Conn.Unlock()
		h.ensureConnection()
	}()

	return &h
}

// Push adds a new record to send to the host queue.
func (h *HostMANGOS) Push(r *rec.RecBytes) {
	select {
	case h.Ch <- r:
	default:
		h.throttled.Inc()
		h.throttledTotal.Inc()
	}
}

// IsAvailable tells if the host is alive.
func (h *HostMANGOS) IsAvailable() bool {
	return h.Available.Load()
}

// Stop brings streaming to a halt.
func (h *HostMANGOS) Stop() {
	close(h.Ch)
}

// Stream launches the sending to target host.
// Exits when queue is closed and sending is finished.
func (h *HostMANGOS) Stream(wg *sync.WaitGroup) {
	defer func() {
		wg.Done()
		close(h.stop)
	}()

	for r := range h.Ch {
		h.tryToSend(r)
	}
}

func (h *HostMANGOS) tryToSend(r *rec.RecBytes) {
	h.Conn.Lock()
	defer h.Conn.Unlock()

	// retry until successful
	for {
		var err error
		if r != nil {
			s := r.Serialize()
			h.Lg.Info("Sending value to host", zap.ByteString("value", s))
			proto := h.Conn.Info()
			h.Lg.Info("Connection info", zap.String("PeerName", proto.PeerName))
			err = h.Conn.Send(s)
			if err == nil {
				h.outRecs.Inc()
				h.outRecsTotal.Inc()
				//h.processingDuration.Observe(time.Since(r.Received).Seconds())
				h.Conn.LastConnUse = time.Now() // TODO: This is not the last time conn was used. It is used when buffer is flushed.
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
}

// Tries to connect as long as Host.Conn  == nil.
// Requires h.Conn.Mutex lock.
// This function may take a long time.
func (h *HostMANGOS) ensureConnection() {
	for waitMs, attemptCount := uint32(0), 1; h.Conn.Socket == nil; {

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

// Connect connects to target host via MANGOS. If unsuccessful, sets conn to nil.
// Requires h.Conn.Mutex lock.
func (h *HostMANGOS) connect(attemptCount int) {
	socket, err := h.getConnectionToHost()
	if err != nil {
		h.Lg.Warn("connection to target host failed")
		h.Conn.Socket = nil
		if attemptCount == 1 {
			if h.Available.Load() {
				h.setAvailability(false)
				h.stateChanges.Inc()
				h.stateChangesTotal.Inc()
			}
		}

		return
	}

	h.Conn.New(socket)
	h.setAvailability(true)
}

func (h *HostMANGOS) getConnectionToHost() (mangos.Socket, error) {

	var err error
	var sock mangos.Socket

	url := fmt.Sprintf("tcp://%s:%d", h.Name, h.Port)
	if err = sock.Dial(url); err != nil {
		return nil, err
	}
	return sock, nil
}

func (h *HostMANGOS) setAvailability(val bool) {
	h.Available.Store(val)
	boolVal := 0.0
	if val {
		boolVal = 1.0
	}
	h.targetState.Set(boolVal)
}
