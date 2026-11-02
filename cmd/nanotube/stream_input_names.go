package main

import (
	"bytes"
	"hash/maphash"
	"sync"
	"unsafe"

	"github.com/bookingcom/nanotube/pkg/metrics"
	"github.com/bookingcom/nanotube/pkg/rec"
	"github.com/bookingcom/nanotube/pkg/rewrites"
	"github.com/bookingcom/nanotube/pkg/rules"
	codec "github.com/bookingcom/nanotube/pkg/streamcodec"
)

const streamNameShards = 64

// Small objects need no malloc header and their allocator classes have at most
// 8/16/32-byte spacing in these ranges. Keep normal 60..120-byte names and the
// compact entry/plan charges close to their allocation sizes. Larger objects use
// the existing conservative size-class/page estimate.
func streamInputCharge(n uint64) uint64 {
	if n <= 32 {
		return (n + 7) &^ 7
	}
	if n <= 256 {
		return (n + 15) &^ 15
	}
	if n <= 512 {
		return (n + 31) &^ 31
	}
	return allocationCharge(n)
}

// A 64-byte entry replaces the old inputName + streamRoute allocations. The
// string and every path are immutable: queued records may outlive all table refs.
// No arenas or entry objects are recycled. refs/routeRefs are shard-locked;
// a cached slot pins canonical, plans and flags, making Point lock-free.
type streamName struct {
	name       string
	plans      []streamPlan
	canonical  *streamName
	refs       uint32
	routeRefs  uint32
	nameClass  uint16 // Name allocation charge in eight-byte units (max wire name: 64 KiB).
	rewritten  bool
	invalid    bool
	routeBytes uint32
}

func (e *streamName) StreamName() []byte {
	// This read-only view avoids keeping both string and slice headers. The
	// string owns its allocation; output records retain it through this slice.
	return unsafe.Slice(unsafe.StringData(e.name), len(e.name))
}

type uncachedStreamName streamName

func (e *uncachedStreamName) StreamName() []byte { return (*streamName)(e).StreamName() }

type streamNameShard struct {
	mu    sync.Mutex
	index []*streamName
	count int
	_     [24]byte // Keep neighbouring shard locks on separate cache lines.
}

type streamNames struct {
	shards    [streamNameShards]streamNameShard
	seed      maphash.Seed
	rules     rules.Rules
	rewrites  rewrites.Rewrites
	normalize bool
	ms        *metrics.Prom
	fixed     int64
}

// Physical deltas are connection-local and published at frame boundaries.
// Cold names and releases do not each contend on three Prometheus gauges.
type streamNameChanges struct{ dictionary, routes, names int }

func newStreamNames(rs rules.Rules, rw rewrites.Rewrites, normalize bool, ms *metrics.Prom) *streamNames {
	t := &streamNames{seed: maphash.MakeSeed(), rules: rs, rewrites: rw, normalize: normalize, ms: ms}
	t.fixed = int64(allocationCharge(uint64(unsafe.Sizeof(*t))))
	for i := range t.shards {
		t.shards[i].index = make([]*streamName, 16)
		t.fixed += int64(streamInputCharge(16 * 8))
	}
	ms.StreamInputDictionaryBytes.Add(float64(t.fixed))
	ms.StreamInputSharedBytes.Add(float64(t.fixed))
	return t
}

func (t *streamNames) shard(hash uint64) *streamNameShard {
	return &t.shards[hash&(streamNameShards-1)]
}

func (t *streamNames) position(s *streamNameShard, name []byte) int {
	return t.positionHash(s, name, maphash.Bytes(t.seed, name))
}

func (*streamNames) positionHash(s *streamNameShard, name []byte, hash uint64) int {
	i := int(hash>>6) & (len(s.index) - 1)
	for s.index[i] != nil && s.index[i].name != string(name) {
		i = (i + 1) & (len(s.index) - 1)
	}
	return i
}

func (t *streamNames) resize(s *streamNameShard, n int, changes *streamNameChanges) {
	old := s.index
	s.index = make([]*streamName, n)
	for _, e := range old {
		if e != nil {
			s.index[t.position(s, e.StreamName())] = e
		}
	}
	delta := int(streamInputCharge(uint64(n*8))) - int(streamInputCharge(uint64(len(old)*8)))
	changes.dictionary += delta
}

