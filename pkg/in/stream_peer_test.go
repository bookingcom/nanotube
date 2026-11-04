package in

import (
	"bufio"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/metrics"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.uber.org/zap"
)

type peerInputSink struct{ points map[string]int }

func (s *peerInputSink) Point(_ uint32, name []byte, value float64, ts int64, _ *any) {
	s.points[fmt.Sprintf("%s/%x/%d", name, math.Float64bits(value), ts)]++
}
func (*peerInputSink) Reset() {}
func (*peerInputSink) Flush() {}

// Native fixture export comes from the independent producer's conformance test.
// It covers ordinary/bound/mixed IDs, automatic byte resets and zstd frames.
func TestNativeStreamInputFrames(t *testing.T) {
	dir := os.Getenv("NATIVE_STREAM_FIXTURES")
	if dir == "" {
		t.Skip("set NATIVE_STREAM_FIXTURES to independent producer fixtures")
	}
	for _, mode := range []string{"rust", "bound", "mixed", "churn", "churn-bound"} {
		series := 23
		if mode == "churn" || mode == "churn-bound" {
			series = 1024
		}
		for flags := range 2 {
			t.Run(fmt.Sprintf("%s-%d", mode, flags), func(t *testing.T) {
				file, err := os.Open(filepath.Join(dir, fmt.Sprintf("%s-%d.bin", mode, flags)))
				if err != nil {
					t.Fatal(err)
				}
				defer file.Close()
				sink := &peerInputSink{points: make(map[string]int)}
				cfg := conf.MakeDefault()
				ms := metrics.New()
				readStream(bufio.NewReader(file), make(chan struct{}), nil, &cfg, ms, zap.NewNop(), sink)
				want := &peerInputSink{points: make(map[string]int)}
				for i := range 10000 {
					bits := uint64(0x3ff0000000000000) + uint64(i)
					if i%37 == 0 {
						bits = 0x7ff8000000001234
					} else if i%31 == 0 {
						bits = 0x8000000000000000
					}
					want.Point(0, []byte(fmt.Sprintf("example.peer.%d", i%series)), math.Float64frombits(bits), int64(1700000000+i/series), nil)
				}
				if testutil.ToFloat64(ms.StreamInputErrors) != 0 || !reflect.DeepEqual(want.points, sink.points) {
					t.Fatal("native frames changed points or failed validation")
				}
			})
		}
	}
}
