//go:build unix

package in

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/metrics"
	codec "github.com/bookingcom/nanotube/pkg/streamcodec"
	"github.com/klauspost/compress/zstd"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.uber.org/zap"
)

type accountingSink struct {
	mu   *sync.Mutex
	rows *[]string
}

func (s *accountingSink) Point(_ uint32, n []byte, v float64, ts int64, _ *any) {
	s.mu.Lock()
	*s.rows = append(*s.rows, fmt.Sprintf("%s %.0f %d", n, v, ts))
	s.mu.Unlock()
}
func (s *accountingSink) Reset() {}
func (s *accountingSink) Flush() {}

type boundHandoffSink struct {
	boundAccountingSink
	collector StreamSink
}

func (s *boundHandoffSink) BoundPoint(n *codec.BoundName, _ bool, v float64, ts int64) {
	s.collector.Point(0, n.Bytes(), v, ts, nil)
}

func handoffPair(t *testing.T) (*net.UnixConn, *net.UnixConn) {
	t.Helper()
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	files := []*os.File{os.NewFile(uintptr(fds[0]), "a"), os.NewFile(uintptr(fds[1]), "b")}
	left, err := net.FileConn(files[0])
	_ = files[0].Close()
	if err != nil {
		t.Fatal(err)
	}
	right, err := net.FileConn(files[1])
	_ = files[1].Close()
	if err != nil {
		t.Fatal(err)
	}
	a, b := left.(*net.UnixConn), right.(*net.UnixConn)
	t.Cleanup(func() { a.Close(); b.Close() })
	return a, b
}

func TestInputHandoffDoesNotWaitForFinalFlush(t *testing.T) {
	for _, pressure := range []string{"budget", "queue"} {
		for _, stateBytes := range []uint64{0, 123} {
			t.Run(fmt.Sprintf("%s/state%d", pressure, stateBytes), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				cfg := conf.MakeDefault()
				cfg.IncomingConnIdleTimeoutSec = 30
				q := make(chan [][]byte, 1)
				limit := uint64(1 << 20)
				if pressure == "budget" {
					limit = 1
				}
				budget := NewBatchBudget(limit)
				held := [][]byte{[]byte("held")}
				budget.acquire(held)
				q <- held
				old := NewTCPInput(q, ctx.Done(), nil, &cfg, metrics.New(), zap.NewNop(), budget)
				next := NewTCPInput(make(chan [][]byte, 8), ctx.Done(), nil, &cfg, metrics.New(), zap.NewNop(), nil)
				ready := make(chan *inputConn)
				flushStarted := make(chan struct{})
				var index atomic.Int32
				// Exercise the actual deferred BatchChan.Close path, with the
				// first reader's parsed record still in its final partial batch.
				// A synthetic state charge also checks delayed reclamation;
				// real plaintext/zstd readers use the zero-charge case.
				old.run = func(c *inputConn) {
					defer old.wg.Done()
					defer close(c.done)
					defer c.Conn.Close()
					defer old.ms.ActiveTCPConnections.Dec()
					first := index.Add(1) == 1
					qb := NewBatchChan(q, 8, 0, old.ms, budget)
					defer func() {
						if first {
							close(flushStarted)
						}
						qb.Close()
					}()
					if first {
						c.releaseBytes = stateBytes
						qb.Push([]byte("first 1 100"))
					}
					ready <- c
					var b [1]byte
					_, _ = c.Read(b[:])
					if c.moving() {
						c.detach(func(w io.Writer) error { _, err := w.Write([]byte{0}); return err })
					}
				}
				l, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				defer l.Close()
				a, b := handoffPair(t)
				var clients []net.Conn
				t.Cleanup(func() {
					cancel()
					_ = a.Close()
					_ = b.Close()
					for _, c := range clients {
						_ = c.Close()
					}
					budget.Release(held)
					go func() { old.wg.Wait(); close(q) }()
					for batch := range q {
						budget.Release(batch)
					}
					next.wg.Wait()
				})
				var conns []*inputConn
				for range 3 {
					client, err := net.Dial("tcp", l.Addr().String())
					if err != nil {
						t.Fatal(err)
					}
					clients = append(clients, client)
					server, err := l.Accept()
					if err != nil {
						t.Fatal(err)
					}
					old.add(server, nil)
					conns = append(conns, <-ready)
				}
				received := make(chan error, 1)
				go func() { received <- next.Receive(ctx, b) }()
				var capability [8]byte
				if _, err := io.ReadFull(a, capability[:]); err != nil {
					t.Fatal(err)
				}
				collections := 0
				memory := startHandoffMemory(1000, func() { collections++ })
				sent := make(chan error, 1)
				// Deterministic connection order isolates the backpressure case.
				go func() { sent <- old.sendConnections(ctx, a, conns, memory) }()
				for _, result := range []<-chan error{sent, received} {
					select {
					case err := <-result:
						if err != nil {
							t.Fatal(err)
						}
					case <-ctx.Done():
						t.Fatal("final Flush blocked the following transfers")
					}
				}
				<-flushStarted
				select {
				case <-conns[0].done:
					t.Fatal("first reader was not blocked flushing its final batch")
				default:
				}
				for _, c := range conns[1:] {
					<-c.done
				}
				if stateBytes == 0 {
					select {
					case <-memory.done:
					case <-ctx.Done():
						t.Fatal("zero-state reader delayed the collector")
					}
				}
				budget.Release(held)
				<-q // Make room for the blocked final batch.
				batch := <-q
				if len(batch) != 1 || string(batch[0]) != "first 1 100" {
					t.Fatalf("lost final parsed record: %q", batch)
				}
				budget.Release(batch)
				<-conns[0].done
				<-memory.done
				want := 0
				if stateBytes != 0 {
					want = 1
				}
				if collections != want || memory.released != stateBytes || budget.Bytes() != 0 {
					t.Fatalf("delayed release: collections=%d, state=%d, budget=%g", collections, memory.released, budget.Bytes())
				}
			})
		}
	}
}

