//go:build unix

package target

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/metrics"
	"github.com/bookingcom/nanotube/pkg/rec"
	"go.uber.org/zap"
)

// TestStreamClientPool is an opt-in 40-relay sticky memory soak. It reuses the
// sparse rollup model from TestStreamChurn. Generation stays outside CPU windows;
// real routing, queue admission, encoding and writes stay inside them.
func TestStreamClientPool(t *testing.T) {
	if os.Getenv("STREAM_CLIENT_POOL") == "" {
		t.Skip("set STREAM_CLIENT_POOL=1")
	}
	pool := 1_080_000
	if n, err := strconv.Atoi(os.Getenv("STREAM_CLIENT_NAMES")); err == nil && n > 60 {
		pool = n
	}
	var fixture *bufio.Writer
	if path := os.Getenv("STREAM_CLIENT_FIXTURE"); path != "" {
		file, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		fixture = bufio.NewWriterSize(file, 1<<20)
		defer fixture.Flush()
	}
	cfg := conf.MakeDefault()
	cfg.TCPInitialConnCheck = false
	cfg.TCPOutConnectionRefreshPeriodSec = 0
	cfg.HostQueueSize = 8192
	cfg.TCPOutBufSize = 4096
	cfg.StreamDictionaryBytes = 256 << 20
	ms := metrics.New()
	cl := &Cluster{Name: "relays", Type: conf.StickyCluster}
	opts := conf.StreamOptions{Zstd: true, DictionaryBytes: 1 << 20, MaxBytes: cfg.TCPOutBufSize}.WithDefaults(1)
	var hosts []*Host
	var senders []*streamSender
	var sockets []*countingStreamConn
	for i := range 40 {
		h := ConstructHost("relays", cfg, conf.Host{Name: fmt.Sprintf("relay-%02d", i), Port: 2003, Stream: true, StreamOptions: opts}, zap.NewNop(), ms)
		w := &countingStreamConn{}
		h.Conn.New(w, 1<<20)
		h.Available.Store(true)
		cl.Hosts = append(cl.Hosts, h)
		hosts = append(hosts, h)
		senders = append(senders, h.makeStreamSender(opts))
		sockets = append(sockets, w)
	}
	var err error
	cl.sticky, err = newStickyCluster(cl, ms)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, s := range senders {
			for i := range s.connections {
				if s.connections[i].codec != nil {
					s.connections[i].codec.close()
				}
			}
		}
	}()
	const window = 4096
	records := make([]*rec.RecBytes, window)
	batch := NewBatch()
	var cpu, count, plain int64
	for minute := range 61 {
		n, idAt := rollupMinute(minute, pool)
		for first := 0; first < n; first += window {
			size := min(window, n-first)
			for i := range size {
				id := idAt(first + i)
				name := fmt.Sprintf("example.application.%02d.region.%02d.namespace.%s.node.%012x.requests", id%32, id%7, strings.Repeat("component.", 16+int(id%7)), id)
				r, err := rec.ParseRec(fmt.Appendf(nil, "%s %d.%03d %d", name, id%10000, (id+uint64(minute*13))%1000, 1700000000+minute*60), false, false, time.Now, zap.NewNop())
				if err != nil {
					t.Fatal(err)
				}
				records[i] = r
				plain += int64(r.PlainSize())
				if fixture != nil && minute == 0 {
					if _, err := fmt.Fprintln(fixture, name); err != nil {
						t.Fatal(err)
					}
				}
			}
			before := outputCPUTime(t)
			for _, r := range records[:size] {
				if err := cl.PushBytesBatch(r, ms, batch); err != nil {
					t.Fatal(err)
				}
			}
			batch.Flush()
			for i, h := range hosts {
				for h.q.n > 0 {
					chunk := h.q.pop()
					senders[i].dispatch(chunk.recs[:chunk.n])
					h.q.recycle(chunk)
				}
			}
			cpu += outputCPUTime(t) - before
			count += int64(size)
			clear(records)
		}
		before := outputCPUTime(t)
		for _, s := range senders {
			for i := range s.connections {
				s.flush(&s.connections[i])
			}
		}
		cpu += outputCPUTime(t) - before
		runtime.GC()
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		rss, _ := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(os.Getpid())).Output()
		var wire int64
		var names, retained int
		for i, s := range senders {
			wire += sockets[i].bytes
			for j := range s.connections {
				if r, ok := s.connections[j].codec.(interface{ retention() (int, int, int) }); ok {
					n, b, _ := r.retention()
					names += n
					retained += b
				}
			}
		}
		shared, sharedNames, _, _ := ms.Stream.DictionaryStats()
		fmt.Printf("CLIENT minute=%d pool=%d relays=40 records=%d cpu_ns=%.3f wire_B=%.4f plaintext_B=%.4f rss_KiB=%s heap=%d shared_bytes=%d shared_names=%d connection_bytes=%d connection_names=%d\n", minute, pool, count, float64(cpu)/float64(count), float64(wire)/float64(count), float64(plain)/float64(count), strings.TrimSpace(string(rss)), m.HeapAlloc, shared, sharedNames, retained, names)
	}
	flushed := int64(0)
	for _, h := range hosts {
		flushed += h.q.output.flushedRecords
	}
	if flushed != count {
		t.Fatalf("flushed %d, expected %d", flushed, count)
	}
}
