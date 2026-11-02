package in

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/klauspost/compress/zstd"
)

// zstdInput preserves streaming Flush behavior, including frames without a
// content size. A frame-limited reader prevents decoder read-ahead into the
// next frame. Only the current compressed frame is journaled in an unlinked
// file; a handoff replays it, discarding the already returned plaintext bytes.
// Memory stays bounded even for a long-lived, continuously flushed frame.
type zstdInput struct {
	wire     *bufio.Reader
	decoder  *zstd.Decoder
	frame    *zstdFrame
	journal  *os.File
	returned uint64
	ended    bool
}

func newZstdInput(wire *bufio.Reader) (*zstdInput, error) {
	f, err := os.CreateTemp("", "nt-zstd-")
	if err != nil {
		return nil, err
	}
	_ = os.Remove(f.Name())
	d, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(16<<20), zstd.WithDecoderMaxWindow(16<<20))
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	z := &zstdInput{wire: wire, decoder: d, journal: f}
	z.frame = &zstdFrame{wire: wire, journal: f}
	if err = d.Reset(z.frame); err != nil {
		z.Close()
		return nil, err
	}
	return z, nil
}
func (z *zstdInput) Close() { z.decoder.Close(); _ = z.journal.Close() }
func (z *zstdInput) Read(b []byte) (int, error) {
	for {
		if z.ended {
			// Do not start a new frame at EOF: that would drop the previous frame's
			// replay checkpoint before a blocked read can be interrupted.
			if _, err := z.wire.Peek(1); err != nil {
				return 0, err
			}
			if err := z.journal.Truncate(0); err != nil {
				return 0, err
			}
			if _, err := z.journal.Seek(0, io.SeekStart); err != nil {
				return 0, err
			}
			z.returned = 0
			z.ended = false
			z.frame = &zstdFrame{wire: z.wire, journal: z.journal}
			if err := z.decoder.Reset(z.frame); err != nil {
				return 0, err
			}
		}
		n, err := z.decoder.Read(b)
		z.returned += uint64(n)
		if errors.Is(err, io.EOF) {
			z.ended = true
			if n > 0 {
				return n, nil
			}
			continue
		}
		return n, err
	}
}

// This reader exposes exactly one RFC 8878 frame, incrementally. Header and
// block bytes are journaled as they leave the wire, including interrupted
// partial headers/blocks. The standard decoder still validates all content.
type zstdFrame struct {
	wire     *bufio.Reader
	journal  *os.File
	pending  []byte
	left     uint64
	phase    int
	checksum bool
	last     bool
}

func (f *zstdFrame) take(n int) ([]byte, error) {
	b := make([]byte, n)
	k, err := io.ReadFull(f.wire, b)
	b = b[:k]
	if _, e := f.journal.Write(b); e != nil {
		return nil, e
	}
	return b, err
}
func (f *zstdFrame) Read(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}
	for len(f.pending) == 0 {
		switch f.phase {
		case 0:
			header, err := f.take(4)
			if err != nil {
				return 0, err
			}
			var h zstd.Header
			for {
				err = h.Decode(header)
				if err == nil {
					break
				}
				if !errors.Is(err, io.ErrUnexpectedEOF) || len(header) >= 18 {
					return 0, err
				}
				more, e := f.take(1)
				header = append(header, more...)
				if e != nil {
					return 0, e
				}
			}
			f.pending = header
			f.checksum = h.HasCheckSum
			f.phase = 1
			if h.Skippable {
				f.left = uint64(h.SkippableSize)
				f.phase = 2
				f.last = true
			}
		case 1:
			h, err := f.take(3)
			if err != nil {
				return 0, err
			}
			bits := uint32(h[0]) | uint32(h[1])<<8 | uint32(h[2])<<16
			typ := bits >> 1 & 3
			f.left = uint64(bits >> 3)
			f.last = bits&1 != 0
			if typ == 3 || f.left > 128<<10 {
				return 0, errors.New("invalid zstd block header")
			}
			if typ == 1 {
				f.left = 1
			}
			f.pending = h
			f.phase = 2
		case 2:
			if f.left > 0 {
				n, err := f.wire.Read(b[:min(uint64(len(b)), f.left)])
				if _, e := f.journal.Write(b[:n]); e != nil {
					return 0, e
				}
				f.left -= uint64(n)
				return n, err
			}
			if !f.last {
				f.phase = 1
				continue
			}
			f.phase = 4
			if f.checksum {
				f.phase = 3
			}
		case 3:
			h, err := f.take(4)
			if err != nil {
				return 0, err
			}
			f.pending = h
			f.phase = 4
		default:
			return 0, io.EOF
		}
	}
	n := copy(b, f.pending)
	f.pending = f.pending[n:]
	return n, nil
}

