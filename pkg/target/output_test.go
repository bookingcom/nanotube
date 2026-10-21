package target

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/metrics"
	"github.com/bookingcom/nanotube/pkg/rec"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type faultTCPConn struct {
	wire   bytes.Buffer
	prefix int
	err    error
	closed bool
}

func (c *faultTCPConn) Write(b []byte) (int, error) {
	n := min(c.prefix, len(b))
	_, _ = c.wire.Write(b[:n])
	return n, c.err
}
func (c *faultTCPConn) Read([]byte) (int, error)         { return 0, io.EOF }
func (c *faultTCPConn) Close() error                     { c.closed = true; return nil }
func (c *faultTCPConn) LocalAddr() net.Addr              { return &net.TCPAddr{} }
func (c *faultTCPConn) RemoteAddr() net.Addr             { return &net.TCPAddr{} }
func (c *faultTCPConn) SetDeadline(time.Time) error      { return nil }
func (c *faultTCPConn) SetReadDeadline(time.Time) error  { return nil }
func (c *faultTCPConn) SetWriteDeadline(time.Time) error { return nil }

type outputTestHost struct {
	Target
	q       *recQueue
	ms      *metrics.Prom
	stage   func([]*rec.RecBytes)
	refresh func()
}

func newOutputTestHost(t *testing.T, size int, multi bool, port uint16, old net.Conn) outputTestHost {
	t.Helper()
	cfg := conf.MakeDefault()
	cfg.TCPInitialConnCheck = false
	cfg.TCPOutBufSize = size
	cfg.TCPOutBufFlushPeriodSec = 0
	cfg.TCPOutConnectionRefreshPeriodSec = 0
	cfg.HostQueueSize = 10000
	ms := metrics.New()
	hc := conf.Host{Name: "127.0.0.1", Port: port, MTCP: 1}
	if multi {
		hc.MTCP = 2
		h := ConstructHostMTCP("test", cfg, hc, zap.NewNop(), ms)
		h.MTCPs[0].New(old, size)
		return outputTestHost{h, h.q, ms, func(rs []*rec.RecBytes) { h.sendBatch(rs, &h.MTCPs[0]) }, func() {
			h.conf.TCPOutConnectionRefreshPeriodSec = 1
			h.MTCPs[0].LastConnUse = time.Now().Add(-2 * time.Second)
			h.keepConnectionFresh(&h.MTCPs[0])
		}}
	}
	h := NewHostTCP("test", cfg, hc, zap.NewNop(), ms)
	h.Conn.New(old, size)
	return outputTestHost{h, h.q, ms, h.sendBatch, func() {
		h.conf.TCPOutConnectionRefreshPeriodSec = 1
		h.Conn.LastConnUse = time.Now().Add(-2 * time.Second)
		h.keepConnectionFresh()
	}}
}

func outputReceiver(t *testing.T) (uint16, <-chan []byte) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	done := make(chan []byte, 1)
	go func() {
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
		b, _ := io.ReadAll(c)
		done <- b
	}()
	return uint16(l.Addr().(*net.TCPAddr).Port), done
}

func receiveOutput(t *testing.T, done <-chan []byte) []byte {
	t.Helper()
	select {
	case b := <-done:
		return b
	case <-time.After(6 * time.Second):
		t.Fatal("target did not close its drained connection")
		return nil
	}
}

func runOutput(t *testing.T, h outputTestHost) {
	t.Helper()
	h.Stop()
	var wg sync.WaitGroup
	wg.Add(1)
	go h.Stream(&wg)
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("output recovery did not finish")
	}
}

func serializeOutput(rs []*rec.RecBytes) []byte {
	var b []byte
	for _, r := range rs {
		b = append(b, r.Bytes()...)
	}
	return b
}