func TestInputHandoffEveryByte(t *testing.T) {
	plain := []byte("one 1 100\r\ntwo 2 101\nthree 3 102\n")
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1))
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()
	compressed := enc.EncodeAll(plain, nil)
	var streaming bytes.Buffer
	zs, _ := zstd.NewWriter(&streaming, zstd.WithEncoderConcurrency(1))
	for _, p := range bytes.SplitAfter(plain, []byte{'\n'}) {
		_, _ = zs.Write(p)
		_ = zs.Flush()
	}
	_ = zs.Close()
	cases := map[string][]byte{"plaintext": plain, "zstd": compressed, "zstd-flush": streaming.Bytes()}
	for _, flags := range []byte{0, codec.FlagZstd} {
		e, _ := codec.NewEncoder(codec.Options{Flags: flags})
		wire, _ := codec.AppendPreamble(nil, flags)
		for i, name := range []string{"one", "two", "three"} {
			_ = e.Add([]byte(name), float64(i+1), int64(100+i))
			wire, _ = e.Flush(wire)
		}
		e.Close()
		cases[fmt.Sprint("stream-", flags)] = wire
		cases[fmt.Sprint("bound-stream-", flags)] = wire
	}
	for name, wire := range cases {
		t.Run(name, func(t *testing.T) {
			// Includes empty input, partial autodetection/preamble/header/payload,
			// dictionary predictors, a partial CRLF and complete-frame boundaries.
			for cut := 0; cut <= len(wire); cut++ {
				t.Run(fmt.Sprint(cut), func(t *testing.T) {
					cfg := conf.MakeDefault()
					cfg.MainQueueBatchSize = 0
					cfg.BatchFlushPerdiodSec = 0
					cfg.IncomingConnIdleTimeoutSec = 10
					q := make(chan [][]byte, 100)
					var mu sync.Mutex
					var rows []string
					factory := func() StreamSink {
						collector := &accountingSink{&mu, &rows}
						if strings.HasPrefix(name, "bound-") {
							return &boundHandoffSink{collector: collector}
						}
						return collector
					}
					stop := make(chan struct{})
					ms := metrics.New()
					old := NewTCPInput(q, stop, nil, &cfg, ms, zap.NewNop(), nil, factory)
					l, err := net.Listen("tcp", "127.0.0.1:0")
					if err != nil {
						t.Fatal(err)
					}
					serveDone := make(chan struct{})
					go func() { old.Serve(l, stop); close(serveDone) }()
					client, err := net.Dial("tcp", l.Addr().String())
					if err != nil {
						t.Fatal(err)
					}
					defer client.Close()
					if _, err = client.Write(wire[:cut]); err != nil {
						t.Fatal(err)
					}
					deadline := time.Now().Add(time.Second)
					for {
						old.mu.Lock()
						n := len(old.conns)
						consumed := int64(0)
						for c := range old.conns {
							consumed = c.readTotal.Load()
						}
						old.mu.Unlock()
						if n == 1 && consumed >= int64(cut) {
							break
						}
						if time.Now().After(deadline) {
							t.Fatal("not accepted")
						}
						time.Sleep(100 * time.Microsecond)
					}
					nextMS := metrics.New()
					next := NewTCPInput(q, make(chan struct{}), nil, &cfg, nextMS, zap.NewNop(), nil, factory)
					a, b := handoffPair(t)
					received := make(chan error, 1)
					ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
					defer cancel()
					go func() { received <- next.Receive(ctx, b) }()
					if err = old.Send(ctx, a); err != nil {
						t.Fatal(err)
					}
					if err = <-received; err != nil {
						t.Fatal(err)
					}
					close(stop)
					<-serveDone
					if _, err = client.Write(wire[cut:]); err != nil {
						t.Fatal(err)
					}
					client.Close()
					next.wg.Wait()
					close(q)
					for batch := range q {
						for _, rec := range batch {
							rows = append(rows, string(rec))
						}
					}
					if len(rows) != 3 {
						t.Fatalf("cut %d: rows=%q errors=%g/%g", cut, rows, testutil.ToFloat64(ms.ErrorRecs), testutil.ToFloat64(nextMS.ErrorRecs))
					}
					want := map[string]int{"one 1 100": 1, "two 2 101": 1, "three 3 102": 1}
					for _, r := range rows {
						want[r]--
					}
					for r, n := range want {
						if n != 0 {
							t.Fatalf("cut %d changed %q: %v", cut, r, rows)
						}
					}
					for _, m := range []*metrics.Prom{ms, nextMS} {
						if testutil.ToFloat64(m.InputHandoffFailures) != 0 || testutil.ToFloat64(m.ErrorRecs) != 0 || testutil.ToFloat64(m.StreamInputReservedBytes) != 0 {
							t.Fatal("handoff leaked/rejected input")
						}
					}
				})
			}
		})
	}
}

