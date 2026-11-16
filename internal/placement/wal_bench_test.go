package placement

import (
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/bookingcom/nanotube/internal/v2wire"
	"github.com/bookingcom/nanotube/pkg/rec"
)

// BenchmarkWAL measures admission and durable ACK checkpoint cost together.
// It deliberately excludes the network and store and reports group latency.
func BenchmarkWAL(b *testing.B) {
	for _, size := range []int{1, 256, 1024} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			r, d := openSpool(b, b.TempDir(), 2048)
			defer d.Close()
			records := make([]*rec.RecBytes, size)
			for i := range records {
				records[i] = durableRecord()
			}
			frames := make([]v2wire.Frame, 0, size)
			latencies := make([]time.Duration, 0, b.N)
			before := d.syncs
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				start := time.Now()
				if e := r.PushBatch(records); e != nil {
					b.Fatal(e)
				}
				frames = append(frames[:0], d.queue...)
				if e := d.acceptAcks(frames); e != nil {
					b.Fatal(e)
				}
				latencies = append(latencies, time.Since(start))
			}
			b.StopTimer()
			elapsed := b.Elapsed().Seconds()
			sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
			total := float64(b.N * size)
			b.ReportMetric(total/elapsed, "records/s")
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/total, "ns/record")
			b.ReportMetric(float64(d.syncs-before)/elapsed, "fsyncs/s")
			b.ReportMetric(float64(latencies[(len(latencies)-1)*99/100].Microseconds()), "p99-group-us")
		})
	}
}
