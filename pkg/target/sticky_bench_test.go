package target

import (
	"fmt"
	"strings"
	"syscall"
	"testing"

	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/rec"
)

// Routing, real queue admission, batching and dequeue accounting are included.
// Socket I/O and parsing are excluded to isolate CPU attributable to the mode.
func BenchmarkClusterCPU(b *testing.B) {
	for _, n := range []int{3, 10, 40} {
		for _, mode := range []string{conf.LB, conf.StickyCluster} {
			for _, batching := range []bool{false, true} {
				for _, cached := range []bool{true, false} {
					b.Run(fmt.Sprintf("%d/%s/batch=%t/cached=%t", n, mode, batching, cached), func(b *testing.B) {
						ports := make([]uint16, n)
						for i := range ports {
							ports[i] = uint16(2000 + i)
						}
						cl, ms := stickyTestCluster(b, ports, 1024)
						cl.Type = mode
						rs := testRecs(4096)
						for _, r := range rs {
							r.Path = []byte("benchmark." + strings.Repeat("metric.", 16) + string(r.Path))
							r.PathHash()
						}
						batch := NewBatch()
						// Prewarm the queue chunks and staging buffers.
						cycle := func(start int) {
							for j := 0; j < 256; j++ {
								r := rs[(start+j)%len(rs)]
								if !cached {
									*r = rec.RecBytes{Path: r.Path, Val: r.Val, Time: r.Time}
								}
								var err error
								if batching {
									err = cl.PushBytesBatch(r, ms, batch)
								} else {
									err = cl.PushBytes(r, ms)
								}
								if err != nil {
									b.Fatal(err)
								}
							}
							batch.Flush()
							for _, h := range cl.sticky.hosts {
								for queueLen(h.q) > 0 {
									c := h.q.pop()
									h.q.recycle(c)
								}
							}
						}
						cycle(0)
						b.ReportAllocs()
						var before, after syscall.Rusage
						if err := syscall.Getrusage(syscall.RUSAGE_SELF, &before); err != nil {
							b.Fatal(err)
						}
						b.ResetTimer()
						for i := 0; i < b.N; i++ {
							cycle(i * 256)
						}
						b.StopTimer()
						if err := syscall.Getrusage(syscall.RUSAGE_SELF, &after); err != nil {
							b.Fatal(err)
						}
						cpu := after.Utime.Nano() + after.Stime.Nano() - before.Utime.Nano() - before.Stime.Nano()
						b.ReportMetric(float64(cpu)/float64(b.N*256), "cpu-ns/record")
						b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*256), "ns/record")
					})
				}
			}
		}
	}
}
