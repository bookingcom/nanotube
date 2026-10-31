package target

import (
	"bytes"
	"fmt"
	"io"
	"math"
	"net"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/metrics"
	"github.com/bookingcom/nanotube/pkg/rec"
	codec "github.com/bookingcom/nanotube/pkg/streamcodec"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.uber.org/zap"
)

type receivedStreamPoint struct {
	name      string
	timestamp int64
	bits      uint64
}

func decodeStream(t testing.TB, wire []byte) ([]receivedStreamPoint, []codec.FrameInfo) {
	t.Helper()
	if len(wire) < codec.PreambleSize {
		t.Fatal("missing preamble")
	}
	flags, err := codec.ParsePreamble(wire[:codec.PreambleSize])
	if err != nil {
		t.Fatal(err)
	}
	d, err := codec.NewDecoder(codec.Options{Flags: flags})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	r := bytes.NewReader(wire[codec.PreambleSize:])
	var points []receivedStreamPoint
	var frames []codec.FrameInfo
	for r.Len() > 0 {
		info, err := d.ReadFrame(r, func(_ uint32, name []byte, value float64, ts int64, _ *any) {
			points = append(points, receivedStreamPoint{string(name), ts, math.Float64bits(value)})
		})
		if err != nil {
			t.Fatal(err)
		}
		frames = append(frames, info)
	}
	return points, frames
}

func streamRecordsForTest(n int, names int) []*rec.RecBytes {
	var rs []*rec.RecBytes
	for i := range n {
		rs = append(rs, &rec.RecBytes{Path: fmt.Appendf(nil, "example.metric.%d", i%names), Val: fmt.Appendf(nil, "%.3f", float64(i)/8), Time: fmt.Appendf(nil, "%d.875", 1700000000+i)})
	}
	return rs
}

func newStreamTestTarget(multi bool, port uint16, options conf.StreamOptions, old net.Conn) (Target, *streamSender, *metrics.Prom) {
	cfg := conf.MakeDefault()
	cfg.TCPInitialConnCheck = false
	cfg.TargetConnectionJitter = false
	cfg.TCPOutConnectionRefreshPeriodSec = 0
	cfg.HostQueueSize = 10000
	ms := metrics.New()
	hc := conf.Host{Name: "127.0.0.1", Port: port, Stream: true, StreamOptions: options}
	if multi {
		hc.MTCP = 2
		h := ConstructHostMTCP("example", cfg, hc, zap.NewNop(), ms)
		if old != nil {
			h.MTCPs[0].New(old, 4096)
		}
		sender := h.makeStreamSender(options.WithDefaults(cfg.TCPOutBufFlushPeriodSec))
		// These tests isolate one MTCP connection's framing/recovery. Dedicated
		// pinning tests below exercise dispatch across all connections.
		sender.connections = sender.connections[:1]
		return h, sender, ms
	}
	h := ConstructHost("example", cfg, hc, zap.NewNop(), ms)
	if old != nil {
		h.Conn.New(old, 4096)
	}
	return h, h.makeStreamSender(options.WithDefaults(cfg.TCPOutBufFlushPeriodSec)), ms
}

func checkStreamAccounting(t *testing.T, ms *metrics.Prom, records int, written int) {
	t.Helper()
	m := ms.TCP.For(map[string]string{"cluster": "example", "upstream_host": "127.0.0.1"})
	if testutil.ToFloat64(m.PendingRecords) != 0 || testutil.ToFloat64(m.PendingBytes) != 0 {
		t.Fatal("pending accounting did not drain")
	}
	if got := testutil.ToFloat64(m.FlushedRecords); got != float64(records) {
		t.Fatalf("flushed %v, want %d", got, records)
	}
	if got := testutil.ToFloat64(m.SocketWrittenBytes); got != float64(written) {
		t.Fatalf("wire bytes %v, want %d", got, written)
	}
}

