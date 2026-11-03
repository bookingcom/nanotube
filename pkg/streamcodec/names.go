package codec

import (
	"bytes"
	"hash/maphash"
	"math/bits"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"
)

const nameBlockBits = 4
const nameBlockSize = 1 << nameBlockBits
const nameKeyBits = 32
const nameGenerationLimit = 1 << (63 - nameKeyBits)

// Tokens cached on records must stay distinct across application reloads.
var nameGeneration atomic.Uint64 //nolint:gochecknoglobals // Process-wide epochs prevent cross-table aliasing.

func nextNameGeneration() uint64 {
	for {
		previous := nameGeneration.Load()
		if previous >= nameGenerationLimit-1 {
			return 0 // Never wrap a token onto a previously issued identity.
		}
		if nameGeneration.CompareAndSwap(previous, previous+1) {
			return previous + 1
		}
	}
}

type sharedName struct {
	route, a, b                    uint64
	generation                     uint64
	refs, touched, address, length uint32
}
type sharedBlock [nameBlockSize]sharedName

// NameTable gives names process-local, generation-checked IDs. It holds either
// full bytes (exact comparison), or two independently seeded 64-bit fingerprints.
// Fingerprint mode has a birthday bound n*(n-1)/2^129, assuming uniform hashes.
// No names or pointers are retained in that mode. A token is only a hint until
// acquired by a connection; expiry while queued is safe and re-resolves it.
type NameTable struct {
	shards     []nameShard
	seeds      [2]maphash.Seed
	full       bool
	lastSweep  atomic.Int64
	overhead   int
	generation uint64
}
type nameShard struct {
	mu                                sync.Mutex
	blocks                            []atomic.Pointer[sharedBlock]
	index                             []uint32
	limit, used, count, idle          int
	free                              uint32 // Intrusive list: retired entry.a stores the next id + 1.
	indexLimit                        int
	cursor                            uint32
	quantum                           int
	budget, bytes                     int
	pages                             [][]byte
	cells                             [13]uint32
	evictions                         uint64
	generation, generationExhaustions uint64
}
type NameStats struct {
	Bytes, Names, NameBytes, Capacity int
	Evictions                         uint64
	GenerationExhaustions             uint64
}

// NewNameTable bounds all table/index/arena capacities. Connection maps and
// predictors have their own byte shares. Zero bytes selects 16 MiB.
func NewNameTable(budget int, fullNames bool) (*NameTable, error) {
	if budget == 0 {
		budget = 16 << 20
	}
	if budget < 1024 {
		return nil, ErrLimit
	}
	shards := 1
	for shards < 32 && budget/shards >= 1<<20 {
		shards *= 2
	}
	t := &NameTable{shards: make([]nameShard, shards), seeds: [2]maphash.Seed{maphash.MakeSeed(), maphash.MakeSeed()}, full: fullNames}
	t.generation = nextNameGeneration()
	if t.generation == 0 {
		return nil, ErrLimit
	}
	t.overhead = int(unsafe.Sizeof(*t)) + shards*int(unsafe.Sizeof(nameShard{}))
	for i := range t.shards {
		s := &t.shards[i]
		s.generation = t.generation
		s.budget = (budget - t.overhead) / shards
		// Full-name arenas are lazy and consume the remaining half of the budget.
		allocation := s.budget
		s.quantum = min(1<<16, 1<<max(4, bits.Len(uint(s.budget/16))-1))
		if fullNames && s.budget >= 4096 {
			allocation /= 2
		}
		capacity := max(16, allocation/72/16*16)
		capacity = min(capacity, (1<<(nameKeyBits-5))-1)
		size := 16
		for size*3/4 < capacity {
			size *= 2
		}
		for size*4+((capacity+15)/16)*16*52+((capacity+15)/16)*8 > allocation {
			capacity -= 16
			if capacity < 16 {
				return nil, ErrLimit
			}
		}
		s.limit = capacity
		s.indexLimit = size
		s.index = make([]uint32, 16)
		s.blocks = make([]atomic.Pointer[sharedBlock], (capacity+15)/16)
		s.bytes = len(s.index)*4 + len(s.blocks)*8
	}
	return t, nil
}
func (t *NameTable) shard(hash uint64) *nameShard {
	return &t.shards[mixHash(hash)&uint64(len(t.shards)-1)]
}
func (s *nameShard) entry(id uint32) *sharedName {
	block := s.blocks[id>>nameBlockBits].Load()
	if block == nil {
		return nil
	}
	return &block[id&(nameBlockSize-1)]
}

