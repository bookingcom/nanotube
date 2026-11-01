package target

import (
	"github.com/bookingcom/nanotube/pkg/conf"
	"testing"
)

func TestStreamIgnoresPlaintextCompression(t *testing.T) {
	for _, global := range []string{"", "none", "zstd"} {
		for _, host := range []string{"", "none", "zstd"} {
			for _, compressed := range []bool{false, true} {
				c := conf.MakeDefault()
				c.TCPOutCompression = global
				c.TCPOutBufSize = 4096
				h := conf.Host{Stream: true, Compression: host, StreamOptions: conf.StreamOptions{Zstd: compressed}}
				got := tcpCompressionConfig(c, h)
				if got.TCPOutCompression != "" || got.TCPOutBufSize != 4096 {
					t.Fatal("stream inherited plaintext compression")
				}
				b := newRecordBuffer(got.TCPOutBufSize)
				b.configureCompression(got)
				if b.compression != nil {
					t.Fatal("stream allocated an outer zstd encoder")
				}
				codec, err := newStreamCodec(h.StreamOptions.WithDefaults(1))
				if err != nil {
					t.Fatal(err)
				}
				preamble := codec.preamble()
				codec.close()
				if len(preamble) == 0 || preamble[0] != 0 {
					t.Fatal("stream must start with its own preamble")
				}
			}
		}
	}
}
