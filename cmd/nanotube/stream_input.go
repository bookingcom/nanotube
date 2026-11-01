package main

import (
	"bytes"
	"time"
	"unsafe"

	"github.com/bookingcom/nanotube/pkg/metrics"
	"github.com/bookingcom/nanotube/pkg/rec"
	"github.com/bookingcom/nanotube/pkg/rewrites"
	"github.com/bookingcom/nanotube/pkg/rules"
	"github.com/bookingcom/nanotube/pkg/target"
	"go.uber.org/zap"
)

type streamPlan struct {
	path  []byte
	hash  uint64
	route rules.Route
}
type streamRoute struct {
	plans     []streamPlan
	rewritten bool
	invalid   bool
}
type streamRouter struct {
	rules     rules.Rules
	rewrites  rewrites.Rewrites
	normalize bool
	lg        *zap.Logger
	ms        *metrics.Prom
	batch     *target.Batch
	now       time.Time
	bytes     int
	maxBytes  int
}

func newStreamRouter(rs rules.Rules, rw rewrites.Rewrites, normalize bool, lg *zap.Logger, ms *metrics.Prom) *streamRouter {
	return &streamRouter{rules: rs, rewrites: rw, normalize: normalize, lg: lg, ms: ms, batch: target.NewBatch()}
}
func (s *streamRouter) Reset() {
	s.ms.StreamInputRouteBytes.Sub(float64(s.retainedBytes()))
	s.bytes = 0
}
func (s *streamRouter) Close() { s.Reset() }
func (s *streamRouter) retainedBytes() int {
	return s.bytes
}
func (s *streamRouter) Flush() { s.batch.Flush(); s.now = time.Time{} }
func (s *streamRouter) Point(_ uint32, name []byte, value float64, ts int64, slot *any) {
	var cached *streamRoute
	if slot != nil {
		cached, _ = (*slot).(*streamRoute)
	}
	if cached == nil {
		cached = &streamRoute{}
		s.ms.StreamInputRouteMisses.Inc()
		path, err := rec.NormalizedPath(name, s.normalize)
		if err != nil {
			cached.invalid = true
			s.retain(slot, cached, int(allocationCharge(uint64(unsafe.Sizeof(streamRoute{})))))
			s.ms.ErrorRecs.Inc()
			return
		}
		// Decoder names are immutable allocations; unchanged normalized paths
		// can share them safely, including after reset while output is queued.
		shared := false
		if bytes.Equal(path, name) {
			path = name
			shared = true
		} else if trimmed := bytes.Trim(name, "."); bytes.Equal(path, trimmed) {
			path = trimmed
			shared = true
		}
		r := rec.NewTyped(path, value, ts)
		// Build immutable rewrite and routing plans once per dictionary ID.
		// The regular rewrite matcher may produce multiple copies or blackholes.
		recs, matched, err := s.rewrites.PlanMetricBytes(nil, r)
		cached.rewritten = matched
		if err != nil {
			cached.invalid = true
			s.retain(slot, cached, int(allocationCharge(uint64(unsafe.Sizeof(streamRoute{})))))
			s.ms.ErrorRecs.Inc()
			return
		}
		cost := int(allocationCharge(uint64(unsafe.Sizeof(streamRoute{}))))
		for _, r := range recs {
			cached.plans = append(cached.plans, streamPlan{r.Path, r.PathHash(), s.rules.Resolve(r)})
			cost += int(allocationCharge(uint64(cap(cached.plans[len(cached.plans)-1].route)) * uint64(unsafe.Sizeof(cached.plans[len(cached.plans)-1].route[0]))))
			if len(r.Path) > 0 && (!shared || &r.Path[0] != &path[0]) {
				cost += int(allocationCharge(uint64(cap(r.Path))))
			}
		}
		cost += int(allocationCharge(uint64(cap(cached.plans)) * uint64(unsafe.Sizeof(streamPlan{}))))
		s.retain(slot, cached, cost)
	} else {
		s.ms.StreamInputRouteHits.Inc()
	}
	if cached.invalid {
		s.ms.ErrorRecs.Inc()
		return
	}
	if cached.rewritten {
		s.ms.RewrittenRecsTotal.Inc()
	}
	if s.now.IsZero() {
		s.now = time.Now()
	}
	for _, plan := range cached.plans {
		r := rec.NewTyped(plan.path, value, ts)
		r.Received = s.now
		r.SetPathHash(plan.hash)
		s.rules.PushRoute(plan.route, r, s.lg, s.batch)
	}
}

// A full cache stops retaining new plans; policy and queue admission still run.
// The next protocol reset drops the oldest generation, preferring recent names.
func (s *streamRouter) retain(slot *any, plan *streamRoute, cost int) {
	limit := s.maxBytes
	if limit == 0 {
		limit = 128 << 20
	}
	if slot == nil || cost > limit-s.bytes {
		return
	}
	*slot = plan
	s.bytes += cost
	s.ms.StreamInputRouteBytes.Add(float64(cost))
}