func writeBuffered(w io.Writer, c *inputConn, r *bufio.Reader) error {
	buffered, _ := r.Peek(r.Buffered())
	if _, err := w.Write(buffered); err != nil {
		return err
	}
	if c.prefix != nil {
		if _, err := io.Copy(w, c.prefix); err != nil {
			return err
		}
	}
	if a, ok := c.Conn.(*readAhead); ok {
		return a.pending(w)
	}
	return nil
}
func writeWirePrefix(w io.Writer, c *inputConn, r *bufio.Reader, partial []byte) error {
	if _, err := w.Write([]byte{0}); err != nil {
		return err
	}
	if _, err := w.Write(partial); err != nil {
		return err
	}
	return writeBuffered(w, c, r)
}
func writeInputPrefix(w io.Writer, c *inputConn, plain, wire *bufio.Reader, partial []byte, z *zstdInput) error {
	if z == nil {
		return writeWirePrefix(w, c, plain, partial)
	}
	buffered, _ := plain.Peek(plain.Buffered())
	var h [13]byte
	h[0] = 1
	binary.BigEndian.PutUint64(h[1:9], z.returned)
	binary.BigEndian.PutUint32(h[9:13], uint32(len(partial)+len(buffered)))
	if _, err := w.Write(h[:]); err != nil {
		return err
	}
	if _, err := w.Write(partial); err != nil {
		return err
	}
	if _, err := w.Write(buffered); err != nil {
		return err
	}
	stat, err := z.journal.Stat()
	if err != nil {
		return err
	}
	if _, err = io.Copy(w, io.NewSectionReader(z.journal, 0, stat.Size())); err != nil {
		return err
	}
	return writeBuffered(w, c, wire)
}

func inheritedZstd(r *bufio.Reader) (*bufio.Reader, *zstdInput, error) {
	var h [12]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return nil, nil, err
	}
	skip := binary.BigEndian.Uint64(h[:8])
	size := binary.BigEndian.Uint32(h[8:])
	if size > 2*bufio.MaxScanTokenSize {
		return nil, nil, errors.New("inherited plaintext tail exceeds bound")
	}
	tail := make([]byte, size)
	if _, err := io.ReadFull(r, tail); err != nil {
		return nil, nil, err
	}
	z, err := newZstdInput(r)
	if err != nil {
		return nil, nil, err
	}
	if skip > uint64(^uint64(0)>>1) {
		z.Close()
		return nil, nil, errors.New("invalid zstd replay offset")
	}
	if _, err = io.CopyN(io.Discard, z, int64(skip)); err != nil {
		z.Close()
		return nil, nil, fmt.Errorf("zstd replay: %w", err)
	}
	return bufio.NewReaderSize(io.MultiReader(bytes.NewReader(tail), z), bufio.MaxScanTokenSize), z, nil
}

func zstdReplayWork(z *zstdInput) uint64 {
	if z == nil {
		return 0
	}
	stat, err := z.journal.Stat()
	if err != nil {
		return z.returned
	}
	return z.returned + uint64(stat.Size())
}
