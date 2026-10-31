package target

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"compress/gzip"
	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/metrics"
	"github.com/bookingcom/nanotube/pkg/rec"
	"github.com/klauspost/compress/zstd"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.uber.org/zap"
)

func TestZstdBufferReplay(t *testing.T) {
	cfg := conf.MakeDefault()
	cfg.TCPOutCompression = "zstd"
	cfg.TCPOutZstdFrameSize = 256
	b := newRecordBuffer(256)
	b.configureCompression(cfg)
	plain := bytes.Repeat([]byte("example.count 1.25 1700000000\n"), 100)
	b.appendRecord(plain)
	fault := &faultTCPConn{prefix: 11, err: io.ErrClosedPipe}
	result := b.flush(fault)
	if result.err == nil || result.written != 11 || result.uncertainRecords != 1 || b.Buffered() != len(plain) {
		t.Fatal("failed write lost pending plaintext")
	}
	var wire bytes.Buffer
	result = b.flush(&wire)
	if result.err != nil || result.written != wire.Len() || result.bytes != len(plain) || result.frames < 2 || b.Buffered() != 0 {
		t.Fatal("bad replay accounting")
	}
	decoder, err := zstd.NewReader(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer decoder.Close()
	decoded, err := decoder.DecodeAll(wire.Bytes(), nil)
	if err != nil || !bytes.Equal(decoded, plain) {
		t.Fatal("replay did not start with independent frames", err)
	}
	// After warmup, no frame allocation is needed, including encoder workspaces.
	cycle := func() {
		b.appendRecord(plain)
		if r := b.flush(io.Discard); r.err != nil {
			panic(r.err)
		}
	}
	cycle()
	if allocs := testing.AllocsPerRun(100, cycle); allocs != 0 {
		t.Fatalf("steady-state allocations: %v", allocs)
	}
}

func TestZstdTargetFlushAndReconnect(t *testing.T) {
	for _, multi := range []bool{false, true} {
		t.Run(fmt.Sprint(multi), func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			received := make(chan []byte, 2)
			go func() {
				for range 2 {
					c, e := listener.Accept()
					if e != nil {
						return
					}
					_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
					wire, e := io.ReadAll(c)
					_ = c.Close()
					if e != nil {
						return
					}
					received <- wire
				}
			}()
			cfg := conf.MakeDefault()
			cfg.TCPInitialConnCheck = false
			cfg.TCPOutCompression = "zstd"
			cfg.TCPOutZstdFrameSize = 1 << 20
			cfg.TCPOutBufFlushPeriodSec = 0
			hc := conf.Host{Name: "127.0.0.1", Port: uint16(listener.Addr().(*net.TCPAddr).Port), MTCP: 2}
			ms := metrics.New()
			rs := testRecs(100)
			plain := serializeOutput(rs)
			var stage func()
			var refresh func()
			var target Target
			var frames prometheus.Counter
			var written prometheus.Counter
			var plaintext prometheus.Collector
			labels := prometheus.Labels{"cluster": "example", "upstream_host": "127.0.0.1"}
			if multi {
				h := ConstructHostMTCP("example", cfg, hc, zap.NewNop(), ms)
				target = h
				stage = func() {
					h.q.push(rs)
					chunk := h.q.pop()
					h.sendBatch(chunk.recs[:chunk.n], &h.MTCPs[0])
					h.q.recycle(chunk)
					h.tryToFlushIfNecessary()
				}
				refresh = func() {
					h.conf.TCPOutConnectionRefreshPeriodSec = 1
					h.MTCPs[0].LastConnUse = time.Now().Add(-2 * time.Second)
					h.keepConnectionFresh(&h.MTCPs[0])
				}
			} else {
				h := NewHostTCP("example", cfg, hc, zap.NewNop(), ms)
				target = h
				stage = func() {
					h.q.push(rs)
					chunk := h.q.pop()
					h.sendBatch(chunk.recs[:chunk.n])
					h.q.recycle(chunk)
					h.tryToFlushIfNecessary()
				}
				refresh = func() {
					h.conf.TCPOutConnectionRefreshPeriodSec = 1
					h.Conn.LastConnUse = time.Now().Add(-2 * time.Second)
					h.keepConnectionFresh()
				}
			}
			frames = ms.TCP.ZstdFrames.With(labels)
			written = ms.TCP.SocketWrittenBytes.With(labels)
			plaintext = ms.TCP.PlaintextBytes.With(labels)
			stage()
			refresh()
			stage()
			target.Stop()
			var wg sync.WaitGroup
			wg.Add(1)
			go target.Stream(&wg)
			wg.Wait()
			decoder, e := zstd.NewReader(nil)
			if e != nil {
				t.Fatal(e)
			}
			defer decoder.Close()
			var wireBytes int
			for range 2 {
				select {
				case wire := <-received:
					got, e := decoder.DecodeAll(wire, nil)
					if e != nil || !bytes.Equal(got, plain) {
						t.Fatalf("reconnected output differs: %v", e)
					}
					wireBytes += len(wire)
				case <-time.After(6 * time.Second):
					t.Fatal("reconnect did not complete")
				}
			}
			if testutil.ToFloat64(frames) != 2 || testutil.ToFloat64(written) != float64(wireBytes) || testutil.ToFloat64(plaintext) != float64(2*len(plain)) {
				t.Fatal("wire/plaintext/frame metrics differ")
			}
		})
	}
}

