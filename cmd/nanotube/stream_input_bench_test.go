package main

import (
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"unsafe"

	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/metrics"
	"github.com/bookingcom/nanotube/pkg/rec"
	"github.com/bookingcom/nanotube/pkg/rewrites"
	"github.com/bookingcom/nanotube/pkg/rules"
	codec "github.com/bookingcom/nanotube/pkg/streamcodec"
	"github.com/bookingcom/nanotube/pkg/target"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.uber.org/zap"
)

func modelCPU() int64 {
	var r syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &r); err != nil {
		panic(err)
	}
	return (r.Utime.Sec+r.Stime.Sec)*1e9 + (int64(r.Utime.Usec)+int64(r.Stime.Usec))*1000
}

// This model also runs unchanged on the parent revision: the optional binder
// selects shared names on revisions that provide it, and the original immutable
// per-connection names otherwise. All receivers inspect the emitted typed data.
type modelName struct {
	name []byte
	plan any
}

func (n *modelName) StreamName() []byte { return n.name }

type modelBinder interface {
	BindName([]byte) any
	ReleaseName(any)
}

type modelReceiver struct{ count, sum atomic.Uint64 }

func (h *modelReceiver) Push(r *rec.RecBytes) {
	v, ts, ok := r.Values()
	if !ok || v != 1 || ts != 1700000000 || !strings.HasPrefix(string(r.Path), "example.") {
		panic("stream model changed a receiver record")
	}
	h.count.Add(1)
	h.sum.Add(r.PathHash())
}
func (*modelReceiver) Stream(*sync.WaitGroup) {}
func (*modelReceiver) Stop()                  {}
func (*modelReceiver) IsAvailable() bool      { return true }

type modelStream struct {
	d        *codec.Decoder
	s        *streamRouter
	receiver *modelReceiver
	names    int
	shared   bool
}

func modelWires(t testing.TB, unique, sharing, epoch int) ([][]byte, []byte) {
	t.Helper()
	wires := make([][]byte, 80/sharing)
	var warm []byte
	for stripe := range wires {
		e, err := codec.NewEncoder(codec.Options{DictionaryBytes: 64 << 20, PeerDictionaryBytes: 1 << 30, MaxRows: 1024})
		if err != nil {
			t.Fatal(err)
		}
		if err := e.Reset(); err != nil {
			t.Fatal(err)
		}
		for i := stripe; i < unique; i += len(wires) {
			// 60..120 bytes, 5% replaced between epochs, the rest unchanged.
			generation := 0
			if i%20 == 0 {
				generation = epoch
			}
			name := fmt.Sprintf("example.%08d.epoch%d.%s", i, generation, strings.Repeat("x", 60+i%61-24))
			if err := e.Add([]byte(name), 1, 1700000000); err != nil {
				t.Fatal(err)
			}
		}
		wires[stripe], err = e.Flush(nil)
		if err != nil {
			t.Fatal(err)
		}
		for j := range 1024 {
			i := stripe + ((j*37)%((unique+len(wires)-1-stripe)/len(wires)))*len(wires)
			name := fmt.Sprintf("example.%08d.epoch%d.%s", i, func() int {
				if i%20 == 0 {
					return epoch
				}
				return 0
			}(), strings.Repeat("x", 60+i%61-24))
			if err := e.Add([]byte(name), 1, 1700000000); err != nil {
				t.Fatal(err)
			}
		}
		warm, err = e.Flush(warm[:0])
		if err != nil {
			t.Fatal(err)
		}
		e.Close()
	}
	return wires, warm
}

func modelStreams(t testing.TB, unique ...int) ([]*modelStream, *metrics.Prom) {
	t.Helper()
	ms := metrics.New()
	streams := make([]*modelStream, 80)
	var first *streamRouter
	h := &modelReceiver{}
	rs, err := rules.Build(&conf.Rules{Rule: []conf.Rule{{Prefixes: []string{"example."}, Clusters: []string{"out"}}}}, target.Clusters{
		"out": {Name: "out", Type: conf.ToallCluster, Hosts: []target.Target{h}},
	}, false, ms)
	if err != nil {
		t.Fatal(err)
	}
	for i := range streams {
		var s *streamRouter
		if clone, ok := any(first).(interface{ NewSink() *streamRouter }); first != nil && ok {
			s = clone.NewSink()
		} else {
			s = newStreamRouter(rs, rewrites.Rewrites{}, true, zap.NewNop(), ms)
		}
		if os.Getenv("STREAM_INPUT_MODEL_SCALED_ROUTES") == "1" && len(unique) > 0 {
			s.maxBytes = max(1, (128<<20)*unique[0]/13000000)
		}
		// Every connection uses the same immutable rules and destination identities.
		if first == nil {
			first = s
		}
		d, err := codec.NewDecoder(codec.Options{DictionaryBytes: 1 << 30})
		if err != nil {
			t.Fatal(err)
		}
		m := &modelStream{d: d, s: s, receiver: h}
		if bind, ok := any(s).(modelBinder); ok {
			m.shared = true
			d.SetNameResolver(bind.BindName)
			any(d).(interface{ SetNameReleaser(func(any)) }).SetNameReleaser(bind.ReleaseName)
		} else {
			d.SetNameResolver(func(name []byte) any {
				m.names += len(name) + 64
				return &modelName{name: append([]byte(nil), name...)}
			})
		}
		streams[i] = m
	}
	return streams, ms
}