// Valid requires no lock and does not acquire a reference. Only generation is
// read concurrently; held connection references prevent its slot being reused.
// Failed-resolution hints are valid for fan-out of that input record only.
func (t *NameTable) Valid(token uint64, hash uint64) bool {
	if token == 0 {
		return false
	}
	s := t.shard(hash)
	if uint32(token) == 0 {
		return token == t.unsharedToken()
	}
	if (uint32(token)-1)&31 != uint32(mixHash(hash)&uint64(len(t.shards)-1)) {
		return false
	}
	id := (uint32(token) - 1) >> 5
	if int(id) >= s.limit {
		return false
	}
	e := s.entry(id)
	return e != nil && atomic.LoadUint64(&e.generation) == token>>nameKeyBits
}

// A zero key is a table-specific failed-resolution hint. Publishers pass it to
// every replica so a full table is consulted only once for each input record.
// It never acquires a reference or enters a connection's global-to-wire index.
func (t *NameTable) unsharedToken() uint64 { return t.generation << nameKeyBits }
func (t *NameTable) Resolve(name []byte, hash uint64) uint64 {
	a, b := uint64(0), uint64(0)
	if !t.full {
		a = maphash.Bytes(t.seeds[0], name)
		b = maphash.Bytes(t.seeds[1], name)
	}
	s := t.shard(hash)
	s.mu.Lock()
	defer s.mu.Unlock()
	mask := len(s.index) - 1
	empty := int(mixHash(hash>>5)) & mask
	for ; s.index[empty] != 0; empty = (empty + 1) & mask {
		id := s.index[empty] - 1
		e := s.entry(id)
		if e.route == hash && ((!t.full && e.a == a && e.b == b) || (t.full && bytes.Equal(s.name(e), name))) {
			if e.refs == 0 {
				e.touched = uint32(time.Now().Unix())
			}
			return atomic.LoadUint64(&e.generation)<<nameKeyBits | uint64((id<<5)|uint32(mixHash(hash)&uint64(len(t.shards)-1))) + 1
		}
	}
	// Name lookup is on every published record, not only new definitions.
	// Keep probes sparse: denser indexes save bytes but raise steady CPU.
	if (s.count+1)*8 > len(s.index) && len(s.index) < s.indexLimit {
		s.growIndex()
		mask = len(s.index) - 1
		empty = -1
	}
	if s.free == 0 && s.used >= s.limit {
		s.reclaim(64, true)
		empty = -1
	}
	var id uint32
	if s.free != 0 {
		id = s.free - 1
		s.free = uint32(s.entry(id).a)
	} else {
		if s.used >= s.limit {
			return t.unsharedToken()
		}
		id = uint32(s.used)
		if s.blocks[id>>nameBlockBits].Load() == nil {
			if 16*48 > s.available() {
				return t.unsharedToken()
			}
			s.blocks[id>>nameBlockBits].Store(new(sharedBlock))
			s.bytes += 16 * 48
		}
		s.used++
	}
	e := s.entry(id)
	if t.full {
		address, ok := s.allocate(name)
		if !ok {
			s.reclaim(64, true)
			address, ok = s.allocate(name)
			empty = -1
		}
		if !ok {
			s.freeEntry(id)
			return t.unsharedToken()
		}
		e.address = address
		e.length = uint32(len(name))
	}
	// Slots in the same epoch have distinct keys. Issue a new process-wide
	// epoch only when reusing a slot already issued in this shard's epoch.
	// Keep the previous generation on retired entries, with its high bit set.
	// Unlike one generation per insertion, this scales with reuse of the
	// hottest slot, rather than aggregate name churn across the whole table.
	if atomic.LoadUint64(&e.generation)&^predictorValid >= s.generation {
		if gen := nextNameGeneration(); gen != 0 {
			s.generation = gen
		} else {
			s.generationExhaustions++
			if e.length != 0 {
				s.releaseBytes(e.address, int(e.length))
				e.length = 0
			}
			s.freeEntry(id)
			return t.unsharedToken()
		}
	}
	e.route, e.a, e.b = hash, a, b
	e.refs = 0
	e.touched = uint32(time.Now().Unix())
	atomic.StoreUint64(&e.generation, s.generation)
	if empty < 0 {
		empty = int(mixHash(hash>>5)) & mask
		for s.index[empty] != 0 {
			empty = (empty + 1) & mask
		}
	}
	s.index[empty] = id + 1
	s.count++
	s.idle++
	return s.generation<<nameKeyBits | uint64((id<<5)|uint32(mixHash(hash)&uint64(len(t.shards)-1))) + 1
}
func (t *NameTable) Acquire(token uint64, hash uint64) bool {
	if uint32(token) == 0 || (uint32(token)-1)&31 != uint32(mixHash(hash)&uint64(len(t.shards)-1)) {
		return false
	}
	s := t.shard(hash)
	s.mu.Lock()
	defer s.mu.Unlock()
	id := (uint32(token) - 1) >> 5
	if int(id) >= s.limit {
		return false
	}
	e := s.entry(id)
	if e == nil || atomic.LoadUint64(&e.generation) != token>>nameKeyBits {
		return false
	}
	if e.refs == 0 {
		s.idle--
	}
	e.refs++
	return true
}
func (t *NameTable) Release(token uint64) {
	if token != 0 {
		t.ReleaseKey(uint32(token))
	}
}
func (t *NameTable) ReleaseKey(key uint32) {
	if key == 0 {
		return
	}
	key--
	s := &t.shards[key&31]
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.entry(key >> 5)
	if e == nil || e.refs == 0 {
		panic("unbalanced stream name release")
	}
	e.refs--
	if e.refs == 0 {
		s.idle++
		e.touched = uint32(time.Now().Unix())
	}
}

