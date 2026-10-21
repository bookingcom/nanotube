package target

import (
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/metrics"
	"github.com/bookingcom/nanotube/pkg/rec"
	"go.uber.org/zap"
)

type discardTCPConn struct{}

func (discardTCPConn) Write(b []byte) (int, error)      { return len(b), nil }
func (discardTCPConn) Read([]byte) (int, error)         { return 0, io.EOF }
func (discardTCPConn) Close() error                     { return nil }
func (discardTCPConn) LocalAddr() net.Addr              { return &net.TCPAddr{} }
func (discardTCPConn) RemoteAddr() net.Addr             { return &net.TCPAddr{} }
func (discardTCPConn) SetDeadline(time.Time) error      { return nil }
func (discardTCPConn) SetReadDeadline(time.Time) error  { return nil }
func (discardTCPConn) SetWriteDeadline(time.Time) error { return nil }

func BenchmarkTCPOutput(b *testing.B) {
	for _, size := range []int{4096, 65536} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			cfg := conf.MakeDefault()
			cfg.TCPOutBufSize = size
			cfg.TCPInitialConnCheck = false
			cfg.TCPOutBufFlushPeriodSec = 0
			cfg.HostQueueSize = 1024
			h := ConstructHostMTCP("test", cfg, conf.Host{Name: "127.0.0.1", Port: 2003, MTCP: 1}, zap.NewNop(), metrics.New())
			h.MTCPs[0].New(discardTCPConn{}, size)
			r, err := rec.ParseRec([]byte("benchmark."+strings.Repeat("metric.", 24)+"count 1.25 1700000000"), false, false, time.Now, zap.NewNop())
			if err != nil {
				b.Fatal(err)
			}
			rs := make([]*rec.RecBytes, 256)
			for i := range rs {
				rs[i] = r
			}
			cycle := func() { h.q.push(rs); c := h.q.pop(); h.sendBatch(c.recs[:c.n], &h.MTCPs[0]); h.q.recycle(c) }
			cycle()
			h.tryToFlushIfNecessary()
			b.SetBytes(int64(len(r.Bytes()) * len(rs)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				cycle()
			}
			b.StopTimer()
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*len(rs)), "ns/record")
			h.tryToFlushIfNecessary()
		})
	}
}