func TestStreamTargetRoundTripAndResets(t *testing.T) {
	for _, multi := range []bool{false, true} {
		for _, zstd := range []bool{false, true} {
			for _, cap := range []int{1, 2} {
				t.Run(fmt.Sprintf("multi=%v/zstd=%v/cap=%d", multi, zstd, cap), func(t *testing.T) {
					old := &faultTCPConn{prefix: 1 << 24}
					target, s, ms := newStreamTestTarget(multi, 0, conf.StreamOptions{MaxRows: 5, MaxNames: cap, Zstd: zstd}, old)
					rs := streamRecordsForTest(25, 3)
					for _, r := range rs {
						target.Push(r)
					}
					target.Stop()
					// Run the same sender used by Target.Stream, keeping the supplied
					// successful fake connection to inspect exact wire bytes.
					s.run()
					got, frames := decodeStream(t, old.wire.Bytes())
					want := make([]receivedStreamPoint, 0, len(rs))
					for _, r := range rs {
						p, _ := parseStreamPoint(r)
						want = append(want, receivedStreamPoint{string(p.name), p.timestamp, math.Float64bits(p.value)})
					}
					if !reflect.DeepEqual(got, want) {
						t.Fatal("stream changed points or per-name ordering")
					}
					names, resets := 0, 0
					for _, f := range frames {
						names += int(f.Names)
						if f.Kind == codec.ResetFrame {
							resets++
						}
					}
					m := ms.Stream.For(map[string]string{"cluster": "example", "upstream_host": "127.0.0.1"})
					if testutil.ToFloat64(m.Frames) != float64(len(frames)) || testutil.ToFloat64(m.Names) != float64(names) || testutil.ToFloat64(m.Resets) != float64(resets) || testutil.ToFloat64(m.Records) != float64(len(rs)) || resets == 0 {
						t.Fatal("encoding counters differ from decoded frames")
					}
					checkStreamAccounting(t, ms, len(rs), old.wire.Len())
				})
			}
		}
	}
}

type failingStreamConn struct {
	faultTCPConn
	writes, failAt int
}

func (c *failingStreamConn) Write(b []byte) (int, error) {
	c.writes++
	if c.writes == c.failAt {
		return c.faultTCPConn.Write(b)
	}
	return c.wire.Write(b)
}

func TestStreamReconnectReencodesFreshDictionary(t *testing.T) {
	for _, multi := range []bool{false, true} {
		for _, zstd := range []bool{false, true} {
			for _, prefix := range []int{0, 7} {
				t.Run(fmt.Sprintf("multi=%v/zstd=%v/prefix=%d", multi, zstd, prefix), func(t *testing.T) {
					port, received := outputReceiver(t)
					old := &failingStreamConn{faultTCPConn: faultTCPConn{prefix: prefix, err: io.ErrClosedPipe}, failAt: 3}
					target, s, ms := newStreamTestTarget(multi, port, conf.StreamOptions{MaxRows: 2, Zstd: zstd}, old)
					rs := streamRecordsForTest(6, 1)
					for _, r := range rs {
						target.Push(r)
					}
					target.Stop()
					s.run()
					wire := receiveOutput(t, received)
					got, frames := decodeStream(t, wire)
					var want []receivedStreamPoint
					for _, r := range rs[2:] {
						p, _ := parseStreamPoint(r)
						want = append(want, receivedStreamPoint{string(p.name), p.timestamp, math.Float64bits(p.value)})
					}
					if !reflect.DeepEqual(got, want) {
						t.Fatal("reconnect changed or lost pending points")
					}
					if len(frames) == 0 || frames[0].Names != 1 {
						t.Fatal("fresh connection reused old dictionary")
					}
					if !old.closed {
						t.Fatal("failed connection not closed")
					}
					checkStreamAccounting(t, ms, len(rs), old.wire.Len()+len(wire))
					m := ms.Stream.For(map[string]string{"cluster": "example", "upstream_host": "127.0.0.1"})
					if testutil.ToFloat64(m.Records) != 8 || testutil.ToFloat64(m.Names) != 2 {
						t.Fatal("retry encoding counters incorrect")
					}
				})
			}
		}
	}
}

