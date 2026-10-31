package main

import (
	"bytes"
	"hash/maphash"
	"sync"
	"unsafe"

	"github.com/bookingcom/nanotube/pkg/metrics"
	"github.com/bookingcom/nanotube/pkg/rules"
)

const (
	routeCacheShards = 64
	routeCacheWays   = 8
)

type routePlan struct {
	hash  uint64
	route rules.Route
}

// Entries are immutable after publication. Eviction can remove an entry while
// a worker uses it; Go keeps that worker's reference alive until it finishes.
type cachedRoute struct {
	raw, normalized []byte
	hash            uint64
	plans           []routePlan
	charge          uint64
}

type routeSlot struct {
	key    uint64
	entry  *cachedRoute
	recent bool
}

type routeShard struct {
	sync.Mutex
	slots       []routeSlot
	hand        int
	used, limit uint64
}

// A eight-way set associative index bounds lookup work and index memory. CLOCK
// eviction bounds retained entry bytes independently of name length. maphash is
// only an index: exact raw bytes determine equality, and FNV still selects hosts.
type routeCache struct {
	seed    maphash.Seed
	shards  [routeCacheShards]routeShard
	metrics *metrics.Prom
}

// allocationCharge conservatively includes size-class rounding. Large Go
// allocations are rounded to pages; small ones have at most 25% class slack.
func allocationCharge(n uint64) uint64 {
	if n == 0 {
		return 0
	}
	n += 16 // conservatively include heap object headers
	if n > 32768 {
		return (n + 8191) &^ 8191
	}
	return (n*5/4 + 63) &^ 63
}

func newRouteCache(limit uint64, ms *metrics.Prom) *routeCache {
	if limit < 65536 {
		return nil
	}
	c := &routeCache{seed: maphash.MakeSeed(), metrics: ms}
	fixed := allocationCharge(uint64(unsafe.Sizeof(*c)))
	perShard := (limit - fixed) / routeCacheShards
	total := fixed
	for i := range c.shards {
		s := &c.shards[i]
		n := routeCacheWays
		for uint64(n*2)*320 <= perShard {
			n *= 2
		}
		s.slots = make([]routeSlot, n)
		table := allocationCharge(uint64(n) * uint64(unsafe.Sizeof(routeSlot{})))
		s.limit = perShard - table
		total += table
	}
	ms.RouteCacheBytes.Set(float64(total))
	return c
}

func (c *routeCache) key(raw []byte) uint64 { return maphash.Bytes(c.seed, raw) }

func (c *routeCache) get(raw []byte, key uint64) *cachedRoute {
	s := &c.shards[key%routeCacheShards]
	s.Lock()
	base := int(key/routeCacheShards) & (len(s.slots) - 1) &^ (routeCacheWays - 1)
	for i := base; i < base+routeCacheWays; i++ {
		slot := &s.slots[i]
		if slot.key == key && slot.entry != nil && bytes.Equal(slot.entry.raw, raw) {
			slot.recent = true
			e := slot.entry
			s.Unlock()
			c.metrics.RouteCacheHits.Inc()
			return e
		}
	}
	s.Unlock()
	c.metrics.RouteCacheMisses.Inc()
	return nil
}

func (c *routeCache) put(raw, normalized []byte, hash uint64, plans []routePlan, key uint64) {
	charge := allocationCharge(uint64(unsafe.Sizeof(cachedRoute{}))) + allocationCharge(uint64(len(raw))) +
		allocationCharge(uint64(cap(plans))*uint64(unsafe.Sizeof(routePlan{})))
	same := bytes.Equal(raw, normalized)
	if !same {
		charge += allocationCharge(uint64(len(normalized)))
	}
	for _, p := range plans {
		charge += allocationCharge(uint64(cap(p.route)) * uint64(unsafe.Sizeof(p.route[0])))
	}
	s := &c.shards[key%routeCacheShards]
	if charge > s.limit {
		return
	} // oversized names are processed, never cached
	s.Lock()
	defer s.Unlock()
	base := int(key/routeCacheShards) & (len(s.slots) - 1) &^ (routeCacheWays - 1)
	chosen := -1
	for i := base; i < base+routeCacheWays; i++ {
		slot := &s.slots[i]
		if slot.entry == nil {
			if chosen < 0 {
				chosen = i
			}
			continue
		}
		if slot.key == key && bytes.Equal(slot.entry.raw, raw) {
			return
		}
	}
	if chosen < 0 {
		// CLOCK within the eight-way bucket; at most two passes.
		for i := 0; i < 2*routeCacheWays; i++ {
			n := base + (s.hand+i)%routeCacheWays
			if !s.slots[n].recent {
				chosen = n
				break
			}
			s.slots[n].recent = false
		}
		s.hand++
		if chosen < 0 {
			chosen = base
		}
		c.evict(s, chosen)
	}
	for steps := 0; s.used+charge > s.limit && steps < 128; steps++ {
		i := s.hand & (len(s.slots) - 1)
		s.hand++
		if s.slots[i].entry == nil {
			continue
		}
		if s.slots[i].recent && steps < 64 {
			s.slots[i].recent = false
			continue
		}
		c.evict(s, i)
	}
	if s.used+charge > s.limit {
		return
	}
	e := &cachedRoute{raw: bytes.Clone(raw), hash: hash, plans: plans, charge: charge}
	if same {
		e.normalized = e.raw
	} else {
		e.normalized = bytes.Clone(normalized)
	}
	s.slots[chosen] = routeSlot{key: key, entry: e, recent: true}
	s.used += charge
	c.metrics.RouteCacheBytes.Add(float64(charge))
}

func (c *routeCache) evict(s *routeShard, i int) {
	if e := s.slots[i].entry; e != nil {
		s.used -= e.charge
		c.metrics.RouteCacheBytes.Sub(float64(e.charge))
		c.metrics.RouteCacheEvictions.Inc()
		s.slots[i] = routeSlot{}
	}
}