// Sweep reclaims unreferenced names after a minute; pressure may reclaim them
// immediately. Live connection references are never evicted.
func (t *NameTable) Sweep() {
	now := time.Now().Unix()
	last := t.lastSweep.Load()
	if now-last < 60 || !t.lastSweep.CompareAndSwap(last, now) {
		return
	}
	for i := range t.shards {
		s := &t.shards[i]
		// Release the lock between fixed-size chunks, including for large tables.
		s.mu.Lock()
		work := s.used
		s.mu.Unlock()
		for work > 0 {
			s.mu.Lock()
			if s.idle == 0 {
				s.mu.Unlock()
				break
			}
			n := min(work, 64)
			s.reclaim(n, false)
			s.mu.Unlock()
			work -= n
		}
	}
}
func (s *nameShard) reclaim(work int, pressure bool) {
	if s.idle == 0 {
		return
	}
	now := uint32(time.Now().Unix())
	for range min(work, s.used) {
		if int(s.cursor) >= s.used {
			s.cursor = 0
		}
		id := s.cursor
		s.cursor++
		e := s.entry(id)
		gen := atomic.LoadUint64(&e.generation)
		if gen == 0 || gen&predictorValid != 0 || e.refs != 0 || (!pressure && now-e.touched < 60) {
			continue
		}
		s.remove(id, e.route)
		if e.length != 0 {
			s.releaseBytes(e.address, int(e.length))
			e.length = 0
		}
		atomic.StoreUint64(&e.generation, gen|predictorValid)
		s.count--
		s.idle--
		s.freeEntry(id)
		s.evictions++
	}
}

// Keep enough budget for the full index even while its allocation is lazy.
// Full-name arenas cannot spend that reservation and later prevent index growth.
func (s *nameShard) available() int {
	return s.budget - s.bytes - (s.indexLimit-len(s.index))*4
}