func TestZstdStreamingBeforeFrameEnd(t *testing.T) {
	var wire bytes.Buffer
	e, _ := zstd.NewWriter(&wire, zstd.WithEncoderConcurrency(1))
	defer e.Close()
	_, _ = e.Write([]byte("one 1 100\n"))
	_ = e.Flush() // deliberately no Close/end marker
	r, w := io.Pipe()
	defer r.Close()
	z, err := newZstdInput(bufio.NewReader(r))
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()
	go func() { _, _ = w.Write(wire.Bytes()) }()
	done := make(chan string, 1)
	go func() {
		b := make([]byte, 10)
		_, err := io.ReadFull(z, b)
		if err != nil {
			done <- err.Error()
		} else {
			done <- string(b)
		}
	}()
	select {
	case got := <-done:
		if got != "one 1 100\n" {
			t.Fatal(got)
		}
	case <-time.After(time.Second):
		t.Fatal("zstd waits for frame completion")
	}
	_ = w.Close()
}

func TestReadAheadSpillsWithoutStallingWriter(t *testing.T) {
	server, client := net.Pipe()
	a := newReadAhead(server)
	defer a.Close()
	defer client.Close()
	data := bytes.Repeat([]byte("input-state-is-replayed-before-new-bytes\n"), 100000)
	written := make(chan error, 1)
	go func() { _, err := client.Write(data); written <- err }()
	select {
	case err := <-written:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("state replay stopped socket read-ahead")
	}
	a.mu.Lock()
	spilled := a.diskEnd
	a.mu.Unlock()
	if spilled == 0 {
		t.Fatal("test did not exceed the in-memory read-ahead bound")
	}
	a.finish()
	got := make([]byte, len(data))
	if _, err := io.ReadFull(a, got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("read-ahead spill changed input order")
	}
}

