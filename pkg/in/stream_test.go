package in

import (
	"bufio"
	"bytes"
	"fmt"
	"testing"

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
	cfg.StreamInputTotalBytes = 4096
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