func TestTCPFinalFlushRecoversCompleteRecords(t *testing.T) {
	for _, multi := range []bool{false, true} {
		for _, size := range []int{4096, 65536} {
			for _, fault := range []string{"zero-error", "partial-error", "short-without-error", "all-written-with-error"} {
				t.Run(fmt.Sprintf("multi-%v/buffer-%d/%s", multi, size, fault), func(t *testing.T) {
					port, received := outputReceiver(t)
					old := &faultTCPConn{err: io.ErrClosedPipe}
					rs := testRecs(100)
					want := serializeOutput(rs)
					switch fault {
					case "partial-error":
						old.prefix = 7
					case "short-without-error":
						old.prefix = 7
						old.err = nil
					case "all-written-with-error":
						old.prefix = len(want)
					}
					h := newOutputTestHost(t, size, multi, port, old)
					h.q.push(rs)
					runOutput(t, h)
					if got := receiveOutput(t, received); !bytes.Equal(got, want) {
						t.Fatalf("replay changed records: got %d bytes, want %d", len(got), len(want))
					}
					if !old.closed {
						t.Fatal("failed connection was not closed")
					}
					m := h.ms.TCP.For(map[string]string{"cluster": "test", "upstream_host": "127.0.0.1"})
					if testutil.ToFloat64(m.PendingRecords) != 0 || testutil.ToFloat64(m.PendingBytes) != 0 {
						t.Fatal("completed replay left pending deliveries")
					}
					if got := testutil.ToFloat64(m.FlushedRecords); got != float64(len(rs)) {
						t.Fatalf("flushed records %v, want %d", got, len(rs))
					}
					if got := testutil.ToFloat64(m.SocketWrittenBytes); got != float64(len(want)+old.wire.Len()) {
						t.Fatalf("socket byte accounting %v", got)
					}
					uncertain := bytes.Count(old.wire.Bytes(), []byte{'\n'})
					if old.wire.Len() > 0 && old.wire.Bytes()[old.wire.Len()-1] != '\n' {
						uncertain++
					}
					if got := testutil.ToFloat64(m.UncertainRecords); got != float64(uncertain) {
						t.Fatalf("uncertain records %v, want %d", got, uncertain)
					}
					if testutil.ToFloat64(m.UncertainBytes) != float64(old.wire.Len()) || testutil.ToFloat64(m.WriteErrors) != 1 {
						t.Fatal("failed-write accounting is incorrect")
					}
					if testutil.ToFloat64(h.ms.ThrottledHostsTotal) != 0 {
						t.Fatal("retry counted as queue rejection")
					}
				})
			}
		}
	}
}

func TestTCPWriteRetryIncludesEarlierRecords(t *testing.T) {
	for _, multi := range []bool{false, true} {
		t.Run(fmt.Sprint(multi), func(t *testing.T) {
			port, received := outputReceiver(t)
			old := &faultTCPConn{err: io.ErrClosedPipe}
			h := newOutputTestHost(t, 128, multi, port, old)
			rs := testRecs(3)
			r, err := rec.ParseRec([]byte(strings.Repeat("x", 256)+" 1 1"), false, false, time.Now, zap.NewNop())
			if err != nil {
				t.Fatal(err)
			}
			rs = append(rs, r)
			h.q.push(rs)
			runOutput(t, h)
			if !bytes.Equal(receiveOutput(t, received), serializeOutput(rs)) {
				t.Fatal("reconnect lost preceding buffered records or changed oversized record")
			}
		})
	}
}

func TestTCPRefreshFlushesDirtyBufferBeforeClosing(t *testing.T) {
	for _, multi := range []bool{false, true} {
		for _, size := range []int{4096, 65536} {
			t.Run(fmt.Sprintf("multi-%v/buffer-%d", multi, size), func(t *testing.T) {
				port, received := outputReceiver(t)
				old := &faultTCPConn{prefix: 65536}
				h := newOutputTestHost(t, size, multi, port, old)
				rs := testRecs(3)
				h.q.push(rs)
				chunk := h.q.pop()
				h.stage(chunk.recs[:chunk.n])
				h.q.recycle(chunk)
				h.refresh()
				runOutput(t, h)
				if !bytes.Equal(old.wire.Bytes(), serializeOutput(rs)) {
					t.Fatal("dirty buffer was discarded before idle refresh")
				}
				if len(receiveOutput(t, received)) != 0 {
					t.Fatal("clean refresh replayed already flushed records")
				}
				if !old.closed {
					t.Fatal("idle connection was not closed")
				}
			})
		}
	}
}

type blockingTCPConn struct {
	faultTCPConn
	started, release chan struct{}
}

func (c *blockingTCPConn) Write(b []byte) (int, error) {
	select {
	case c.started <- struct{}{}:
	default:
	}
	<-c.release
	return len(b), nil
}