func TestStreamNumericDropsDrainAndPeriodicFlush(t *testing.T) {
	for _, multi := range []bool{false, true} {
		t.Run(fmt.Sprint(multi), func(t *testing.T) {
			old := &faultTCPConn{prefix: 1 << 24}
			target, sender, ms := newStreamTestTarget(multi, 0, conf.StreamOptions{FlushPeriodMS: 10}, old)
			rs := streamRecordsForTest(1, 1)
			rs = append(rs, &rec.RecBytes{Path: []byte("example.invalid"), Val: []byte("NaN"), Time: []byte("1")}, &rec.RecBytes{Path: []byte("example.invalid"), Val: []byte("1"), Time: []byte("NaN")}, &rec.RecBytes{Path: []byte("example.invalid"), Val: []byte("bad"), Time: []byte("1")})
			for _, r := range rs {
				target.Push(r)
			}
			var wg sync.WaitGroup
			wg.Add(1)
			go func() { defer wg.Done(); sender.run() }()
			defer func() { target.Stop(); wg.Wait() }()
			m := ms.TCP.For(map[string]string{"cluster": "example", "upstream_host": "127.0.0.1"})
			deadline := time.Now().Add(time.Second)
			for testutil.ToFloat64(m.FlushedRecords) != 1 {
				if time.Now().After(deadline) {
					t.Fatal("periodic flush did not send quiet connection")
				}
				time.Sleep(time.Millisecond)
			}
			target.Stop()
			wg.Wait()
			points, _ := decodeStream(t, old.wire.Bytes())
			if len(points) != 1 {
				t.Fatal("numeric rejects reached peer")
			}
			if testutil.ToFloat64(ms.Stream.For(map[string]string{"cluster": "example", "upstream_host": "127.0.0.1"}).Dropped) != 3 {
				t.Fatal("numeric rejects not counted")
			}
			checkStreamAccounting(t, ms, 1, old.wire.Len())
		})
	}
}

func TestStreamIdleRefreshFlushesBeforeFreshPreamble(t *testing.T) {
	for _, multi := range []bool{false, true} {
		t.Run(fmt.Sprint(multi), func(t *testing.T) {
			port, received := outputReceiver(t)
			old := &faultTCPConn{prefix: 1 << 24}
			target, s, ms := newStreamTestTarget(multi, port, conf.StreamOptions{}, old)
			s.main.TCPOutConnectionRefreshPeriodSec = 1
			rs := streamRecordsForTest(4, 1)
			stage := func(batch []*rec.RecBytes) {
				s.q.push(batch)
				chunk := s.q.pop()
				s.sendBatch(&s.connections[0], chunk.recs[:chunk.n])
				s.q.recycle(chunk)
			}
			stage(rs[:2])
			*s.connections[0].lastUse = time.Now().Add(-2 * time.Second)
			stage(rs[2:])
			target.Stop()
			s.run()
			first, _ := decodeStream(t, old.wire.Bytes())
			wire := receiveOutput(t, received)
			second, _ := decodeStream(t, wire)
			if len(first) != 2 || len(second) != 2 || !old.closed {
				t.Fatal("idle refresh discarded or replayed points")
			}
			checkStreamAccounting(t, ms, 4, old.wire.Len()+len(wire))
		})
	}
}

func TestStreamKnownNamesNoAllocations(t *testing.T) {
	old := &countingStreamConn{}
	_, s, _ := newStreamTestTarget(false, 0, conf.StreamOptions{MaxRows: 32}, old)
	rs := streamRecordsForTest(32, 8)
	cycle := func() {
		s.q.push(rs)
		chunk := s.q.pop()
		s.sendBatch(&s.connections[0], chunk.recs[:chunk.n])
		s.q.recycle(chunk)
		s.flush(&s.connections[0])
	}
	for range 10 {
		cycle()
	}
	if n := testing.AllocsPerRun(1000, cycle); n != 0 {
		t.Fatalf("known-name output allocated %v times per 32 points", n)
	}
	s.connections[0].codec.close()
}