func (s *nameShard) freeEntry(id uint32) {
	s.entry(id).a = uint64(s.free)
	s.free = id + 1
}

func (s *nameShard) growIndex() {
	old := s.index
	s.index = make([]uint32, min(len(old)*2, s.indexLimit))
	mask := len(s.index) - 1
	for _, key := range old {
		if key == 0 {
			continue
		}
		i := int(mixHash(s.entry(key-1).route>>5)) & mask
		for s.index[i] != 0 {
			i = (i + 1) & mask
		}
		s.index[i] = key
	}
	s.bytes += (len(s.index) - len(old)) * 4
}

// Backshift deletion keeps lookup chains intact without tombstones or a
// table-sized rebuild. At the bounded index load, expected work is constant.
func (s *nameShard) remove(id uint32, hash uint64) {
	mask := len(s.index) - 1
	hole := int(mixHash(hash>>5)) & mask
	for s.index[hole] != id+1 {
		hole = (hole + 1) & mask
	}
	for i := (hole + 1) & mask; s.index[i] != 0; i = (i + 1) & mask {
		next := s.index[i]
		home := int(mixHash(s.entry(next-1).route>>5)) & mask
		if (i-home)&mask >= (i-hole)&mask {
			s.index[hole] = next
			hole = i
		}
	}
	s.index[hole] = 0
}
func (t *NameTable) Stats() NameStats {
	r := NameStats{Bytes: t.overhead}
	for i := range t.shards {
		s := &t.shards[i]
		s.mu.Lock()
		r.Bytes += s.bytes
		r.Capacity += s.limit
		r.GenerationExhaustions += s.generationExhaustions
		r.Names += s.count
		r.Evictions += s.evictions
		for _, p := range s.pages {
			r.NameBytes += len(p)
		}
		s.mu.Unlock()
	}
	return r
}
func (s *nameShard) name(e *sharedName) []byte {
	p := e.address - 1
	return s.pages[p>>16][p&65535 : uint32(p&65535)+e.length]
}
func (s *nameShard) allocate(name []byte) (uint32, bool) {
	class := max(4, bits.Len(uint(len(name)-1)))
	if class > 16 {
		return 0, false
	}
	size := 1 << class
	c := class - 4
	if s.cells[c] == 0 {
		pageSize := max(size, s.quantum)
		extra := pageSize + (nextCapacity(len(s.pages)+1, cap(s.pages))-cap(s.pages))*24
		if extra > s.available() || len(s.pages) >= 1<<16 {
			return 0, false
		}
		if s.cells[c] == 0 {
			page := make([]byte, pageSize)
			base := uint32(len(s.pages)) << 16
			pageCapacity := nextCapacity(len(s.pages)+1, cap(s.pages))
			if pageCapacity > cap(s.pages) {
				pages := make([][]byte, len(s.pages), pageCapacity)
				copy(pages, s.pages)
				s.bytes += (pageCapacity - cap(s.pages)) * 24
				s.pages = pages
			}
			s.pages = append(s.pages, page)
			s.bytes += pageSize
			for offset := pageSize - size; offset >= 0; offset -= size {
				address := base + uint32(offset) + 1
				putCell(page[offset:], s.cells[c])
				s.cells[c] = address
			}
		}
	}
	address := s.cells[c]
	p := address - 1
	cell := s.pages[p>>16][p&65535:]
	s.cells[c] = getCell(cell)
	copy(cell, name)
	return address, true
}
func putCell(b []byte, v uint32) {
	b[0] = byte(v)
	b[1] = byte(v >> 8)
	b[2] = byte(v >> 16)
	b[3] = byte(v >> 24)
}
func getCell(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}
func (s *nameShard) releaseBytes(address uint32, n int) {
	class := max(4, bits.Len(uint(n-1))) - 4
	p := address - 1
	b := s.pages[p>>16][p&65535:]
	putCell(b, s.cells[class])
	s.cells[class] = address
}