func TestTCPDeadlineSnapshotIncludesQueuedAndInFlight(t *testing.T) {
	blocked := &blockingTCPConn{started: make(chan struct{}, 1), release: make(chan struct{})}
	h := newOutputTestHost(t, 1, false, 2003, blocked)
	h.q.limit = 2
	rs := testRecs(5)
	h.q.push(rs[:2])
	var wg sync.WaitGroup
	wg.Add(1)
	go h.Stream(&wg)
	var release sync.Once
	finish := func() { h.Stop(); release.Do(func() { close(blocked.release) }); wg.Wait() }
	t.Cleanup(finish)
	select {
	case <-blocked.started:
	case <-time.After(time.Second):
		t.Fatal("socket write did not start")
	}
	for _, r := range rs[2:] {
		h.Push(r) // two queued, one rejected; two more are in the detached chunk.
	}
	var logs bytes.Buffer
	core := zapcore.NewCore(zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()), zapcore.AddSync(&logs), zap.InfoLevel)
	cls := Clusters{"test": &Cluster{Hosts: []Target{h.Target}}}
	if cls.ReportTCPShutdown(zap.New(core), true) {
		t.Fatal("deadline snapshot ignored blocked output")
	}
	m := h.ms.TCP.For(map[string]string{"cluster": "test", "upstream_host": "127.0.0.1"})
	if got := testutil.ToFloat64(m.AbandonedRecords); got != 4 {
		t.Fatalf("abandoned snapshot %v, want four accepted deliveries", got)
	}
	if got := testutil.ToFloat64(m.AbandonedBytes); got != float64(len(serializeOutput(rs[:4]))) {
		t.Fatalf("pending byte snapshot %v", got)
	}
	if testutil.ToFloat64(h.ms.ThrottledHostsTotal) != 1 {
		t.Fatal("queue rejection was conflated with pending output")
	}
	if !strings.Contains(logs.String(), "Shutdown: TCP target summary") {
		t.Fatal("missing external terminal summary")
	}
	finish()
	if !cls.ReportTCPShutdown(zap.NewNop(), false) {
		t.Fatal("healthy drain retained pending accounting")
	}
}

// Periodic recovery must complete while the queue is still open, rather than
// relying on Stream's final flush to rescue it during shutdown.
func TestTCPPeriodicFlushRecoversBeforeShutdown(t *testing.T) {
	for _, multi := range []bool{false, true} {
		for _, size := range []int{4096, 65536} {
			t.Run(fmt.Sprintf("multi-%v/buffer-%d", multi, size), func(t *testing.T) {
				port, received := outputReceiver(t)
				old := &faultTCPConn{prefix: 7, err: io.ErrClosedPipe}
				h := newOutputTestHost(t, size, multi, port, old)
				rs := testRecs(100)
				h.q.push(rs)
				chunk := h.q.pop()
				h.stage(chunk.recs[:chunk.n])
				h.q.recycle(chunk)
				done := make(chan struct{})
				go func() {
					defer close(done)
					switch target := h.Target.(type) {
					case *HostTCP:
						target.flush(time.Millisecond)
					case *HostMTCP:
						target.flush(time.Millisecond)
					}
				}()
				var finish sync.Once
				stop := func() { finish.Do(func() { runOutput(t, h); <-done }) }
				defer stop()
				m := h.ms.TCP.For(map[string]string{"cluster": "test", "upstream_host": "127.0.0.1"})
				deadline := time.Now().Add(time.Second)
				for testutil.ToFloat64(m.FlushedRecords) != float64(len(rs)) {
					if time.Now().After(deadline) {
						t.Fatal("periodic flush did not recover while running")
					}
					time.Sleep(time.Millisecond)
				}
				stop()
				if !bytes.Equal(receiveOutput(t, received), serializeOutput(rs)) {
					t.Fatal("periodic recovery changed records")
				}
			})
		}
	}
}

func TestTCPRefreshRecoversDirtyBufferAndKeepsNewConnection(t *testing.T) {
	for _, multi := range []bool{false, true} {
		t.Run(fmt.Sprint(multi), func(t *testing.T) {
			port, received := outputReceiver(t)
			old := &faultTCPConn{prefix: 7, err: io.ErrClosedPipe}
			h := newOutputTestHost(t, 4096, multi, port, old)
			rs := testRecs(4)
			stage := func(batch []*rec.RecBytes) {
				h.q.push(batch)
				chunk := h.q.pop()
				h.stage(chunk.recs[:chunk.n])
				h.q.recycle(chunk)
			}
			stage(rs[:3])
			h.refresh()
			stage(rs[3:])
			runOutput(t, h)
			if !bytes.Equal(receiveOutput(t, received), serializeOutput(rs)) {
				t.Fatal("idle refresh lost pending records or replaced its recovered connection")
			}
		})
	}
}