// get acquires only the immutable raw name. The caller already holds its shard.
func (t *streamNames) get(s *streamNameShard, name []byte, hash uint64, changes *streamNameChanges) *streamName {
	i := t.positionHash(s, name, hash)
	if e := s.index[i]; e != nil {
		if e.refs == ^uint32(0) {
			return nil
		}
		e.refs++
		return e
	}
	if (s.count+1)*4 > len(s.index)*3 {
		t.resize(s, len(s.index)*2, changes)
		i = t.positionHash(s, name, hash)
	}
	e := &streamName{name: string(name), refs: 1, nameClass: uint16(streamInputCharge(uint64(len(name))) / 8)}
	s.index[i] = e
	s.count++
	cost := nameStorage(e)
	changes.dictionary += cost
	changes.names++
	return e
}

// Logical charges include index growth. At 1/4 occupancy the retained index is
// at most 32 bytes/name beyond its fixed allocation; 64 covers resize overlap.
func nameStorage(e *streamName) int {
	return int(streamInputCharge(uint64(unsafe.Sizeof(*e)))) + int(e.nameClass)*8
}
func nameAllowance(e *streamName) int { return nameStorage(e) + 64 }
func nameSizeAllowance(n int) int {
	return int(streamInputCharge(uint64(unsafe.Sizeof(streamName{})))+streamInputCharge(uint64(n))) + 64
}

func (e *streamName) routeStorage() int {
	if e.canonical != nil {
		return e.canonical.routeStorage()
	}
	return int(e.routeBytes)
}

func (e *streamName) allowance() (int, int) {
	dict := nameAllowance(e)
	if e.canonical != nil {
		dict += nameAllowance(e.canonical)
	}
	return dict, e.routeStorage()
}

// acquire runs only on dictionary definitions. Plans are built under their
// canonical name's shard lock; DATA rows never touch a table or a lock.
func (t *streamNames) acquire(name []byte, routeLimit, dictLimit int, changes *streamNameChanges) (*streamName, bool, int, int, error) {
	if nameSizeAllowance(len(name)) > dictLimit {
		return nil, false, 0, 0, codec.ErrLimit
	}
	hash := maphash.Bytes(t.seed, name)
	s := t.shard(hash)
	s.mu.Lock()
	e := t.get(s, name, hash, changes)
	if e == nil {
		s.mu.Unlock()
		return nil, false, 0, 0, codec.ErrLimit
	}
	if e.routeRefs > 0 {
		dict, route := e.allowance()
		cached := route <= routeLimit
		if cached {
			if dict > dictLimit {
				e.refs--
				s.mu.Unlock()
				return nil, false, 0, 0, codec.ErrLimit
			}
			e.routeRefs++
		} else {
			dict, route = nameAllowance(e), 0
		}
		s.mu.Unlock()
		return e, cached, dict, route, nil
	}
	path, err := rec.NormalizedPath(e.StreamName(), t.normalize)
	if err != nil || bytes.Equal(path, e.StreamName()) {
		t.buildNormalized(e, path, err != nil)
		cost := e.routeStorage()
		cached := cost <= routeLimit
		if cached {
			e.routeRefs = 1
			changes.routes += cost
		} else {
			e.plans = nil
			cost = 0
		}
		s.mu.Unlock()
		return e, cached, nameAllowance(e), cost, nil
	}
	// Canonical names can hash to the same shard. Never hold two shard locks.
	// Pin the raw name while acquiring its canonical plan, then release a
	// redundant canonical reference if another alias binder won the race.
	s.mu.Unlock()
	if nameAllowance(e)+nameSizeAllowance(len(path)) > dictLimit {
		t.release(e, false, changes)
		return nil, false, 0, 0, codec.ErrLimit
	}
	canonical, cached, _, route, acquireErr := t.acquire(path, routeLimit, dictLimit-nameAllowance(e), changes)
	if acquireErr != nil {
		t.release(e, false, changes)
		return nil, false, 0, 0, acquireErr
	}
	s.mu.Lock()
	if !cached {
		s.mu.Unlock()
		t.release(canonical, false, changes)
		return e, false, nameAllowance(e), 0, nil
	}
	if e.routeRefs == 0 {
		e.canonical = canonical
		e.routeRefs = 1
		s.mu.Unlock()
		return e, true, nameAllowance(e) + nameAllowance(canonical), route, nil
	}
	e.routeRefs++
	dict, route := e.allowance()
	s.mu.Unlock()
	t.release(canonical, true, changes)
	return e, true, dict, route, nil
}

