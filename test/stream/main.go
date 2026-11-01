// Command stream sends synthetic points through the real target implementation.
// e2e.py checks its expected JSON against a separate go-carbon process.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/metrics"
	"github.com/bookingcom/nanotube/pkg/rec"
	"github.com/bookingcom/nanotube/pkg/target"
	"go.uber.org/zap"
)

type expectedPoint struct {
	Name      string `json:"name"`
	Timestamp int64  `json:"timestamp"`
	Bits      string `json:"bits"`
}

func main() {
	port := flag.Uint("port", 2003, "localhost stream listener port")
	multi := flag.Int("mtcp", 1, "number of target connections")
	compressed := flag.Bool("zstd", false, "compress stream frames")
	flag.Parse()
	cfg := conf.MakeDefault()
	cfg.HostQueueSize = 10000
	cfg.TCPInitialConnCheck = false
	cfg.TCPOutConnectionRefreshPeriodSec = 1
	cfg.TargetConnectionJitter = false
	h := target.NewHost("example", cfg, conf.Host{Name: "127.0.0.1", Port: uint16(*port), MTCP: *multi, Stream: true, StreamOptions: conf.StreamOptions{MaxRows: 32, MaxBytes: 4096, FlushPeriodMS: 25, MaxNames: 1, Zstd: *compressed}}, zap.NewNop(), metrics.New())
	var wg sync.WaitGroup
	wg.Add(1)
	go h.Stream(&wg)
	var expected []expectedPoint
	for phase := range 3 {
		if phase > 0 {
			time.Sleep(1200 * time.Millisecond)
		}
		for i := range 640 {
			name := "example." + strings.Repeat("application.component.worker.", 7) + strconv.Itoa(i%11)
			value := float64(i*17) / 8
			if i%7 == 0 {
				value = math.Copysign(0, -1)
			}
			if i%19 == 0 {
				value = math.Inf(1)
			}
			ts := int64(1700000000 + phase*1000 + i)
			r := &rec.RecBytes{Path: []byte(name), Val: []byte(strconv.FormatFloat(value, 'g', -1, 64)), Time: []byte(strconv.FormatInt(ts, 10) + ".875")}
			h.Push(r)
			expected = append(expected, expectedPoint{name, ts, fmt.Sprintf("%016x", math.Float64bits(value))})
		}
		for _, invalid := range [][2]string{{"NaN", "1700000000"}, {"1", "NaN"}, {"bad", "1700000000"}} {
			h.Push(&rec.RecBytes{Path: []byte("example.rejected"), Val: []byte(invalid[0]), Time: []byte(invalid[1])})
		}
	}
	h.Stop()
	wg.Wait()
	if err := json.NewEncoder(os.Stdout).Encode(expected); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
