package in

import (
	"bufio"
	"bytes"
	"fmt"
	"testing"

	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/metrics"
	codec "github.com/bookingcom/nanotube/pkg/streamcodec"
	"go.uber.org/zap"
)

// BenchmarkStreamInput includes admission, compact binding, frame accounting and
// cleanup. The warm case adds repeated resident-id frames on one connection.
func BenchmarkStreamInput(b *testing.B) {
	const names = 256
	for _, flags := range []byte{0, codec.FlagZstd} {
		label := "raw"
		if flags != 0 {
			label = "zstd"
		}
		for _, epochs := range []int{1, 32} {
			phase := "cold"
			if epochs > 1 {
				phase = "warm"
			}
			b.Run(label+"/"+phase, func(b *testing.B) {
				e, err := codec.NewEncoder(codec.Options{Flags: flags, MaxRows: names})
				if err != nil {
					b.Fatal(err)
				}
				defer e.Close()
				wire, _ := codec.AppendPreamble(nil, flags)
				for epoch := range epochs {
					for i := range names {
						if err := e.Add(fmt.Appendf(nil, "example.series.%d", i), float64(i), int64(epoch)); err != nil {
							b.Fatal(err)
						}
					}
					wire, err = e.Flush(wire)
					if err != nil {
						b.Fatal(err)
					}
				}
				cfg, ms, lg := conf.MakeDefault(), metrics.New(), zap.NewNop()
				term := make(chan struct{})
				var source bytes.Reader
				reader := bufio.NewReaderSize(&source, 4096)
				b.ReportAllocs()
				b.ResetTimer()
				for range b.N {
					source.Reset(wire)
					reader.Reset(&source)
					sink := &boundAccountingSink{}
					readStream(reader, term, nil, &cfg, ms, lg, sink)
					if sink.rows != names*epochs {
						b.Fatal("incomplete input")
					}
				}
				b.StopTimer()
				b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*names*epochs), "ns/point")
			})
		}
	}
}