func TestStreamPinsNamesAcrossConnectionsAndReconnect(t *testing.T) {
	cfg := conf.MakeDefault()
	cfg.TCPInitialConnCheck = false
	cfg.HostQueueSize = 10000
	h := ConstructHostMTCP("example", cfg, conf.Host{Name: "localhost", MTCP: 10, Stream: true}, zap.NewNop(), metrics.New())
	wires := make([]*faultTCPConn, 10)
	for i := range h.MTCPs {
		wires[i] = &faultTCPConn{prefix: 1 << 24}
		h.MTCPs[i].New(wires[i], 4096)
	}
	s := h.makeStreamSender((conf.StreamOptions{MaxRows: 32}).WithDefaults(2))
	rs := streamRecordsForTest(2048, 127)
	for i := 0; i < len(rs); i += queueChunkLen {
		s.dispatch(rs[i : i+queueChunkLen])
	}
	for i := range s.connections {
		s.flush(&s.connections[i])
	}
	before := make([]int, 10)
	for i, w := range wires {
		before[i] = w.wire.Len()
	}
	// Replace one socket under its existing partition: only that socket's
	// dictionary is discarded. Other streams retain their name ids and deltas.
	s.disconnect(&s.connections[3])
	replacement := &faultTCPConn{prefix: 1 << 24}
	h.MTCPs[3].New(replacement, 4096)
	for i := 0; i < len(rs); i += queueChunkLen {
		s.dispatch(rs[i : i+queueChunkLen])
	}
	for i := range s.connections {
		s.flush(&s.connections[i])
		defer s.connections[i].codec.close()
	}
	owners := map[string]int{}
	counts := map[string]int{}
	timelines := map[string][]int64{}
	for i, w := range wires {
		points, frames := decodeStream(t, w.wire.Bytes())
		for _, p := range points {
			if previous, ok := owners[p.name]; ok && previous != i {
				t.Fatal("name moved between live connections")
			}
			owners[p.name] = i
			counts[p.name]++
			timelines[p.name] = append(timelines[p.name], p.timestamp)
		}
		if i != 3 {
			// Decode the suffix using a fresh decoder: unchanged connections
			// must use their existing dictionaries, not redefine names.
			if len(frames) == 0 {
				t.Fatal("empty partition")
			}
			reader := bytes.NewReader(w.wire.Bytes()[before[i]:])
			d, _ := codec.NewDecoder(codec.Options{})
			_, err := d.ReadFrame(reader, nil)
			d.Close()
			if err == nil {
				t.Fatal("unaffected connection unexpectedly reset its dictionary")
			}
		}
	}
	points, frames := decodeStream(t, replacement.wire.Bytes())
	if len(frames) == 0 || frames[0].Names == 0 {
		t.Fatal("reconnected partition did not start a fresh dictionary")
	}
	for _, p := range points {
		if owners[p.name] != 3 {
			t.Fatal("reconnect affected another partition")
		}
		counts[p.name]++
		timelines[p.name] = append(timelines[p.name], p.timestamp)
	}
	for _, r := range rs {
		if owners[string(r.Path)] != s.connectionIndex(r.Path) {
			t.Fatal("incorrect path partition")
		}
	}
	expectedOrder := map[string][]int64{}
	for range 2 {
		for _, r := range rs {
			p, _ := parseStreamPoint(r)
			expectedOrder[string(r.Path)] = append(expectedOrder[string(r.Path)], p.timestamp)
		}
	}
	if !reflect.DeepEqual(timelines, expectedOrder) {
		t.Fatal("partitioning changed per-name point order")
	}
	for name, n := range counts {
		want := 0
		for _, r := range rs {
			if string(r.Path) == name {
				want += 2
			}
		}
		if n != want {
			t.Fatal("partition lost or duplicated points")
		}
	}
}

