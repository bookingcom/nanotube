package codec

import "github.com/klauspost/compress/zstd"

// DecoderPool bounds compressed-frame contexts across a listener. Idle sockets
// retain no context or window. A token belongs to each context, including while
// in use, so a reconnect burst cannot allocate one context per connection.
type DecoderPool struct {
	maxFrame int
	slots    chan *zstd.Decoder
}

func NewDecoderPool(maxFrame, concurrency int) *DecoderPool {
	if maxFrame == 0 {
		maxFrame = DefaultMaxFrameSize
	}
	p := &DecoderPool{maxFrame: maxFrame, slots: make(chan *zstd.Decoder, max(1, concurrency))}
	for range cap(p.slots) {
		p.slots <- nil
	}
	return p
}

// SetDecoderPool shares only contexts with identical frame/window ceilings.
// Call before decoding. Decoded buffers and dictionary state remain private.
func (d *Decoder) SetDecoderPool(p *DecoderPool) {
	if p != nil && p.maxFrame == d.opts.MaxFrameSize {
		d.pool = p
	}
}

func newFrameDecoder(maxFrame int) (*zstd.Decoder, error) {
	return zstd.NewReader(nil, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(uint64(maxFrame)), zstd.WithDecoderMaxWindow(uint64(max(1<<10, maxFrame))), zstd.WithDecodeAllCapLimit(true))
}

func (d *Decoder) decompress(src, dst []byte) ([]byte, error) {
	if d.pool == nil {
		if d.z == nil {
			var err error
			d.z, err = newFrameDecoder(d.opts.MaxFrameSize)
			if err != nil {
				return nil, err
			}
		}
		return d.z.DecodeAll(src, dst)
	}
	z := <-d.pool.slots
	defer func() { d.pool.slots <- z }()
	if z == nil {
		var err error
		z, err = newFrameDecoder(d.opts.MaxFrameSize)
		if err != nil {
			return nil, err
		}
	}
	return z.DecodeAll(src, dst)
}
