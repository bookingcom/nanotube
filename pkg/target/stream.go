package target

import (
	"io"
	"math"
	"net"
	"time"

	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/metrics"
	"github.com/bookingcom/nanotube/pkg/rec"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"
)

// Points remain available until a complete frame write succeeds. Replaying wire
// bytes alone would refer to the previous connection's dictionary and deltas.
type streamPoint struct {
	name       []byte
	hash       uint64
	value      float64
	timestamp  int64
	plainBytes int
}

type streamFrameStats struct{ frames, names, resets, records int }

type streamCodec interface {
	preamble() []byte
	encode([]streamPoint) ([]byte, int, streamFrameStats, error)
	close()
}

type streamConnection struct {
	adopt        func() streamCodec
	lock, unlock func()
	ensure       func() net.Conn
	disconnect   func() error
	markDown     func()
	lastUse      *time.Time
	conn         net.Conn
	codec        streamCodec
	points       []streamPoint
	plainBytes   int
}

type streamSender struct {
	batches                              [][]*rec.RecBytes
	options                              conf.StreamOptions
	main                                 *conf.Main
	q                                    *recQueue
	connections                          []streamConnection
	ms                                   *metrics.StreamOutput
	out, outTotal, refresh, refreshTotal prometheus.Counter
	lg                                   *zap.Logger
	stop                                 chan int
}

// parseStreamPoint preserves strconv.ParseFloat's bits and conversions,
// including NaN timestamp rejection and truncation after float64 rounding.
func parseStreamPoint(r *rec.RecBytes) (streamPoint, bool) {
	v, err := parseStreamNumber(r.Val)
	if err != nil || math.IsNaN(v) {
		return streamPoint{}, false
	}
	t, err := parseStreamNumber(r.Time)
	if err != nil || math.IsNaN(t) {
		return streamPoint{}, false
	}
	return streamPoint{r.Path, r.PathHash(), v, int64(t), len(r.Path) + len(r.Val) + len(r.Time) + 3}, true
}

func (s *streamSender) run() {
	flushed := make(chan struct{})
	go func() {
		defer close(flushed)
		ticker := time.NewTicker(time.Duration(s.options.FlushPeriodMS) * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-s.stop:
				return
			case <-ticker.C:
				s.lockAll()
				for i := range s.connections {
					s.flush(&s.connections[i])
				}
				s.unlockAll()
			}
		}
	}()
	for chunk := s.q.pop(); chunk != nil; chunk = s.q.pop() {
		s.dispatch(chunk.recs[:chunk.n])
		s.q.recycle(chunk)
	}
	close(s.stop)
	<-flushed
	s.lockAll()
	defer s.unlockAll()
	for i := range s.connections {
		c := &s.connections[i]
		s.flush(c)
		if c.codec != nil {
			c.codec.close()
			c.codec = nil
		}
		if err := c.disconnect(); err != nil {
			s.lg.Warn("closing drained stream connection", zap.Error(err))
		}
	}
}

// Avalanche the cached routing hash with the SplitMix64 finalizer before taking
// a connection index. Using hash%connections directly can correlate with the
// jump hash that chooses the host. This mapping depends only on the name hash
// and connection count, so reconnects and host placement leave it unchanged.
func (s *streamSender) connectionIndex(hash uint64) int {
	hash = (hash ^ (hash >> 30)) * 0xbf58476d1ce4e5b9
	hash = (hash ^ (hash >> 27)) * 0x94d049bb133111eb
	hash ^= hash >> 31
	return int(hash % uint64(len(s.connections)))
}

