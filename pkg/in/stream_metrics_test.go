package in

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"testing"

	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/metrics"
	codec "github.com/bookingcom/nanotube/pkg/streamcodec"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.uber.org/zap"
)

// Each chunk ends at a frame boundary, so check runs after the previous frame
// was accounted, before ReadFrame can consume the next one. No timers or sleeps.
type streamBoundaryReader struct {
	chunks [][]byte
	index  int
	check  func(int)
}

func (r *streamBoundaryReader) Read(p []byte) (int, error) {
	for len(r.chunks) > 0 && len(r.chunks[0]) == 0 {
		r.chunks = r.chunks[1:]
		r.index++
		r.check(r.index)
	}
	if len(r.chunks) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.chunks[0])
	r.chunks[0] = r.chunks[0][n:]
	return n, nil
}

func TestStreamInputDictionaryMetrics(t *testing.T) {
	for _, flags := range []byte{0, codec.FlagZstd} {
		for _, bound := range []bool{false, true} {
			t.Run(fmt.Sprintf("flags=%d/bound=%t", flags, bound), func(t *testing.T) {
				opts := codec.Options{Flags: flags}
				e, err := codec.NewEncoder(opts)
				if err != nil {
					t.Fatal(err)
				}
				defer e.Close()
				wire, _ := codec.AppendPreamble(nil, flags)
				for _, name := range []string{"one", "two", "one"} {
					if err := e.Add([]byte(name), 1, 2); err != nil {
						t.Fatal(err)
					}
				}
				wire, err = e.Flush(wire)
				if err != nil {
					t.Fatal(err)
				}
				// A resident-id frame must not increment the definition counter.
				if err := e.Add([]byte("one"), 3, 4); err != nil {
					t.Fatal(err)
				}
				wire, err = e.Flush(wire)
				if err != nil {
					t.Fatal(err)
				}
				if err := e.Reset(); err != nil {
					t.Fatal(err)
				}
				wire, err = e.Flush(wire)
				if err != nil {
					t.Fatal(err)
				}
				if err := e.Add([]byte("three"), 5, 6); err != nil {
					t.Fatal(err)
				}
				wire, err = e.Flush(wire)
				if err != nil {
					t.Fatal(err)
				}
				oracle, err := codec.NewDecoder(opts)
				if err != nil {
					t.Fatal(err)
				}
				defer oracle.Close()
				type counts struct{ names, data, state int }
				want := []counts{{}}
				chunks := [][]byte{wire[:codec.PreambleSize]}
				appendFrames := func(frames []byte) {
					for len(frames) > 0 {
						n := codec.HeaderSize + int(binary.BigEndian.Uint32(frames[4:8]))
						frame := frames[:n]
						info, err := oracle.Decode(frame, nil)
						if err != nil {
							t.Fatal(err)
						}
						next := want[len(want)-1]
						next.names = oracle.NameCount()
						if info.Kind == codec.StateFrame {
							next.state += int(info.Names)
						} else {
							next.data += int(info.Names)
						}
						want = append(want, next)
						chunks = append(chunks, frame)
						frames = frames[n:]
					}
				}
				appendFrames(wire[codec.PreambleSize:])
				state, err := oracle.Snapshot(nil)
				if err != nil {
					t.Fatal(err)
				}
				appendFrames(state)
				cfg, ms := conf.MakeDefault(), metrics.New()
				reader := &streamBoundaryReader{chunks: chunks}
				baseData, baseState := 0, 0
				reader.check = func(index int) {
					w := want[index-1]
					if got := testutil.ToFloat64(ms.StreamInputDictionaryNames); got != float64(w.names) {
						t.Fatalf("boundary %d: names %g != oracle %d", index, got, w.names)
					}
					for kind, count := range map[string]int{"data": w.data + baseData, "state": w.state + baseState} {
						if got := testutil.ToFloat64(ms.StreamInputNamesDefined.WithLabelValues(kind)); got != float64(count) {
							t.Fatalf("boundary %d: %s definitions %g != %d", index, kind, got, count)
						}
					}
				}
				for range 2 { // Reconnect with the same names and a fresh dictionary.
					reader.chunks = append([][]byte(nil), chunks...)
					reader.index = 0
					var sink StreamSink = &testStreamSink{}
					if bound {
						sink = &boundAccountingSink{}
					}
					readStream(bufio.NewReader(reader), make(chan struct{}), nil, &cfg, ms, zap.NewNop(), sink)
					if got := testutil.ToFloat64(ms.StreamInputDictionaryNames); got != 0 {
						t.Fatalf("close retained %g names", got)
					}
					if testutil.ToFloat64(ms.StreamInputErrors) != 0 {
						t.Fatal("valid frame rejected")
					}
					baseData += want[len(want)-1].data
					baseState += want[len(want)-1].state
				}
			})
		}
	}
}

func TestStreamInputDictionaryNamesSumConnections(t *testing.T) {
	e, err := codec.NewEncoder(codec.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	wire, _ := codec.AppendPreamble(nil, 0)
	for _, name := range []string{"one", "two"} {
		if err := e.Add([]byte(name), 1, 2); err != nil {
			t.Fatal(err)
		}
	}
	wire, err = e.Flush(wire)
	if err != nil {
		t.Fatal(err)
	}
	cfg, ms := conf.MakeDefault(), metrics.New()
	outer := &streamBoundaryReader{chunks: [][]byte{wire}, check: func(int) {
		if got := testutil.ToFloat64(ms.StreamInputDictionaryNames); got != 2 {
			t.Fatalf("outer names %g", got)
		}
		sink := &boundAccountingSink{}
		sink.flush = func() {
			want := 4.0
			if sink.bytes == 0 {
				want = 2
			} // Final flush after inner close.
			if got := testutil.ToFloat64(ms.StreamInputDictionaryNames); got != want {
				t.Fatalf("overlapping identical dictionaries: %g != %g", got, want)
			}
		}
		readStream(bufio.NewReader(bytes.NewReader(wire)), make(chan struct{}), nil, &cfg, ms, zap.NewNop(), sink)
	}}
	readStream(bufio.NewReader(outer), make(chan struct{}), nil, &cfg, ms, zap.NewNop(), &testStreamSink{})
	if testutil.ToFloat64(ms.StreamInputDictionaryNames) != 0 || testutil.ToFloat64(ms.StreamInputNamesDefined.WithLabelValues("data")) != 4 {
		t.Fatal("close leaked names or definitions were deduplicated")
	}
}
