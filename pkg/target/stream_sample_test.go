package target

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"testing"
	"time"

	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/metrics"
	"github.com/bookingcom/nanotube/pkg/rec"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.uber.org/zap"
)

// Opt-in because samples are private and may be large. Only aggregate counts
// are reported; paths, names and raw records are never logged or committed.
func TestStreamSampleReport(t *testing.T) {
	path := os.Getenv("NANOTUBE_STREAM_SAMPLE")
	if path == "" {
		t.Skip("set NANOTUBE_STREAM_SAMPLE to a gzip plaintext sample")
	}
	type result struct {
		Mode              string  `json:"mode"`
		Connections       int     `json:"connections"`
		Records           int     `json:"records"`
		PlainBytes        int64   `json:"plain_bytes"`
		WireBytes         int64   `json:"wire_bytes"`
		BytesPerRecord    float64 `json:"bytes_per_record"`
		Ratio             float64 `json:"ratio"`
		SenderNSPerRecord float64 `json:"sender_ns_per_record"`
		Names             int64   `json:"names_defined"`
		Resets            int64   `json:"dictionary_resets"`
		ConnectionNames   []int   `json:"names_per_connection"`
		TruncatedBytes    int     `json:"truncated_tail_bytes"`
	}
	var results []result
	for _, n := range []int{1, 10} {
		for _, zstd := range []bool{false, true} {
			t.Run(fmt.Sprintf("connections=%d/zstd=%v", n, zstd), func(t *testing.T) {
				cfg := conf.MakeDefault()
				cfg.TCPInitialConnCheck = false
				cfg.HostQueueSize = 1024
				cfg.TCPOutConnectionRefreshPeriodSec = 0
				ms := metrics.New()
				h := ConstructHostMTCP("example", cfg, conf.Host{Name: "localhost", MTCP: n}, zap.NewNop(), ms)
				wires := make([]*countingStreamConn, n)
				for i := range h.MTCPs {
					wires[i] = &countingStreamConn{}
					h.MTCPs[i].New(wires[i], 65536)
				}
				s := h.makeStreamSender((conf.StreamOptions{Zstd: zstd}).WithDefaults(2))
				defer func() {
					for i := range s.connections {
						if s.connections[i].codec != nil {
							s.connections[i].codec.close()
						}
					}
				}()
				f, err := os.Open(path)
				if err != nil {
					t.Fatal("cannot open sample")
				}
				defer f.Close()
				gz, err := gzip.NewReader(f)
				if err != nil {
					t.Fatal("cannot read gzip sample")
				}
				defer gz.Close()
				scanner := bufio.NewScanner(gz)
				batch := make([]*rec.RecBytes, 0, queueChunkLen)
				var elapsed time.Duration
				r := result{Mode: "stream", Connections: n}
				scanner.Split(func(data []byte, atEOF bool) (int, []byte, error) {
					if atEOF && len(data) > 0 && bytes.IndexByte(data, '\n') < 0 {
						r.TruncatedBytes += len(data)
						return len(data), nil, nil
					}
					return bufio.ScanLines(data, atEOF)
				})
				if zstd {
					r.Mode = "stream-zstd"
				}
				cycle := func() {
					start := time.Now()
					s.q.push(batch)
					chunk := s.q.pop()
					s.dispatch(chunk.recs[:chunk.n])
					s.q.recycle(chunk)
					elapsed += time.Since(start)
					clear(batch)
					batch = batch[:0]
				}
				for scanner.Scan() {
					point, err := rec.ParseRec(scanner.Bytes(), false, false, time.Now, zap.NewNop())
					if err != nil {
						t.Fatal("invalid sample row")
					}
					batch = append(batch, point)
					r.Records++
					r.PlainBytes += int64(len(point.Path) + len(point.Val) + len(point.Time) + 3)
					if len(batch) == queueChunkLen {
						cycle()
					}
				}
				if err := scanner.Err(); err != nil && err != io.EOF {
					t.Fatal("cannot scan sample")
				}
				if len(batch) > 0 {
					cycle()
				}
				start := time.Now()
				for i := range s.connections {
					s.flush(&s.connections[i])
				}
				elapsed += time.Since(start)
				for _, w := range wires {
					r.WireBytes += w.bytes
				}
				for i := range s.connections {
					if c := s.connections[i].codec; c != nil {
						r.ConnectionNames = append(r.ConnectionNames, c.(*carbonStreamCodec).encoder.NameCount())
					} else {
						r.ConnectionNames = append(r.ConnectionNames, 0)
					}
				}
				m := ms.Stream.For(map[string]string{"cluster": "example", "upstream_host": "localhost"})
				if testutil.ToFloat64(m.Records) != float64(r.Records) || testutil.ToFloat64(m.Dropped) != 0 {
					t.Fatal("sample records were rejected or duplicated by encoder")
				}
				r.Names = int64(testutil.ToFloat64(m.Names))
				r.Resets = int64(testutil.ToFloat64(m.Resets))
				r.BytesPerRecord = float64(r.WireBytes) / float64(r.Records)
				r.Ratio = float64(r.PlainBytes) / float64(r.WireBytes)
				r.SenderNSPerRecord = float64(elapsed.Nanoseconds()) / float64(r.Records)
				results = append(results, r)
				t.Logf("records=%d wire_B/record=%.3f ratio=%.2fx sender_ns/record=%.1f names=%d resets=%d", r.Records, r.BytesPerRecord, r.Ratio, r.SenderNSPerRecord, r.Names, r.Resets)
			})
		}
	}
	if out := os.Getenv("NANOTUBE_STREAM_SAMPLE_RESULT"); out != "" {
		b, err := json.MarshalIndent(results, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(out, append(b, '\n'), 0600); err != nil {
			t.Fatal("cannot write aggregate sample results")
		}
	}
}