func TestStreamPreambleShortWriteReconnect(t *testing.T) {
	for _, multi := range []bool{false, true} {
		t.Run(fmt.Sprint(multi), func(t *testing.T) {
			port, received := outputReceiver(t)
			old := &failingStreamConn{faultTCPConn: faultTCPConn{prefix: 7}, failAt: 1}
			target, s, ms := newStreamTestTarget(multi, port, conf.StreamOptions{}, old)
			for _, r := range streamRecordsForTest(4, 1) {
				target.Push(r)
			}
			target.Stop()
			s.run()
			wire := receiveOutput(t, received)
			points, _ := decodeStream(t, wire)
			if len(points) != 4 || !old.closed {
				t.Fatal("preamble retry lost points or retained failed state")
			}
			checkStreamAccounting(t, ms, 4, len(wire)+old.wire.Len())
		})
	}
}

func TestStreamInitialCheckWritesPreambleWithoutPoints(t *testing.T) {
	for _, multi := range []bool{false, true} {
		t.Run(fmt.Sprint(multi), func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			n := 1
			if multi {
				n = 2
			}
			done := make(chan error, 1)
			go func() {
				for range n {
					conn, err := listener.Accept()
					if err != nil {
						done <- err
						return
					}
					_ = conn.SetReadDeadline(time.Now().Add(time.Second))
					marker := make([]byte, codec.PreambleSize)
					_, err = io.ReadFull(conn, marker)
					_ = conn.Close()
					if err == nil {
						_, err = codec.ParsePreamble(marker)
					}
					if err != nil {
						done <- err
						return
					}
				}
				done <- nil
			}()
			cfg := conf.MakeDefault()
			cfg.TCPInitialConnCheck = true
			h := NewHost("example", cfg, conf.Host{Name: "127.0.0.1", Port: uint16(listener.Addr().(*net.TCPAddr).Port), MTCP: n, Stream: true}, zap.NewNop(), metrics.New())
			defer func() {
				switch h := h.(type) {
				case *Host:
					h.Conn.Lock()
					defer h.Conn.Unlock()
					_ = h.Conn.Close()
				case *HostMTCP:
					h.LockMTCP()
					defer h.UnlockMTCP()
					for i := range h.MTCPs {
						_ = h.MTCPs[i].Close()
					}
				}
			}()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("initial connect did not write preamble")
			}
		})
	}
}

func TestStreamPinnedKnownNamesNoAllocations(t *testing.T) {
	for _, compressed := range []bool{false, true} {
		t.Run(fmt.Sprint(compressed), func(t *testing.T) {
			cfg := conf.MakeDefault()
			cfg.TCPInitialConnCheck = false
			cfg.HostQueueSize = 1024
			cfg.TCPOutConnectionRefreshPeriodSec = 0
			h := ConstructHostMTCP("example", cfg, conf.Host{Name: "localhost", MTCP: 10}, zap.NewNop(), metrics.New())
			for i := range h.MTCPs {
				h.MTCPs[i].New(&countingStreamConn{}, 65536)
			}
			s := h.makeStreamSender((conf.StreamOptions{Zstd: compressed, MaxRows: 64}).WithDefaults(2))
			rs := streamRecordsForTest(256, 32)
			cycle := func() {
				s.q.push(rs)
				chunk := s.q.pop()
				s.dispatch(chunk.recs[:chunk.n])
				s.q.recycle(chunk)
				for i := range s.connections {
					s.flush(&s.connections[i])
				}
			}
			for range 10 {
				cycle()
			}
			defer func() {
				for i := range s.connections {
					if s.connections[i].codec != nil {
						s.connections[i].codec.close()
					}
				}
			}()
			if n := testing.AllocsPerRun(1000, cycle); n != 0 {
				t.Fatalf("pinned known-name output allocated %v times per chunk", n)
			}
		})
	}
}
