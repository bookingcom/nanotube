package in

import (
	"bufio"
	"bytes"
	"fmt"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/metrics"
	codec "github.com/bookingcom/nanotube/pkg/streamcodec"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.uber.org/zap"
)

type testStreamSink struct{ rows, resets int }

func (s *testStreamSink) Point(_ uint32, _ []byte, _ float64, _ int64, _ *any) { s.rows++ }
func (s *testStreamSink) Reset()                                               { s.resets++ }
func (s *testStreamSink) Flush()                                               {}
func TestStreamInputFrameBounds(t *testing.T) {
	for _, z := range []bool{false, true} {
		flags := byte(0)
		if z {
			flags = codec.FlagZstd
		}
		e, err := codec.NewEncoder(codec.Options{Flags: flags})
		if err != nil {
			t.Fatal(err)
		}
		wire, _ := codec.AppendPreamble(nil, flags)
		if err = e.Add([]byte("example.one"), 1, 2); err != nil {
			t.Fatal(err)
		}
		wire, err = e.Flush(wire)
		if err != nil {
			t.Fatal(err)
		}
		e.Close()
		for _, mode := range []string{"valid", "crc", "truncated", "dictionary-bytes", "frame-bytes", "preamble"} {
			t.Run(mode, func(t *testing.T) {
				input := append([]byte(nil), wire...)
				cfg := conf.MakeDefault()
				switch mode {
				case "crc":
					input[len(input)-1] ^= 1
				case "truncated":
					input = input[:len(input)-1]
				case "dictionary-bytes":
					cfg.StreamInputMaxDictionaryBytes = 4096
					enc, _ := codec.NewEncoder(codec.Options{Flags: flags})
					_ = enc.Add(bytes.Repeat([]byte("x"), 5000), 1, 2)
					input, _ = codec.AppendPreamble(nil, flags)
					input, _ = enc.Flush(input)
					enc.Close()
				case "frame-bytes":
					input[codec.PreambleSize+4] = 0x7f
				case "preamble":
					input[8] = 9
				}
				ms := metrics.New()
				sink := &testStreamSink{}
				readStream(bufio.NewReader(bytes.NewReader(input)), make(chan struct{}), nil, &cfg, ms, zap.NewNop(), sink)
				if mode == "valid" {
					if sink.rows != 1 {
						t.Fatal(sink.rows)
					}
				} else {
					if sink.rows != 0 || testutil.ToFloat64(ms.StreamInputErrors) != 1 {
						t.Fatalf("bad frame emitted rows=%d", sink.rows)
					}
				}
			})
		}
	}
}

func TestStreamInputResetAndReservationRelease(t *testing.T) {
	e, _ := codec.NewEncoder(codec.Options{MaxNames: 1})
	wire, _ := codec.AppendPreamble(nil, 0)
	for _, name := range []string{"one", "two", "one"} {
		_ = e.Add([]byte(name), 1, 2)
	}
	wire, _ = e.Flush(wire)
	e.Close()
	cfg := conf.MakeDefault()
	ms := metrics.New()
	sink := &testStreamSink{}
	readStream(bufio.NewReader(bytes.NewReader(wire)), make(chan struct{}), nil, &cfg, ms, zap.NewNop(), sink)
	if sink.rows != 3 || sink.resets != 2 || testutil.ToFloat64(ms.StreamInputResets) != 2 {
		t.Fatalf("rows=%d resets=%d", sink.rows, sink.resets)
	}
	if testutil.ToFloat64(ms.StreamInputDictionaryBytes) != 0 || testutil.ToFloat64(ms.StreamInputReservedBytes) != 0 || testutil.ToFloat64(ms.StreamInputActive) != 0 {
		t.Fatal("closed input retained a reservation or dictionary gauge")
	}
	cfg.StreamInputTotalBytes = 4095
	readStream(bufio.NewReader(bytes.NewReader(wire)), make(chan struct{}), nil, &cfg, ms, zap.NewNop(), sink)
	if testutil.ToFloat64(ms.StreamInputErrors) != 1 || sink.rows != 3 {
		t.Fatal("over-budget input was admitted")
	}
}

type holdingStreamSink struct{ names [][]byte }

func (s *holdingStreamSink) Point(_ uint32, name []byte, _ float64, _ int64, _ *any) {
	s.names = append(s.names, name)
}
func (*holdingStreamSink) Reset() {}
func (*holdingStreamSink) Flush() {}

func TestStreamInputQueuedNamesSurviveArenaReuse(t *testing.T) {
	e, _ := codec.NewEncoder(codec.Options{MaxNames: 1})
	wire, _ := codec.AppendPreamble(nil, 0)
	for _, name := range []string{"example.aaaaaaaa", "example.bbbbbbbb", "example.cccccccc"} {
		_ = e.Add([]byte(name), 1, 2)
	}
	wire, _ = e.Flush(wire)
	e.Close()
	cfg := conf.MakeDefault()
	ms := metrics.New()
	sink := &holdingStreamSink{}
	readStream(bufio.NewReader(bytes.NewReader(wire)), make(chan struct{}), nil, &cfg, ms, zap.NewNop(), sink)
	if got := fmt.Sprint(sink.names); got != fmt.Sprint([][]byte{[]byte("example.aaaaaaaa"), []byte("example.bbbbbbbb"), []byte("example.cccccccc")}) {
		t.Fatalf("reset changed queued names: %s", got)
	}
}