// Group each queue chunk before locking. Points for each name retain FIFO order,
// and each populated connection is locked once per chunk, never per record.
func (s *streamSender) dispatch(rs []*rec.RecBytes) {
	if len(s.connections) == 1 {
		c := &s.connections[0]
		c.lock()
		s.sendBatch(c, rs)
		c.unlock()
		return
	}
	if s.batches == nil {
		s.batches = make([][]*rec.RecBytes, len(s.connections))
		for i := range s.batches {
			s.batches[i] = make([]*rec.RecBytes, 0, queueChunkLen)
		}
	}
	for _, r := range rs {
		i := s.connectionIndex(r.PathHash())
		s.batches[i] = append(s.batches[i], r)
	}
	for i, batch := range s.batches {
		if len(batch) == 0 {
			continue
		}
		c := &s.connections[i]
		c.lock()
		s.sendBatch(c, batch)
		c.unlock()
		clear(batch)
		s.batches[i] = batch[:0]
	}
}

func (s *streamSender) lockAll() {
	for i := range s.connections {
		s.connections[i].lock()
	}
}
func (s *streamSender) unlockAll() {
	for i := range s.connections {
		s.connections[i].unlock()
	}
}

func (s *streamSender) sendBatch(c *streamConnection, rs []*rec.RecBytes) {
	for i, r := range rs {
		p, ok := parseStreamPoint(r)
		if !ok {
			s.ms.Dropped.Inc()
			s.q.accountStreamDrop(len(r.Path) + len(r.Val) + len(r.Time) + 3)
			continue
		}
		s.ensure(c)
		if i == 0 && s.main.TCPOutConnectionRefreshPeriodSec > 0 && time.Since(*c.lastUse) > time.Duration(s.main.TCPOutConnectionRefreshPeriodSec)*time.Second {
			s.refresh.Inc()
			s.refreshTotal.Inc()
			old := c.conn
			s.flush(c)
			if c.conn == old {
				s.disconnect(c)
				s.ensure(c)
			}
		}
		// This bound also limits retained replay memory. The codec applies the
		// exact encoded payload bound when splitting these points into frames.
		if len(c.points) > 0 && c.plainBytes+p.plainBytes > s.options.MaxBytes {
			s.flush(c)
		}
		c.points = append(c.points, p)
		c.plainBytes += p.plainBytes
		if len(c.points) >= s.options.MaxRows || c.plainBytes >= s.options.MaxBytes {
			s.flush(c)
		}
	}
	*c.lastUse = time.Now()
	s.out.Add(float64(len(rs)))
	s.outTotal.Add(float64(len(rs)))
}

func startStreamSocket(conn net.Conn, options conf.StreamOptions, main *conf.Main, q *recQueue, lg *zap.Logger) (streamCodec, error) {
	codec, err := newStreamCodec(options)
	if err != nil {
		return nil, err
	}
	sender := streamSender{main: main, q: q, lg: lg}
	if result := sender.write(&streamConnection{conn: conn}, codec.preamble(), 0, 0); result.err != nil {
		codec.close()
		return nil, result.err
	}
	return codec, nil
}

func (s *streamSender) ensure(c *streamConnection) {
	for c.codec == nil {
		c.conn = c.ensure()
		if c.adopt != nil {
			c.codec = c.adopt()
			if c.codec != nil {
				return
			}
		}
		codec, err := startStreamSocket(c.conn, s.options, s.main, s.q, s.lg)
		if err == nil {
			c.codec = codec
			return
		}
		s.lg.Warn("writing stream preamble; reconnecting", zap.Error(err))
		s.disconnect(c)
	}
}

func (s *streamSender) disconnect(c *streamConnection) {
	if c.codec != nil {
		c.codec.close()
		c.codec = nil
	}
	if err := c.disconnect(); err != nil {
		s.lg.Warn("closing failed stream connection", zap.Error(err))
	}
	c.conn = nil
	c.markDown()
}

