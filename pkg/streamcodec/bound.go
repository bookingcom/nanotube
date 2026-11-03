package codec

import (
	"io"
	"unsafe"
)

// BoundName is an immutable name owned by a shared application index. It can be
// embedded as the first field of the application's identity, avoiding a second
// name header or per-connection interface. Bytes must never be modified.
type BoundName struct{ Text string }

func (n *BoundName) Bytes() []byte {
	return unsafe.Slice(unsafe.StringData(n.Text), len(n.Text))
}

// NameBinder pins one reference per wire definition. The boolean is opaque
// caller state (for example whether this reference pins a cached route plan).
// Neither it nor the reference changes until RESET or Close releases it.
type NameBinder interface {
	BindStreamName([]byte) (*BoundName, bool, error)
	ReleaseStreamName(*BoundName, bool)
}

// BoundVisit receives fully validated rows with their pinned name and state.
type BoundVisit func(*BoundName, bool, float64, int64)

const boundBlockBits = 7
const boundBlockSize = 1 << boundBlockBits

type boundEntry struct {
	ts   int64
	bits uint64
	name *BoundName
}

// Two bitmaps replace predictor padding and the repeated interface type word.
// 128 entries fit a 3200-byte Go allocation including the allocation header.
type boundBlock struct {
	entries [boundBlockSize]boundEntry
	valid   [boundBlockSize / 64]uint64
	cached  [boundBlockSize / 64]uint64
}

type boundEntries struct {
	blocks []*boundBlock
	n      int
}

func (b *boundEntries) at(id int) *boundEntry {
	return &b.blocks[id>>boundBlockBits].entries[id&(boundBlockSize-1)]
}

func (b *boundEntries) add(p point) {
	if b.n>>boundBlockBits == len(b.blocks) {
		if len(b.blocks) == cap(b.blocks) {
			blocks := make([]*boundBlock, len(b.blocks), nextCapacity(len(b.blocks)+1, cap(b.blocks)))
			copy(blocks, b.blocks)
			b.blocks = blocks
		}
		b.blocks = append(b.blocks, new(boundBlock))
	}
	*b.at(b.n) = boundEntry{ts: p.ts, bits: p.bits}
	if p.valid {
		b.setValid(b.n)
	}
	b.n++
}

func (b *boundEntries) last(id int) point {
	e := b.at(id)
	return point{ts: e.ts, bits: e.bits, valid: b.blocks[id>>boundBlockBits].valid[(id&(boundBlockSize-1))/64]&(uint64(1)<<(id&63)) != 0}
}

func (b *boundEntries) setValid(id int) {
	b.blocks[id>>boundBlockBits].valid[(id&(boundBlockSize-1))/64] |= uint64(1) << (id & 63)
}

func (b *boundEntries) cached(id int) bool {
	return b.blocks[id>>boundBlockBits].cached[(id&(boundBlockSize-1))/64]&(uint64(1)<<(id&63)) != 0
}

func (b *boundEntries) bind(id int, name *BoundName, cached bool) {
	b.at(id).name = name
	if cached {
		b.blocks[id>>boundBlockBits].cached[(id&(boundBlockSize-1))/64] |= uint64(1) << (id & 63)
	}
}

func (b *boundEntries) bytes() int { return len(b.blocks)*3200 + cap(b.blocks)*8 }

func (b *boundEntries) projectedBytes() int {
	n := max(len(b.blocks), (b.n+boundBlockSize)/boundBlockSize)
	// Match the explicit pointer-vector growth in add, including unused capacity.
	return n*3200 + nextCapacity(n, cap(b.blocks))*8
}

// SetNameBinder selects compact, paged slots. Configure a fresh decoder, before
// reading any frames. Use ReadBoundFrame/DecodeBound with this binding mode.
// The ordinary resolver/Visit API remains available for arbitrary caller slots.
func (d *Decoder) SetNameBinder(binder NameBinder) {
	if d.NameCount() != 0 || d.resolve != nil || d.binder != nil || binder == nil {
		panic("stream name binder must be set on a fresh decoder")
	}
	d.binder = binder
}

func (d *Decoder) ReadBoundFrame(r io.Reader, visit BoundVisit) (FrameInfo, error) {
	return d.readFrame(r, nil, visit)
}

func (d *Decoder) DecodeBound(frame []byte, visit BoundVisit) (FrameInfo, error) {
	return d.decode(frame, nil, visit)
}

func (d *Decoder) last(id int) point {
	if d.binder != nil {
		return d.bound.last(id)
	}
	return d.entries[id].last
}
