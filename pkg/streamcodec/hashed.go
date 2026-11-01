package codec

import "math"

// HashedEncoder accepts an existing name hash, avoiding a second full-name hash
// for dictionary hits. Names are still compared byte for byte; hash collisions
// form a chain of dictionary ids. Identical names must use the same hash.
// Like Encoder, it is connection-local and not concurrency safe.
//
// The copied Encoder remains the source of truth for definitions, resets and
// wire encoding. Only the known-name Add path bypasses its string-keyed map.
type HashedEncoder struct {
	encoder *Encoder
	ids     map[uint64]uint32
	next    []uint32 // collision links: id+1, with zero terminating a chain
}

func NewHashedEncoder(o Options) (*HashedEncoder, error) {
	e, err := NewEncoder(o)
	if err != nil {
		return nil, err
	}
	return &HashedEncoder{encoder: e, ids: make(map[uint64]uint32)}, nil
}

// AddHashed has AddObserved's wire behavior and counters, using the supplied
// hash only to narrow the exact-name lookup. New names still go through Add.
func (h *HashedEncoder) AddHashed(name []byte, hash uint64, value float64, ts int64) (defined, reset bool, err error) {
	e := h.encoder
	if id, ok := h.ids[hash]; ok {
		for {
			if e.names[id] == string(name) {
				// Mirror Encoder.Add's known-name path. There is no definition
				// or dictionary reset here; Flush remains the shared wire writer.
				const cost = 24
				if cost+32 > e.opts.MaxFrameSize {
					return false, false, ErrLimit
				}
				if len(e.rows) > 0 && (len(e.rows) >= e.opts.MaxRows || e.estimate+cost+32 > e.opts.MaxFrameSize) {
					if err := e.flushPending(); err != nil {
						return false, false, err
					}
				}
				e.rows = append(e.rows, row{id: id, ts: ts, bits: math.Float64bits(value)})
				e.estimate += cost
				return false, false, nil
			}
			link := h.next[id]
			if link == 0 {
				break
			}
			id = link - 1
		}
	}
	defined, reset, err = e.AddObserved(name, value, ts)
	if err != nil {
		return false, false, err
	}
	if reset {
		clear(h.ids)
		h.next = h.next[:0]
	}
	id := uint32(len(e.names) - 1)
	link := uint32(0)
	if previous, ok := h.ids[hash]; ok {
		link = previous + 1
	}
	h.next = append(h.next, link)
	h.ids[hash] = id
	return defined, reset, nil
}

func (h *HashedEncoder) Ready() bool                      { return h.encoder.Ready() }
func (h *HashedEncoder) Flush(dst []byte) ([]byte, error) { return h.encoder.Flush(dst) }
func (h *HashedEncoder) Close()                           { h.encoder.Close() }
func (h *HashedEncoder) NameCount() int                   { return h.encoder.NameCount() }

// Reset clears both lookup tables only after the encoder successfully resets.
func (h *HashedEncoder) Reset() error {
	if err := h.encoder.Reset(); err != nil {
		return err
	}
	clear(h.ids)
	h.next = h.next[:0]
	return nil
}
