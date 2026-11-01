package target

import (
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/metrics"
	"github.com/bookingcom/nanotube/pkg/rec"
	"go.uber.org/zap"
)

type countingStreamConn struct {
	discardTCPConn
	bytes int64
}

func (c *countingStreamConn) Write(b []byte) (int, error) {
	c.bytes += int64(len(b))
	return len(b), nil
}

// 1024 series sampled once per epoch, long shared prefixes, evolving counters
// and quantized gauges. Inputs are prepared outside the measured send loop.
func syntheticStreamRecords(b testing.TB) []*rec.RecBytes {
	b.Helper()
	var rs []*rec.RecBytes
	for epoch := range 64 {
		for i := range 1024 {
			name := fmt.Sprintf("example.application.workload.%03d.%sworker.%04d.value", i%16, strings.Repeat("component.subsystem.", 5+i%8), i)
			value := float64(epoch*17 + i%29)
			if i%2 == 1 {
				value = 25.125 + math.Sin(float64(epoch)/16)*float64(i%9)/8
			}
			r, err := rec.ParseRec(fmt.Appendf(nil, "%s %.16g %d", name, value, 1700000000+epoch), false, false, time.Now, zap.NewNop())
			if err != nil {
				b.Fatal(err)
			}
			rs = append(rs, r)
		}
	}
	return rs
}

func benchmarkStreamOutput(b *testing.B, records []*rec.RecBytes, connections int) {
	for _, mode := range []string{"plaintext", "stream", "stream-zstd"} {
		b.Run(mode, func(b *testing.B) {
			cfg := conf.MakeDefault()
			cfg.TCPInitialConnCheck = false
			cfg.TCPOutConnectionRefreshPeriodSec = 0
			cfg.HostQueueSize = 1024
			cfg.TCPOutBufSize = 65536
			h := ConstructHostMTCP("example", cfg, conf.Host{Name: "localhost", MTCP: connections}, zap.NewNop(), metrics.New())
			wires := make([]*countingStreamConn, connections)
			for i := range h.MTCPs {
				wires[i] = &countingStreamConn{}
				h.MTCPs[i].New(wires[i], cfg.TCPOutBufSize)
			}
			var send func([]*rec.RecBytes)
			var flush func()
			if mode == "plaintext" {
				index := 0
				send = func(rs []*rec.RecBytes) { h.sendBatch(rs, &h.MTCPs[index]); index = (index + 1) % connections }
				flush = h.tryToFlushIfNecessary
			} else {
				opts := (conf.StreamOptions{Zstd: mode == "stream-zstd"}).WithDefaults(cfg.TCPOutBufFlushPeriodSec)
				s := h.makeStreamSender(opts)
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
			cycle := func(rs []*rec.RecBytes) {
				h.q.push(rs)
				chunk := h.q.pop()
				send(chunk.recs[:chunk.n])
				h.q.recycle(chunk)
			}
			// Warm every name and all reusable frame/queue buffers, including
			// the final size of delta and XOR columns and the timestamp/value
			// transition when the fixture cycles back to its first epoch.
			for range 2 {
				for i := 0; i < len(records); i += queueChunkLen {
					cycle(records[i:min(i+queueChunkLen, len(records))])
				}
				flush()
			}
			for _, wire := range wires {
				wire.bytes = 0
			}
			b.ReportAllocs()
			b.ResetTimer()
			nrecords := 0
			for i := 0; i < b.N; i++ {
				start := (i * queueChunkLen) % len(records)
				batch := records[start:min(start+queueChunkLen, len(records))]
				cycle(batch)
				nrecords += len(batch)
			}
			flush()
			b.StopTimer()
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(nrecords), "ns/record")
			var written int64
			for _, wire := range wires {
				written += wire.bytes
			}
			b.ReportMetric(float64(written)/float64(nrecords), "wire-B/record")
		})
	}
}

func BenchmarkStreamOutput(b *testing.B) {
	records := syntheticStreamRecords(b)
	for _, n := range []int{1, 10} {
		b.Run(fmt.Sprintf("connections=%d", n), func(b *testing.B) { benchmarkStreamOutput(b, records, n) })
	}
}