func TestInputHandoffRepeated(t *testing.T) {
	cfg := conf.MakeDefault()
	cfg.MainQueueBatchSize = 0
	cfg.BatchFlushPerdiodSec = 0
	cfg.IncomingConnIdleTimeoutSec = 10
	e, _ := codec.NewEncoder(codec.Options{Flags: codec.FlagZstd})
	defer e.Close()
	wire, _ := codec.AppendPreamble(nil, codec.FlagZstd)
	for i := range 200 {
		_ = e.Add([]byte(fmt.Sprint("name", i%3)), float64(i), int64(100+i))
		wire, _ = e.Flush(wire)
	}
	q := make(chan [][]byte, 1000)
	var mu sync.Mutex
	var rows []string
	factory := func() StreamSink { return &accountingSink{&mu, &rows} }
	start := func() (*TCPInput, chan struct{}, chan struct{}, net.Listener) {
		stop, done := make(chan struct{}), make(chan struct{})
		h := NewTCPInput(q, stop, nil, &cfg, metrics.New(), zap.NewNop(), nil, factory)
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		go func() { h.Serve(l, stop); close(done) }()
		return h, stop, done, l
	}
	old, stop, done, l := start()
	client, err := net.Dial("tcp", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	offset := 0
	for generation := range 12 {
		end := (generation + 1) * len(wire) / 13
		if _, err = client.Write(wire[offset:end]); err != nil {
			t.Fatal(err)
		}
		offset = end
		deadline := time.Now().Add(time.Second)
		for {
			old.mu.Lock()
			n := len(old.conns)
			old.mu.Unlock()
			if n == 1 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("connection not tracked")
			}
			time.Sleep(time.Millisecond)
		}
		next, nextStop, nextDone, _ := start()
		a, b := handoffPair(t)
		receive := make(chan error, 1)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		go func() { receive <- next.Receive(ctx, b) }()
		if err = old.Send(ctx, a); err != nil {
			t.Fatal(err)
		}
		if err = <-receive; err != nil {
			t.Fatal(err)
		}
		cancel()
		a.Close()
		b.Close()
		close(stop)
		<-done
		old, stop, done = next, nextStop, nextDone
	}
	if _, err = client.Write(wire[offset:]); err != nil {
		t.Fatal(err)
	}
	client.Close()
	old.wg.Wait()
	close(stop)
	<-done
	if len(rows) != 200 {
		t.Fatalf("repeated handoffs emitted %d rows", len(rows))
	}
	for i, r := range rows {
		if r != fmt.Sprintf("name%d %d %d", i%3, i, 100+i) {
			t.Fatalf("point %d changed: %s", i, r)
		}
	}
}

func TestInputHandoffRejectsUnknownVersionBeforePause(t *testing.T) {
	a, b := handoffPair(t)
	go func() { _, _ = b.Write([]byte("NTINPUT9")) }()
	cfg := conf.MakeDefault()
	h := NewTCPInput(make(chan [][]byte), make(chan struct{}), nil, &cfg, metrics.New(), zap.NewNop(), nil)
	if err := h.Send(context.Background(), a); err == nil {
		t.Fatal("unknown protocol accepted")
	}
}

type identitySink struct {
	mu   sync.Mutex
	seen []bool
	bad  bool
}

func (s *identitySink) Point(_ uint32, name []byte, v float64, ts int64, _ *any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := int(ts - 100)
	if i < 0 || i >= len(s.seen) || s.seen[i] || v != float64(i) || len(name) == 0 {
		s.bad = true
		return
	}
	s.seen[i] = true
}
func (s *identitySink) Reset() {}
func (s *identitySink) Flush() {}

