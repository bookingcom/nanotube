package target

import (
	"io"

	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/klauspost/compress/zstd"
)

// tcpCompressionConfig resolves the host override after cluster inheritance.
// gRPC and stream bypass the plaintext TCP output buffer. Stream compression
// is controlled only by stream_options.zstd, regardless of inherited defaults.
func tcpCompressionConfig(c conf.Main, h conf.Host) conf.Main {
	if h.GRPC || h.Stream {
		c.TCPOutCompression = ""
		return c
	}
	if h.Compression != "" {
		c.TCPOutCompression = h.Compression
	}
	if c.TCPOutCompression == "zstd" {
		if c.TCPOutZstdFrameSize == 0 {
			c.TCPOutZstdFrameSize = 1 << 20
		}
		c.TCPOutBufSize = c.TCPOutZstdFrameSize
	}
	return c
}

type zstdOutput struct {
	encoder   *zstd.Encoder
	wire      []byte
	frameSize int
}

func (b *recordBuffer) configureCompression(c conf.Main) {
	if c.TCPOutCompression != "zstd" || b.compression != nil {
		return
	}
	level := zstd.SpeedFastest
	if c.TCPOutZstdLevel == "default" {
		level = zstd.SpeedDefault
	}
	// These options are fixed and validated; construction cannot fail.
	encoder, _ := zstd.NewWriter(nil, zstd.WithEncoderLevel(level), zstd.WithEncoderConcurrency(1))
	size := c.TCPOutZstdFrameSize
	if size == 0 {
		size = 1 << 20
	}
	b.compression = &zstdOutput{encoder: encoder, wire: make([]byte, 0, encoder.MaxEncodedSize(size)), frameSize: size}
}

// flushCompressed preserves complete plaintext records until the entire buffer
// succeeds. A failed prefix is uncertain, as with plaintext TCP: retrying cannot
// guarantee exactly once without receiver acknowledgements. Every retry starts
// with a fresh, independently decodable frame.
func (b *recordBuffer) flushCompressed(w io.Writer) tcpWriteResult {
	z := b.compression
	result := tcpWriteResult{}
	for plain := b.data; len(plain) > 0; {
		size := min(len(plain), z.frameSize)
		z.wire = z.encoder.EncodeAll(plain[:size], z.wire[:0])
		n, err := w.Write(z.wire)
		if n < 0 || n > len(z.wire) {
			n, err = 0, io.ErrShortWrite
		}
		if n < len(z.wire) && err == nil {
			err = io.ErrShortWrite
		}
		result.written += n
		if err != nil {
			result.err = err
			if result.written > 0 {
				result.uncertainRecords = b.records
			}
			return result
		}
		result.frames++
		plain = plain[size:]
	}
	result.records, result.bytes = b.records, len(b.data)
	b.data, b.records = b.data[:0], 0
	return result
}