// Last-name release removes the index entry; last-plan release drops its arrays.
// Uncached slots never read mutable plan fields. Queued path bytes are immutable
// Go allocations and remain valid independently of either reference count.
func (t *streamNames) release(e *streamName, cached bool, changes *streamNameChanges) (int, int) {
	hash := maphash.String(t.seed, e.name)
	s := t.shard(hash)
	s.mu.Lock()
	dict, route := nameAllowance(e), 0
	var canonical *streamName
	if cached {
		dict, route = e.allowance()
		e.routeRefs--
		if e.routeRefs == 0 {
			canonical = e.canonical
			if canonical == nil {
				changes.routes -= route
			}
			e.canonical, e.plans = nil, nil
		}
	}
	e.refs--
	if e.refs == 0 {
		t.remove(s, e, hash, changes)
		changes.dictionary -= nameStorage(e)
		changes.names--
	}
	s.mu.Unlock()
	if canonical != nil {
		t.release(canonical, true, changes)
	}
	return dict, route
}

func (t *streamNames) remove(s *streamNameShard, e *streamName, hash uint64, changes *streamNameChanges) {
	mask := len(s.index) - 1
	hole := t.positionHash(s, e.StreamName(), hash)
	for i := (hole + 1) & mask; s.index[i] != nil; i = (i + 1) & mask {
		home := int(maphash.String(t.seed, s.index[i].name)>>6) & mask
		if (i-home)&mask >= (i-hole)&mask {
			s.index[hole] = s.index[i]
			hole = i
		}
	}
	s.index[hole] = nil
	s.count--
	if len(s.index) > 16 && s.count*4 <= len(s.index) {
		t.resize(s, len(s.index)/2, changes)
	}
}

func (t *streamNames) build(e *streamName, name []byte) {
	path, err := rec.NormalizedPath(name, t.normalize)
	t.buildNormalized(e, path, err != nil)
}

func (t *streamNames) buildNormalized(e *streamName, path []byte, invalid bool) {
	t.ms.StreamInputRouteMisses.Inc()
	e.invalid, e.rewritten, e.routeBytes = invalid, false, 0
	if invalid {
		return
	}
	// Plans depend only on paths. Leave numbers to Point, and avoid transient
	// record/slice allocations entirely when no rewrite rules are configured.
	if t.rewrites.Empty() {
		input := rec.RecBytes{Path: path}
		e.plans = append(e.plans, streamPlan{unsafe.String(unsafe.SliceData(path), len(path)), input.PathHash(), t.rules.Resolve(&input)})
		cost := streamInputCharge(uint64(cap(e.plans))*uint64(unsafe.Sizeof(streamPlan{}))) + streamInputCharge(uint64(cap(e.plans[0].route))*uint64(unsafe.Sizeof(e.plans[0].route[0])))
		e.routeBytes = uint32(min(cost, uint64(^uint32(0))))
		return
	}
	input := rec.RecBytes{Path: path}
	var scratch [1]*rec.RecBytes
	recs, matched, err := t.rewrites.PlanMetricBytes(scratch[:0], &input)
	e.rewritten = matched
	if err != nil {
		e.invalid = true
		return
	}
	cost := uint64(0)
	for _, r := range recs {
		route := t.rules.Resolve(r)
		cost += streamInputCharge(uint64(cap(route)) * uint64(unsafe.Sizeof(route[0])))
		if len(r.Path) > 0 && (len(e.name) == 0 || &r.Path[0] != unsafe.StringData(e.name)) {
			cost += streamInputCharge(uint64(cap(r.Path)))
		}
		// Keep the immutable path with one string header instead of a slice
		// header. The backing allocation stays alive through the string.
		e.plans = append(e.plans, streamPlan{unsafe.String(unsafe.SliceData(r.Path), len(r.Path)), r.PathHash(), route})
	}
	cost += streamInputCharge(uint64(cap(e.plans)) * uint64(unsafe.Sizeof(streamPlan{})))
	// Configuration caps cached plans at 1 GiB/connection. A larger temporary
	// plan always takes the uncached fallback rather than wrapping its charge.
	e.routeBytes = uint32(min(cost, uint64(^uint32(0))))
}