func TestInputLargeHandoff(t *testing.T) {
	for _, compact := range []bool{false, true} {
		for _, flags := range []byte{0, codec.FlagZstd} {
			t.Run(fmt.Sprintf("flags%d/compact%t", flags, compact), func(t *testing.T) {
				names := 50000
				if raw := os.Getenv("NANOTUBE_HANDOFF_STRESS_NAMES"); raw != "" {
					var err error
					names, err = strconv.Atoi(raw)
					if err != nil || names < 1 || names > 800000 {
						t.Fatal("invalid stress name count")
					}
				}
				sink := &identitySink{seen: make([]bool, names+1)}
				cfg := conf.MakeDefault()
				cfg.IncomingConnIdleTimeoutSec = 30
				cfg.StreamInputMaxDictionaryBytes = 256 << 20
				q := make(chan [][]byte, 1)
				stop := make(chan struct{})
				oldMS, nextMS := metrics.New(), metrics.New()
				factory := func() StreamSink {
					if compact {
						return &boundHandoffSink{collector: sink}
					}
					return sink
				}
				old := NewTCPInput(q, stop, nil, &cfg, oldMS, zap.NewNop(), nil, factory)
				l, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				done := make(chan struct{})
				go func() { old.Serve(l, stop); close(done) }()
				client, err := net.Dial("tcp", l.Addr().String())
				if err != nil {
					t.Fatal(err)
				}
				defer client.Close()
				e, _ := codec.NewEncoder(codec.Options{Flags: flags, DictionaryBytes: 256 << 20, PeerDictionaryBytes: 256 << 20})
				defer e.Close()
				wire, _ := codec.AppendPreamble(nil, flags)
				for i := range names {
					if err = e.Add([]byte(fmt.Sprintf("metric.%0240d", i)), float64(i), int64(100+i)); err != nil {
						t.Fatal(err)
					}
				}
				wire, err = e.Flush(wire)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = client.Write(wire); err != nil {
					t.Fatal(err)
				}
				deadline := time.Now().Add(5 * time.Second)
				for {
					sink.mu.Lock()
					parsed := sink.seen[names-1]
					sink.mu.Unlock()
					if parsed {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("large dictionary was not primed")
					}
					time.Sleep(time.Millisecond)
				}
				_ = e.Add([]byte(fmt.Sprintf("metric.%0240d", 0)), float64(names), int64(100+names))
				tail, _ := e.Flush(nil)
				if _, err = client.Write(tail[:len(tail)/2]); err != nil {
					t.Fatal(err)
				}
				next := NewTCPInput(q, make(chan struct{}), nil, &cfg, nextMS, zap.NewNop(), nil, factory)
				a, b := handoffPair(t)
				received := make(chan error, 1)
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				go func() { received <- next.Receive(ctx, b) }()
				started := time.Now()
				retained := testutil.ToFloat64(oldMS.StreamInputDictionaryBytes)
				old.mu.Lock()
				var transferred *inputConn
				for c := range old.conns {
					transferred = c
				}
				old.mu.Unlock()
				if err = old.Send(ctx, a); err != nil {
					t.Fatal(err)
				}
				// Replay can finish before deferred reader cleanup. Waiting here
				// checks eventual release, without imposing that wait on Send.
				<-transferred.done
				if transferred.releaseBytes == 0 || testutil.ToFloat64(oldMS.StreamInputDictionaryBytes) != 0 || testutil.ToFloat64(oldMS.StreamInputReservedBytes) != 0 {
					t.Fatal("handoff retained predecessor dictionary storage or reservations")
				}
				if err = <-received; err != nil {
					t.Fatal(err)
				}
				t.Logf("names=%d retained_dictionary_bytes=%.0f state_bytes=%d handoff_seconds=%.6f", names, retained, transferred.stateBytes, time.Since(started).Seconds())
				close(stop)
				<-done
				if _, err = client.Write(tail[len(tail)/2:]); err != nil {
					t.Fatal(err)
				}
				client.Close()
				next.wg.Wait()
				if sink.bad {
					t.Fatal("large dictionary handoff changed or duplicated a point")
				}
				for i, seen := range sink.seen {
					if !seen {
						t.Fatalf("point %d missing", i)
					}
				}
				if testutil.ToFloat64(oldMS.InputHandoffConnections) != 1 || testutil.ToFloat64(nextMS.StreamInputReservedBytes) != 0 {
					t.Fatal("large handoff accounting/reservations")
				}
			})
		}
	}
}

func TestInheritedStreamRespectsSuccessorReservation(t *testing.T) {
	cfg := conf.MakeDefault()
	cfg.IncomingConnIdleTimeoutSec = 10
	oldMS, nextMS := metrics.New(), metrics.New()
	q := make(chan [][]byte, 1)
	stop := make(chan struct{})
	factory := func() StreamSink { return &testStreamSink{} }
	old := NewTCPInput(q, stop, nil, &cfg, oldMS, zap.NewNop(), nil, factory)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { old.Serve(l, stop); close(done) }()
	client, err := net.Dial("tcp", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	wire, _ := codec.AppendPreamble(nil, 0)
	if _, err = client.Write(wire); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for testutil.ToFloat64(oldMS.StreamInputActive) != 1 {
		if time.Now().After(deadline) {
			t.Fatal("old stream not admitted")
		}
		time.Sleep(time.Millisecond)
	}
	nextCfg := cfg
	nextCfg.StreamInputTotalBytes = 1
	next := NewTCPInput(q, make(chan struct{}), nil, &nextCfg, nextMS, zap.NewNop(), nil, factory)
	a, b := handoffPair(t)
	received := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() { received <- next.Receive(ctx, b) }()
	if err = old.Send(ctx, a); err == nil {
		t.Fatal("over-budget inherited stream accepted")
	}
	a.Close()
	<-received
	close(stop)
	<-done
	next.wg.Wait()
	if testutil.ToFloat64(nextMS.StreamInputErrors) != 1 || testutil.ToFloat64(nextMS.InputHandoffFailures) == 0 {
		t.Fatal("inherited reservation rejection was not counted")
	}
	for _, ms := range []*metrics.Prom{oldMS, nextMS} {
		if testutil.ToFloat64(ms.StreamInputReservedBytes) != 0 {
			t.Fatal("inherited reservation leaked")
		}
	}
}