// ZSTD_SAMPLE opts into the private gzip fixture; contents are never printed.
func BenchmarkZstdSampleEncode(b *testing.B) {
	path := os.Getenv("ZSTD_SAMPLE")
	if path == "" {
		b.Skip("set ZSTD_SAMPLE to a local gzip sample")
	}
	f, err := os.Open(path)
	if err != nil {
		b.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		b.Fatal(err)
	}
	defer gz.Close()
	plain, err := io.ReadAll(gz)
	if err != nil {
		b.Fatal(err)
	}
	for _, level := range []zstd.EncoderLevel{zstd.SpeedFastest, zstd.SpeedDefault} {
		for _, size := range []int{256 << 10, 1 << 20, 4 << 20} {
			b.Run(fmt.Sprintf("%s/%d", level, size), func(b *testing.B) {
				encoder, e := zstd.NewWriter(nil, zstd.WithEncoderLevel(level), zstd.WithEncoderConcurrency(1))
				if e != nil {
					b.Fatal(e)
				}
				defer encoder.Close()
				wire := make([]byte, 0, encoder.MaxEncodedSize(size))
				decoder, e := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1))
				if e != nil {
					b.Fatal(e)
				}
				defer decoder.Close()
				var decoded []byte
				var total int64
				for i := 0; i < len(plain); i += size {
					wire = encoder.EncodeAll(plain[i:min(i+size, len(plain))], wire[:0])
					total += int64(len(wire))
					decoded, e = decoder.DecodeAll(wire, decoded[:0])
					if e != nil || !bytes.Equal(decoded, plain[i:min(i+size, len(plain))]) {
						b.Fatal("sample frame byte parity failed")
					}
				}
				b.SetBytes(int64(len(plain)))
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					for j := 0; j < len(plain); j += size {
						wire = encoder.EncodeAll(plain[j:min(j+size, len(plain))], wire[:0])
					}
				}
				b.ReportMetric(float64(len(plain))/float64(total), "ratio")
				b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/float64(len(plain)), "cores/GBps")
			})
		}
	}
}

func BenchmarkZstdTargetWrite(b *testing.B) {
	for _, compression := range []string{"none", "zstd"} {
		b.Run(compression, func(b *testing.B) {
			c := conf.MakeDefault()
			c.TCPOutCompression = compression
			c.TCPInitialConnCheck = false
			c.TCPOutZstdFrameSize = 1 << 20
			h := ConstructHostMTCP("example", c, conf.Host{Name: "127.0.0.1", MTCP: 1}, zap.NewNop(), metrics.New())
			h.MTCPs[0].New(discardTCPConn{}, h.conf.TCPOutBufSize)
			h.MTCPs[0].W.configureCompression(*h.conf)
			rs := testRecs(256)
			if path := os.Getenv("ZSTD_SAMPLE"); path != "" {
				f, err := os.Open(path)
				if err != nil {
					b.Fatal(err)
				}
				defer f.Close()
				gz, err := gzip.NewReader(f)
				if err != nil {
					b.Fatal(err)
				}
				defer gz.Close()
				reader := bufio.NewReader(gz)
				rs = make([]*rec.RecBytes, 8192)
				for i := range rs {
					line, err := reader.ReadBytes('\n')
					if err != nil {
						b.Fatal(err)
					}
					rs[i], err = rec.ParseRec(bytes.TrimSuffix(line, []byte{'\n'}), false, false, time.Now, zap.NewNop())
					if err != nil {
						b.Fatal("sample parse failed")
					}
				}
			}
			offset := 0
			cycle := func() {
				h.q.push(rs[offset : offset+256])
				offset = (offset + 256) % len(rs)
				chunk := h.q.pop()
				h.sendBatch(chunk.recs[:chunk.n], &h.MTCPs[0])
				h.q.recycle(chunk)
			}
			for range 100 {
				cycle()
			}
			h.tryToFlushIfNecessary()
			b.ReportAllocs()
			b.SetBytes(int64(len(serializeOutput(rs)) * 256 / len(rs)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				cycle()
			}
			b.StopTimer()
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*256), "ns/record")
			h.tryToFlushIfNecessary()
		})
	}
}

func TestZstdIgnoredByGRPCTarget(t *testing.T) {
	cfg := conf.MakeDefault()
	cfg.TCPInitialConnCheck = false
	cfg.TCPOutCompression = "zstd"
	cfg.TCPOutBufSize = 4096
	target := NewHostGRPC("example", cfg, conf.Host{Name: "127.0.0.1", GRPC: true, Compression: "zstd"}, zap.NewNop(), metrics.New())
	if target.conf.TCPOutCompression != "" || target.conf.TCPOutBufSize != 4096 || target.q.output != nil {
		t.Fatal("gRPC inherited TCP compression or output accounting")
	}
}
