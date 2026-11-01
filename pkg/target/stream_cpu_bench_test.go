//go:build unix

package target

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/metrics"
	"github.com/bookingcom/nanotube/pkg/rec"
	"go.uber.org/zap"
)

// BenchmarkStreamOutputCPU measures the complete per-host output path, including
// queue accounting, connection selection, parsing, framing, compression, metrics
// and writes. A memory sink excludes receiver/network CPU from both modes.
// Routing has already populated PathHash before a host sees these records.
//
// Run serially with -cpu=1; cpu-ns/record is process user+system CPU, whereas
// ns/record is elapsed time. Fixture construction and warmup are outside both.
func BenchmarkStreamOutputCPU(b *testing.B) {
	const names = 250_000 // approximately 25k distinct names on each of 10 sockets
	const samples = 4     // changed values avoid an unrealistically all-zero XOR column
	rs := make([]*rec.RecBytes, names*samples)
	for n := range rs {
		i, sample := n%names, n/names
		name := fmt.Sprintf("example.application.node.%02d.%sworker.%06d.requests", i%32, strings.Repeat("component.", 3+i%7), i)
		value := fmt.Sprint(i%10000 + sample*17)
		if i%3 == 0 {
			value = fmt.Sprintf("%d.%03d", i%1000+sample, (i+sample*13)%1000)
		}
		r, err := rec.ParseRec([]byte(name+" "+value+" 1700000000"), false, false, time.Now, zap.NewNop())
		if err != nil {
			b.Fatal(err)
		}
		r.PathHash()
		rs[n] = r
	}
	// Shared prefixes but interleaved names, rather than a tiny hot dictionary
	// or conveniently sorted input. The seed makes before/after reproducible.
	rng := rand.New(rand.NewPCG(1, 2))
	rng.Shuffle(len(rs), func(i, j int) { rs[i], rs[j] = rs[j], rs[i] })
	for _, mode := range []string{"zstd-plaintext", "stream-zstd"} {
		b.Run(mode, func(b *testing.B) {
			cfg := conf.MakeDefault()
			cfg.TCPInitialConnCheck = false
			cfg.TCPOutConnectionRefreshPeriodSec = 0
			cfg.HostQueueSize = 1024
			cfg.TCPOutCompression = "zstd"
			cfg.TCPOutZstdFrameSize = 1 << 20
			h := ConstructHostMTCP("example", cfg, conf.Host{Name: "localhost", MTCP: 10, Stream: mode == "stream-zstd", StreamOptions: conf.StreamOptions{Zstd: true}}, zap.NewNop(), metrics.New())
			wires := make([]*countingStreamConn, 10)
			for i := range h.MTCPs {
				wires[i] = &countingStreamConn{}
				h.MTCPs[i].New(wires[i], cfg.TCPOutZstdFrameSize)
				if mode == "zstd-plaintext" {
					h.MTCPs[i].W.configureCompression(cfg)
				}
			}
			var send func([]*rec.RecBytes)
			flush := h.tryToFlushIfNecessary
			if mode == "zstd-plaintext" {
				index := 0
				send = func(batch []*rec.RecBytes) {
					h.sendBatch(batch, &h.MTCPs[index])
					index = (index + 1) % len(h.MTCPs)
				}
				defer func() {
					for i := range h.MTCPs {
						h.MTCPs[i].W.compression.encoder.Close()
					}
				}()
			} else {
				s := h.makeStreamSender((conf.StreamOptions{Zstd: true}).WithDefaults(cfg.TCPOutBufFlushPeriodSec))
				send = s.dispatch
				flush = func() {
					for i := range s.connections {
						s.flush(&s.connections[i])
					}
				}
				defer func() {
					for i := range s.connections {
						if s.connections[i].codec != nil {
							s.connections[i].codec.close()
						}
					}
				}()
			}
			cycle := func(batch []*rec.RecBytes) {
				h.pushBulk(batch)
				chunk := h.q.pop()
				send(chunk.recs[:chunk.n])
				h.q.recycle(chunk)
			}
			for range 2 {
				for start := 0; start < len(rs); start += queueChunkLen {
					cycle(rs[start:min(start+queueChunkLen, len(rs))])
				}
				flush()
			}
			for _, w := range wires {
				w.bytes = 0
			}
			b.ReportAllocs()
			b.ResetTimer()
			before := outputCPUTime(b)
			count, start := 0, 0
			for range b.N {
				end := min(start+queueChunkLen, len(rs))
				cycle(rs[start:end])
				count += end - start
				start = end
				if start == len(rs) {
					start = 0
				}
			}
			flush()
			cpu := outputCPUTime(b) - before
			b.StopTimer()
			var written int64
			for _, w := range wires {
				written += w.bytes
			}
			b.ReportMetric(float64(cpu)/float64(count), "cpu-ns/record")
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(count), "ns/record")
			b.ReportMetric(float64(written)/float64(count), "wire-B/record")
		})
	}
}

func outputCPUTime(b testing.TB) int64 {
	b.Helper()
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		b.Fatal(err)
	}
	return (int64(usage.Utime.Sec)+int64(usage.Stime.Sec))*1e9 + (int64(usage.Utime.Usec)+int64(usage.Stime.Usec))*1e3
}