func (s *streamSender) flush(c *streamConnection) {
	for len(c.points) > 0 {
		s.ensure(c)
		wire, n, stats, err := c.codec.encode(c.points)
		if err != nil {
			s.lg.Error("stream point exceeds codec limits", zap.Error(err))
			s.ms.Dropped.Inc()
			s.q.accountStreamDrop(c.points[0].plainBytes)
			c.plainBytes -= c.points[0].plainBytes
			copy(c.points, c.points[1:])
			clear(c.points[len(c.points)-1:])
			c.points = c.points[:len(c.points)-1]
			s.disconnect(c)
			continue
		}
		s.ms.Frames.Add(float64(stats.frames))
		s.ms.Names.Add(float64(stats.names))
		s.ms.Resets.Add(float64(stats.resets))
		s.ms.Records.Add(float64(stats.records))
		plainBytes := 0
		for _, p := range c.points[:n] {
			plainBytes += p.plainBytes
		}
		result := s.write(c, wire, n, plainBytes)
		if result.err != nil {
			s.lg.Warn("writing stream frame; reconnecting and re-encoding pending points", zap.Error(result.err), zap.Int("pending_records", len(c.points)))
			s.disconnect(c)
			continue
		}
		copy(c.points, c.points[n:])
		clear(c.points[len(c.points)-n:])
		c.points = c.points[:len(c.points)-n]
		c.plainBytes -= plainBytes
		*c.lastUse = time.Now()
	}
}

func (s *streamSender) write(c *streamConnection, wire []byte, records, plainBytes int) tcpWriteResult {
	if err := c.conn.SetWriteDeadline(time.Now().Add(time.Duration(s.main.SendTimeoutSec) * time.Second)); err != nil {
		s.lg.Warn("setting stream write deadline", zap.Error(err))
	}
	n, err := c.conn.Write(wire)
	if n < 0 || n > len(wire) {
		n, err = 0, io.ErrShortWrite
	}
	if n < len(wire) && err == nil {
		err = io.ErrShortWrite
	}
	result := tcpWriteResult{written: n, err: err}
	if err == nil {
		result.records, result.bytes = records, plainBytes
	} else if n > 0 {
		result.uncertainRecords = records
	}
	s.q.accountTCPWrite(result)
	return result
}

func (h *Host) makeStreamSender(options conf.StreamOptions) *streamSender {
	s := streamSender{options: options, main: h.conf, q: h.q, ms: h.Ms.Stream.For(prometheus.Labels{"cluster": h.q.output.cluster, "upstream_host": h.Name}), out: h.outRecs, outTotal: h.outRecsTotal, refresh: h.oldConnectionRefresh, refreshTotal: h.oldConnectionRefreshTotal, lg: h.Lg, stop: h.stop}
	c := &h.Conn
	s.connections = []streamConnection{{lock: c.Lock, unlock: c.Unlock, ensure: func() net.Conn { h.ensureConnection(); return c.Conn }, disconnect: c.Close, adopt: func() streamCodec { codec := c.streamCodec; c.streamCodec = nil; return codec }, markDown: func() { h.setAvailability(false) }, lastUse: &c.LastConnUse}}
	return &s
}

func (h *HostMTCP) makeStreamSender(options conf.StreamOptions) *streamSender {
	s := streamSender{options: options, main: h.conf, q: h.q, ms: h.Ms.Stream.For(prometheus.Labels{"cluster": h.q.output.cluster, "upstream_host": h.Name}), out: h.outRecs, outTotal: h.outRecsTotal, refresh: h.oldConnectionRefresh, refreshTotal: h.oldConnectionRefreshTotal, lg: h.Lg, stop: h.stop}
	for i := range h.MTCPs {
		c := &h.MTCPs[i]
		s.connections = append(s.connections, streamConnection{lock: c.Lock, unlock: c.Unlock, ensure: func() net.Conn { h.ensureConnection(c); return c.Conn }, disconnect: c.Close, adopt: func() streamCodec { codec := c.streamCodec; c.streamCodec = nil; return codec }, markDown: func() { c.Available.Store(false); h.setHostAvailability() }, lastUse: &c.LastConnUse})
	}
	return &s
}

func (h *Host) streamRecords(options conf.StreamOptions)     { h.makeStreamSender(options).run() }
func (h *HostMTCP) streamRecords(options conf.StreamOptions) { h.makeStreamSender(options).run() }
