// Local persistent input load generator for input_reload_e2e.py.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	codec "github.com/bookingcom/nanotube/pkg/streamcodec"
	"github.com/klauspost/compress/zstd"
)

type statistics struct {
	Lane            int      `json:"lane"`
	Mode            string   `json:"mode"`
	Errors          []string `json:"errors"`
	MaxWriteSeconds float64  `json:"max_write_seconds"`
	WireBytes       int      `json:"wire_bytes"`
}

func main() {
	address := flag.String("address", "127.0.0.1:2003", "local input address")
	mode := flag.String("mode", "plaintext", "plaintext, zstd, zstd-flush, stream or mixed")
	count := flag.Int("points", 300000, "total unique points")
	lanes := flag.Int("lanes", 1, "persistent input connections")
	names := flag.Int("names", 128, "name pool")
	base := flag.Int64("base", 1700000000, "first timestamp")
	chunk := flag.Int("chunk", 512, "points per write")
	delay := flag.Duration("delay", 2*time.Millisecond, "pause between writes per lane")
	flag.Parse()
	var submitted atomic.Int64
	var startWG, doneWG sync.WaitGroup
	startWG.Add(*lanes)
	doneWG.Add(*lanes)
	barrier := make(chan struct{})
	results := make([]statistics, *lanes)
	for lane := range *lanes {
		go func() {
			defer doneWG.Done()
			kind := *mode
			if kind == "mixed" {
				kind = "plaintext"
				if lane%10 < 4 {
					kind = "stream"
				} else if lane%10 == 4 {
					kind = "zstd"
				}
			}
			stat := &results[lane]
			stat.Lane = lane
			stat.Mode = kind
			var conn net.Conn
			var stream *codec.Encoder
			var compressor *zstd.Encoder
			fresh := false
			connect := func() error {
				var err error
				conn, err = net.DialTimeout("tcp", *address, 5*time.Second)
				if err != nil {
					return err
				}
				if tcp, ok := conn.(*net.TCPConn); ok {
					_ = tcp.SetWriteBuffer(64 << 10)
				}
				fresh = true
				if kind == "stream" {
					stream, err = codec.NewEncoder(codec.Options{Flags: codec.FlagZstd, DictionaryBytes: 256 << 20, PeerDictionaryBytes: 256 << 20})
				}
				if kind == "zstd-flush" {
					compressor, err = zstd.NewWriter(conn, zstd.WithEncoderConcurrency(1))
				}
				return err
			}
			if kind == "zstd" {
				compressor, _ = zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1))
				defer compressor.Close()
			}
			if err := connect(); err != nil {
				panic(err)
			}
			first := true
			for i := lane * *count / *lanes; i < (lane+1)**count / *lanes; i += *chunk {
				end := min(i+*chunk, (lane+1)**count / *lanes)
				for {
					var wire []byte
					if kind == "stream" {
						if fresh {
							wire, _ = codec.AppendPreamble(wire, codec.FlagZstd)
						}
						for j := i; j < end; j++ {
							name := fmt.Sprintf("example.%smetric_%06d", strings.Repeat("application.component.worker.region.zone.", 5), j%*names)
							if err := stream.Add([]byte(name), float64(j%8192)/8, *base+int64(j / *names)); err != nil {
								panic(err)
							}
						}
						var err error
						wire, err = stream.Flush(wire)
						if err != nil {
							panic(err)
						}
					} else {
						var text strings.Builder
						for j := i; j < end; j++ {
							fmt.Fprintf(&text, "example.%smetric_%06d %.17g %d\n", strings.Repeat("application.component.worker.region.zone.", 5), j%*names, float64(j%8192)/8, *base+int64(j / *names))
						}
						wire = []byte(text.String())
						if kind == "zstd" {
							wire = compressor.EncodeAll(wire, nil)
						}
					}
					fresh = false
					_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
					now := time.Now()
					var err error
					if kind == "zstd-flush" {
						_, err = compressor.Write(wire)
						if err == nil {
							err = compressor.Flush()
						}
					} else {
						_, err = conn.Write(wire)
					}
					stat.MaxWriteSeconds = max(stat.MaxWriteSeconds, time.Since(now).Seconds())
					if err == nil {
						stat.WireBytes += len(wire)
						break
					}
					stat.Errors = append(stat.Errors, fmt.Sprintf("%s: %v", time.Now().Format(time.RFC3339Nano), err))
					_ = conn.Close()
					if stream != nil {
						stream.Close()
					}
					if kind == "zstd-flush" {
						_ = compressor.Close()
					}
					if err = connect(); err != nil {
						panic(err)
					}
				}
				submitted.Add(int64(end - i))
				if first {
					first = false
					startWG.Done()
					<-barrier
				}
				time.Sleep(*delay)
			}
			if stream != nil {
				stream.Close()
			}
			if kind == "zstd-flush" {
				_ = compressor.Close()
			}
			_ = conn.Close()
		}()
	}
	startWG.Wait()
	fmt.Println("ready")
	close(barrier)
	doneWG.Wait()
	if err := json.NewEncoder(os.Stdout).Encode(map[string]any{"submitted": submitted.Load(), "connections": results}); err != nil {
		panic(err)
	}
}
