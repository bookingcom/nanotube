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

func newStreamCodec(o conf.StreamOptions, tables ...*codec.NameTable) (streamCodec, error) {
	var table *codec.NameTable
	if len(tables) > 0 {
		table = tables[0]
	}
	flags := byte(0)
	if o.Zstd {
		flags = codec.FlagZstd
	}
	opts := codec.Options{Flags: flags, FrameSize: o.MaxBytes, MaxRows: o.MaxRows, MaxNames: o.MaxNames, DictionaryBytes: o.DictionaryBytes, PeerDictionaryBytes: o.PeerDictionaryBytes}
	var e *codec.Encoder
	var err error
	if table == nil {
		e, err = codec.NewEncoder(opts)
	} else {
		e, err = codec.NewSharedEncoder(opts, table)
	}
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
	beforeUnshared := c.encoder.UnsharedTotal()
	stats := streamFrameStats{}
	for _, p := range points {
		defined, reset, addErr := c.encoder.AddGlobal(p.name, p.hash, p.global, p.value, p.timestamp)
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
			switch c.encoder.LastResetReason() {
			case codec.ResetPeerBytes:
				stats.peerResets++
			case codec.ResetLocalBytes:
				stats.localResets++
			case codec.ResetNames:
				stats.nameResets++
			}
		}
		n++
		if c.encoder.Ready() {
			break
		}
	}
	wire, err := c.encoder.Flush(c.wire[:0])
	c.wire = wire
	stats.records = n
	stats.unshared = int(c.encoder.UnsharedTotal() - beforeUnshared)
	for rest := wire; len(rest) >= codec.HeaderSize; {
		stats.frames++
		rest = rest[codec.HeaderSize+int(binary.BigEndian.Uint32(rest[4:8])):]
	}
	return wire, n, stats, err
}

func (c *carbonStreamCodec) retention() (int, int, int) {
	return c.encoder.NameCount(), c.encoder.RetainedBytes(), c.encoder.MapBytes()
}
