package target

import (
	"fmt"
	"math/rand"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/metrics"
	"github.com/bookingcom/nanotube/pkg/rec"
	codec "github.com/bookingcom/nanotube/pkg/streamcodec"

	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/atomic"
	"go.uber.org/zap"
)

// HostMTCP represents a single target hosts to send records in multimple TCP connections to.
type HostMTCP struct {
	Name      string
	Port      uint16
	Available atomic.Bool
	NumMTCP   int
	MTCPs     []MultiConnection

	q  *recQueue
	id int

	stop chan int

	Lg *zap.Logger
	Ms *metrics.Prom

	conf          *conf.Main
	streamOptions *conf.StreamOptions
	streamNames   *codec.NameTable

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
	W           *recordBuffer
	streamCodec streamCodec
	attempted   bool
	Available   atomic.Bool
}

// New or updated target connection from existing net.Conn
// Requires Connection.Mutex lock
func (c *MultiConnection) New(n net.Conn, bufSize int) {
	c.Conn = n
	c.attempted = true
	c.LastConnUse = time.Now()
	if c.W == nil {
		c.W = newRecordBuffer(bufSize)
	}
}

// Close the connection while mainaining correct internal state.
// Overrides c.Conn.Close().
// Use instead the default close.
func (c *MultiConnection) Close() error {
	if c.streamCodec != nil {
		c.streamCodec.close()
		c.streamCodec = nil
	}

	if c.Conn == nil {
		return nil
	}
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
	mainCfg = tcpCompressionConfig(mainCfg, hostCfg)
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
		q:    newRecQueue(int(mainCfg.HostQueueSize)),
		id:   nextQueueID(),
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
	if hostCfg.Stream {
		options := hostCfg.StreamOptions.WithDefaults(mainCfg.TCPOutBufFlushPeriodSec)
		h.streamOptions = &options
		budget := mainCfg.StreamDictionaryBytes
		if budget == 0 {
			budget = 1 << 30
		}
		h.streamNames = ms.Stream.NameTable(budget/2, mainCfg.StreamFullNames)
	}
	h.q.trackTCP(ms.TCP.For(promLabels), clusterName, h.String())

	if mainCfg.TCPInitialConnCheck {
		h.setHostAvailability()
		go func() {
			h.LockMTCP()
			defer h.UnlockMTCP()
			for i := range h.MTCPs {
				h.MTCPs[i].Conn = nil
				h.ensureConnection(&h.MTCPs[i])
			}
		}()
	} else {
		h.Available.Store(true)
		h.targetState.Set(1.0)
	}

	return &h
}

// Push adds a new record to send to the host queue.
func (h *HostMTCP) Push(r *rec.RecBytes) {
	if h.streamOptions != nil {
		prepareStreamName(r, h.streamNames)
	}
	if !h.q.pushOne(r) {
		h.throttled.Inc()
		h.throttledTotal.Inc()
		//h.Lg.Warn("host queue is full", zap.Int("queue_size", h.q.n))
	}
}

func (h *HostMTCP) pushBulk(rs []*rec.RecBytes) {
	if h.streamOptions != nil {
		for _, r := range rs {
			prepareStreamName(r, h.streamNames)
		}
	}
	if dropped := h.q.push(rs); dropped > 0 {
		h.throttled.Add(float64(dropped))
		h.throttledTotal.Add(float64(dropped))
	}
}

func (h *HostMTCP) queueID() int {
	return h.id
}

// IsAvailable tells if the host is alive.
func (h *HostMTCP) IsAvailable() bool {
	return h.Available.Load()
}

// Stop brings streaming to a halt.
func (h *HostMTCP) Stop() {
	h.q.close()
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
	defer wg.Done()
	if h.streamOptions != nil {
		h.streamRecords(*h.streamOptions)
		return
	}
	flushDone := make(chan struct{})
	if h.conf.TCPOutBufFlushPeriodSec != 0 {
		go func() { defer close(flushDone); h.flush(time.Second * time.Duration(h.conf.TCPOutBufFlushPeriodSec)) }()
	} else {
		close(flushDone)
	}
	i := 0
	for c := h.q.pop(); c != nil; c = h.q.pop() {
		h.sendBatch(c.recs[:c.n], &h.MTCPs[i])
		h.q.recycle(c)
		i++
		if i == h.NumMTCP {
			i = 0
		}
	}
	close(h.stop)
	<-flushDone
	h.LockMTCP()
	defer h.UnlockMTCP()
	h.tryToFlushIfNecessary()
	for i := range h.MTCPs {
		if err := h.MTCPs[i].Close(); err != nil {
			h.Lg.Warn("closing drained target connection failed", zap.Error(err))
		}
	}
}

// sendBatch writes a batch of records to one connection under a single lock.
func (h *HostMTCP) sendBatch(batch []*rec.RecBytes, conn *MultiConnection) {
	conn.Lock()
	defer conn.Unlock()

	for i, r := range batch {
		h.tryToSend(r, conn, i == 0)
	}
	conn.LastConnUse = time.Now() // TODO: This is not the last time conn was used. It is used when buffer is flushed.

	h.outRecs.Add(float64(len(batch)))
	h.outRecsTotal.Add(float64(len(batch)))
}