func (m *modelStream) decode(t testing.TB, wire []byte) {
	t.Helper()
	for len(wire) > 0 {
		n := codec.HeaderSize + int(binary.BigEndian.Uint32(wire[4:8]))
		info, err := m.d.Decode(wire[:n], func(id uint32, name []byte, v float64, ts int64, slot *any) {
			if !m.shared {
				slot = &(*slot).(*modelName).plan
			}
			m.s.Point(id, name, v, ts, slot)
		})
		if err != nil {
			t.Fatal(err)
		}
		if info.Kind == codec.ResetFrame {
			m.names = 0
			m.s.Reset()
		}
		m.s.Flush()
		wire = wire[n:]
	}
}

func closeModel(streams []*modelStream) {
	for _, m := range streams {
		m.d.Close()
		m.s.Close()
	}
}

func BenchmarkStreamInputModel(b *testing.B) {
	for _, sharing := range []int{20, 1} {
		b.Run(fmt.Sprintf("sharing%d", sharing), func(b *testing.B) {
			wires, warm := modelWires(b, 5120, sharing, 0)
			streams, _ := modelStreams(b, 5120)
			defer closeModel(streams)
			for i, m := range streams {
				m.decode(b, wires[i%len(wires)])
			}
			b.ReportAllocs()
			b.ResetTimer()
			cpu := modelCPU()
			var worker atomic.Uint32
			workers := runtime.GOMAXPROCS(0)
			if workers > len(streams) {
				b.Skip("model supports up to 80 benchmark workers")
			}
			b.RunParallel(func(pb *testing.PB) {
				w := int(worker.Add(1) - 1)
				first, end := w*len(streams)/workers, (w+1)*len(streams)/workers
				i := first
				for pb.Next() {
					streams[i].decode(b, warm)
					i++
					if i == end {
						i = first
					}
				}
			})
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*1024), "ns/record")
			b.ReportMetric(float64(modelCPU()-cpu)/float64(b.N*1024), "cpu-ns/record")
		})
	}
}

func BenchmarkStreamInputModelChurn(b *testing.B) {
	for _, sharing := range []int{20, 1} {
		b.Run(fmt.Sprintf("sharing%d", sharing), func(b *testing.B) {
			wires0, _ := modelWires(b, 2560, sharing, 0)
			wires1, _ := modelWires(b, 2560, sharing, 1)
			streams, _ := modelStreams(b, 2560)
			defer closeModel(streams)
			for i, m := range streams {
				m.decode(b, wires0[i%len(wires0)])
			}
			b.ReportAllocs()
			b.ResetTimer()
			cpu := modelCPU()
			for epoch := range b.N {
				wires := wires0
				if epoch%2 == 0 {
					wires = wires1
				}
				for i, m := range streams {
					m.decode(b, wires[i%len(wires)])
				}
			}
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*2560*sharing), "ns/record")
			b.ReportMetric(float64(modelCPU()-cpu)/float64(b.N*2560*sharing), "cpu-ns/record")
		})
	}
}

// Opt-in process memory model; fixtures are generated in RAM and never retained
// as per-connection copies. Run each shape in a fresh test process.
func TestStreamInputMemoryModel(t *testing.T) {
	count, _ := strconv.Atoi(os.Getenv("STREAM_INPUT_MODEL_NAMES"))
	if count == 0 {
		t.Skip("set STREAM_INPUT_MODEL_NAMES for the memory model")
	}
	sharing, _ := strconv.Atoi(os.Getenv("STREAM_INPUT_MODEL_SHARING"))
	if sharing == 0 {
		sharing = 20
	}
	streams, ms := modelStreams(t, count)
	defer closeModel(streams)
	for epoch := range 3 {
		wires, _ := modelWires(t, count, sharing, epoch)
		for i, m := range streams {
			m.decode(t, wires[i%len(wires)])
		}
		wires = nil
		runtime.GC()
		var mem runtime.MemStats
		runtime.ReadMemStats(&mem)
		dict := testutil.ToFloat64(ms.StreamInputDictionaryBytes)
		for _, m := range streams {
			dict += float64(m.d.RetainedBytes() + m.names)
		}
		rss, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(os.Getpid())).Output()
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("epoch=%d unique=%d sharing=%d streams=80 heap=%d heap_sys=%d rss_kib=%s dictionary=%.0f route=%.0f entry_size=%d output_count=%d output_hash_sum=%d", epoch, count, sharing, mem.HeapAlloc, mem.HeapSys, strings.TrimSpace(string(rss)), dict, testutil.ToFloat64(ms.StreamInputRouteBytes), unsafe.Sizeof(modelName{}), streams[0].receiver.count.Load(), streams[0].receiver.sum.Load())
	}
	runtime.KeepAlive(streams)
}