type idleTimeout struct{}

func (idleTimeout) Error() string   { return "i/o timeout" }
func (idleTimeout) Timeout() bool   { return true }
func (idleTimeout) Temporary() bool { return true }

// idleReader yields its bytes, then fails like a connection whose idle read deadline expired.
type idleReader struct{ data []byte }

func (r *idleReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, idleTimeout{}
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, nil
}

func TestStreamInputIdleTimeoutIsNotAnError(t *testing.T) {
	e, _ := codec.NewEncoder(codec.Options{})
	wire, _ := codec.AppendPreamble(nil, 0)
	_ = e.Add([]byte("one"), 1, 2)
	wire, _ = e.Flush(wire)
	e.Close()
	cfg := conf.MakeDefault()
	ms := metrics.New()
	sink := &testStreamSink{}
	readStream(bufio.NewReader(&idleReader{data: wire}), make(chan struct{}), nil, &cfg, ms, zap.NewNop(), sink)
	if sink.rows != 1 {
		t.Fatalf("rows before idle close: %d", sink.rows)
	}
	if n := testutil.ToFloat64(ms.StreamInputErrors) + testutil.ToFloat64(ms.ErrorRecs); n != 0 {
		t.Fatalf("idle close counted as %v errors", n)
	}
}

func TestStreamInputGrowthRejectsWholeFrame(t *testing.T) {
	e, _ := codec.NewEncoder(codec.Options{})
	wire, _ := codec.AppendPreamble(nil, 0)
	_ = e.Add([]byte("example.one"), 1, 2)
	wire, _ = e.Flush(wire)
	// The first frame fits. The second needs another decoder capacity step.
	for i := range 20 {
		_ = e.Add(fmt.Appendf(nil, "example.%d", i), 1, 2)
	}
	wire, _ = e.Flush(wire)
	e.Close()
	cfg := conf.MakeDefault()
	cfg.StreamInputTotalBytes = 6000
	ms := metrics.New()
	sink := &testStreamSink{}
	readStream(bufio.NewReader(bytes.NewReader(wire)), make(chan struct{}), nil, &cfg, ms, zap.NewNop(), sink)
	if sink.rows != 1 || testutil.ToFloat64(ms.StreamInputErrors) != 1 {
		t.Fatalf("growth failure emitted part of a frame: rows=%d", sink.rows)
	}
	if testutil.ToFloat64(ms.StreamInputReservedBytes) != 0 || testutil.ToFloat64(ms.StreamInputDictionaryBytes) != 0 {
		t.Fatal("failed growth leaked capacity")
	}
}

type churnStreamSink struct{ rows, resets *atomic.Int64 }

func (s *churnStreamSink) Point(uint32, []byte, float64, int64, *any) { s.rows.Add(1) }
func (s *churnStreamSink) Reset()                                     { s.resets.Add(1) }
func (*churnStreamSink) Flush()                                       {}

func TestStreamInputConnectResetCloseChurn(t *testing.T) {
	cfg := conf.MakeDefault()
	cfg.StreamInputTotalBytes = 64 << 10
	ms := metrics.New()
	var rows, resets atomic.Int64
	stop, done := make(chan struct{}), make(chan struct{})
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	input := NewTCPInput(nil, stop, nil, &cfg, ms, zap.NewNop(), nil, func() StreamSink {
		return &churnStreamSink{rows: &rows, resets: &resets}
	})
	go func() { input.Serve(l, stop); close(done) }()
	defer func() { close(stop); <-done }()
	wires := make([][]byte, 2)
	for flags := range 2 {
		e, err := codec.NewEncoder(codec.Options{Flags: byte(flags), MaxNames: 1})
		if err != nil {
			t.Fatal(err)
		}
		wire, _ := codec.AppendPreamble(nil, byte(flags))
		for _, name := range []string{"example.one", "example.two", "example.three"} {
			if err := e.Add([]byte(name), 1, 2); err != nil {
				t.Fatal(err)
			}
		}
		wires[flags], err = e.Flush(wire)
		if err != nil {
			t.Fatal(err)
		}
		e.Close()
	}
	for i := range 100 {
		conn, err := net.Dial("tcp", l.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		_, err = conn.Write(wires[i%2])
		_ = conn.Close()
		if err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(time.Second)
		for rows.Load() != int64((i+1)*3) || testutil.ToFloat64(ms.StreamInputReservedBytes) != 0 {
			if time.Now().After(deadline) {
				t.Fatal("connection failed to drain or release admission")
			}
			time.Sleep(time.Millisecond)
		}
	}
	if resets.Load() != 200 || testutil.ToFloat64(ms.StreamInputDictionaryBytes) != 0 || testutil.ToFloat64(ms.StreamInputErrors) != 0 {
		t.Fatal("connect/reset/close churn leaked state or rejected valid frames")
	}
}
