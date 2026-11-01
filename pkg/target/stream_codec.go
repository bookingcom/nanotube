package target

import (
	"encoding/binary"

	"github.com/bookingcom/nanotube/pkg/conf"
	codec "github.com/bookingcom/nanotube/pkg/streamcodec"
)

type carbonStreamCodec struct {
	encoder *codec.HashedEncoder
	marker  []byte
	wire    []byte
}

func newStreamCodec(o conf.StreamOptions) (streamCodec, error) {
	flags := byte(0)
	if o.Zstd {
		flags = codec.FlagZstd
	}
	e, err := codec.NewHashedEncoder(codec.Options{Flags: flags, FrameSize: o.MaxBytes, MaxRows: o.MaxRows, MaxNames: o.MaxNames})
	if err != nil {
		return nil, err
	}
	marker, err := codec.AppendPreamble(nil, flags)
	if err != nil {
		e.Close()
		return nil, err
	}
	return &carbonStreamCodec{encoder: e, marker: marker}, nil
}
func (c *carbonStreamCodec) preamble() []byte { return c.marker }
func (c *carbonStreamCodec) close()           { c.encoder.Close() }
func (c *carbonStreamCodec) encode(points []streamPoint) ([]byte, int, streamFrameStats, error) {
	n := 0
	stats := streamFrameStats{}
	for _, p := range points {
		defined, reset, addErr := c.encoder.AddHashed(p.name, p.hash, p.value, p.timestamp)
		if addErr != nil {
			if n == 0 {
				return nil, 0, streamFrameStats{}, addErr
			}
			break
		}
		if defined {
			stats.names++
		}
		if reset {
			stats.resets++
		}
		n++
		if c.encoder.Ready() {
			break
		}
	}
	wire, err := c.encoder.Flush(c.wire[:0])
	c.wire = wire
	stats.records = n
	for rest := wire; len(rest) >= codec.HeaderSize; {
		stats.frames++
		rest = rest[codec.HeaderSize+int(binary.BigEndian.Uint32(rest[4:8])):]
	}
	return wire, n, stats, err
}