// Requires conn lock.
// Connection freshness only needs checking before the first record of a
// batch: after a successful write the connection counts as just used.
func (h *HostMTCP) tryToSend(r *rec.RecBytes, conn *MultiConnection, checkFresh bool) {
	h.ensureConnection(conn)
	if checkFresh {
		h.keepConnectionFresh(conn)
	}
	b := r.Bytes()
	if conn.W.Buffered() > 0 && !conn.W.fits(len(b)) {
		h.flushOutput(conn)
	}
	conn.W.appendRecord(b)
	if conn.W.Buffered() >= conn.W.size {
		h.flushOutput(conn)
	}
}

// Requires conn lock and an established connection.
func (h *HostMTCP) setWriteDeadline(conn *MultiConnection) {
	err := conn.SetWriteDeadline(time.Now().Add(
		time.Duration(h.conf.SendTimeoutSec) * time.Second))
	if err != nil {
		h.Lg.Warn("error setting write deadline", zap.Error(err))
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
			h.LockMTCP()
			h.tryToFlushIfNecessary()
			h.UnlockMTCP()
		}
	}
}

// Requires h.Conn.Mutex lock.
func (h *HostMTCP) tryToFlushIfNecessary() {
	for i := range h.MTCPs {
		conn := &h.MTCPs[i]
		if conn.W != nil && conn.W.Buffered() > 0 {
			h.flushOutput(conn)
			conn.LastConnUse = time.Now()
		}
	}
}

func (h *HostMTCP) flushOutput(conn *MultiConnection) {
	for conn.W != nil && conn.W.Buffered() > 0 {
		h.ensureConnection(conn)
		h.setWriteDeadline(conn)
		result := conn.W.flush(conn.Conn)
		h.q.accountTCPWrite(result)
		if result.err == nil {
			return
		}
		h.Lg.Warn("error flushing target records; reconnecting and replaying buffer", zap.Error(result.err), zap.Int("pending_records", conn.W.records), zap.Int("pending_bytes", conn.W.Buffered()), zap.Int("uncertain_bytes", result.written))
		if err := conn.Close(); err != nil {
			h.Lg.Warn("closing failed target connection", zap.Error(err))
		}
		conn.Available.Store(false)
		h.setHostAvailability()
	}
}

// Requires h.Conn.Mutex lock.
// This function may take a long time.
func (h *HostMTCP) keepConnectionFresh(conn *MultiConnection) {
	// 0 value = don't refresh connections
	if h.conf.TCPOutConnectionRefreshPeriodSec != 0 {
		if conn != nil && (time.Since(conn.LastConnUse) > time.Second*time.Duration(h.conf.TCPOutConnectionRefreshPeriodSec)) {
			h.oldConnectionRefresh.Inc()
			h.oldConnectionRefreshTotal.Inc()

			old := conn.Conn
			h.flushOutput(conn)
			if conn.Conn != old {
				return // Recovery already established a fresh connection.
			}
			err := conn.Close()
			if err != nil {
				h.Lg.Error("closing connection to target host failed", zap.String("host", h.Name))
			}
			h.ensureConnection(conn)
		}
	}
}

// Tries to connect as long as Host.Conn.Conn == nil.
// Requires h.Conn.Mutex lock.
// This function may take a long time.
func (h *HostMTCP) ensureConnection(conn *MultiConnection) {
	for waitMs, attemptCount := uint32(0), 1; conn.Conn == nil; {
		if h.jitterEnabled && conn.attempted {
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

		h.connect(conn, attemptCount)
		attemptCount++
	}
}

// Connect multiple TCP connects to target host via TCP. If unsuccessful, sets conn to nil.
// Requires h.Conn.Mutex lock.
func (h *HostMTCP) connect(c *MultiConnection, attemptCount int) {
	c.attempted = true
	conn, err := h.getConnectionToHost()
	if err != nil {
		h.Lg.Warn("connection to target host failed")
		c.Conn = nil
		if attemptCount == 1 {
			if c.Available.Load() {
				c.Available.Store(false)
			}
		}
		return
	}
	c.New(conn, h.conf.TCPOutBufSize)
	c.W.configureCompression(*h.conf)
	if h.streamOptions != nil {
		c.streamCodec, err = startStreamSocket(conn, *h.streamOptions, h.conf, h.q, h.Lg, h.streamNames)
		if err != nil {
			h.Lg.Warn("stream preamble failed", zap.Error(err))
			_ = c.Close()
			c.Available.Store(false)
			h.setHostAvailability()
			return
		}
	}
	c.Available.Store(true)
	h.setHostAvailability()
}

func (h *HostMTCP) getConnectionToHost() (net.Conn, error) {
	dialer := net.Dialer{
		Timeout:   time.Duration(h.conf.OutConnTimeoutSec) * time.Second,
		KeepAlive: time.Duration(h.conf.KeepAliveSec) * time.Second,
	}
	n, err := dialer.Dial("tcp", net.JoinHostPort(h.Name, fmt.Sprint(h.Port)))
	return n, err
}

// set host availability based on the availability of its connections
// if one connection is down - all host is down
// if all connections are up - host is up
func (h *HostMTCP) setHostAvailability() {
	avail := 0
	for c := range h.MTCPs {
		if h.MTCPs[c].Available.Load() {
			avail++
		}
	}
	if avail == h.NumMTCP {
		h.Available.Store(true)
		h.targetState.Set(1.0)
	} else {
		h.Available.Store(false)
		h.targetState.Set(0.0)
	}
}
