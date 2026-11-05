package main

import (
	"time"
	"unsafe"

	"github.com/bookingcom/nanotube/pkg/metrics"
	"github.com/bookingcom/nanotube/pkg/rec"
	"github.com/bookingcom/nanotube/pkg/rewrites"
	"github.com/bookingcom/nanotube/pkg/rules"
	codec "github.com/bookingcom/nanotube/pkg/streamcodec"
	"github.com/bookingcom/nanotube/pkg/target"
	"go.uber.org/zap"
)

type streamPlan struct {
	path  string
	hash  uint64
	route rules.Route
}

// Only the batch, clock and logical allowances belong to a connection. Names,
// normalization and immutable plans belong to the relay's configuration.
type streamRouter struct {
	names     *streamNames
	lg        *zap.Logger
	ms        *metrics.Prom
	batch     *target.Batch
	now       time.Time
	bytes     int
	nameBytes int
	maxBytes  int
	nameLimit int
	changes   streamNameChanges
}

func newStreamRouter(rs rules.Rules, rw rewrites.Rewrites, normalize bool, lg *zap.Logger, ms *metrics.Prom, totalLimit ...int) *streamRouter {
	return &streamRouter{names: newStreamNames(rs, rw, normalize, ms, totalLimit...), lg: lg, ms: ms, batch: target.NewBatch(), nameLimit: int(^uint(0) >> 1)}
}

func (s *streamRouter) NewSink() *streamRouter {
	return &streamRouter{names: s.names, lg: s.lg, ms: s.ms, batch: target.NewBatch(), maxBytes: s.maxBytes, nameLimit: int(^uint(0) >> 1)}
}

func (s *streamRouter) Reset()             { s.publishNames() } // Decoder slots have been released.
func (s *streamRouter) Close()             { s.publishNames() } // Includes STATE-only definitions.
func (s *streamRouter) NameBytes() int     { return s.nameBytes }
func (s *streamRouter) SetNameLimit(n int) { s.nameLimit = n }
func (s *streamRouter) Flush()             { s.publishNames(); s.batch.Flush(); s.now = time.Time{} }

func (s *streamRouter) publishNames() {
	c := s.changes
	if c.dictionary != 0 {
		s.ms.StreamInputDictionaryBytes.Add(float64(c.dictionary))
		s.ms.StreamInputSharedBytes.Add(float64(c.dictionary))
	}
	if c.routes != 0 {
		s.ms.StreamInputRouteBytes.Add(float64(c.routes))
	}
	if c.names != 0 {
		s.ms.StreamInputSharedNames.Add(float64(c.names))
	}
	s.changes = streamNameChanges{}
}

func (s *streamRouter) BindName(name []byte) any {
	limit := s.maxBytes
	if limit == 0 {
		limit = 128 << 20
	}
	e, cached, dict, route, err := s.names.acquire(name, limit-s.bytes, s.nameLimit-s.nameBytes, &s.changes)
	if err != nil {
		return err
	}
	s.nameBytes += dict
	s.bytes += route
	if cached {
		return e
	}
	// The interface type records whether this slot owns a plan reference,
	// without another allocation or a connection-sized side array.
	return (*uncachedStreamName)(e)
}

func (s *streamRouter) ReleaseName(ref any) {
	var e *streamName
	cached := false
	switch v := ref.(type) {
	case *streamName:
		e, cached = v, true
	case *uncachedStreamName:
		e = (*streamName)(v)
	default:
		panic("invalid stream input name reference")
	}
	dict, route := s.names.release(e, cached, &s.changes)
	s.nameBytes -= dict
	s.bytes -= route
}

func (s *streamRouter) BindStreamName(name []byte) (*codec.BoundName, bool, error) {
	ref := s.BindName(name)
	switch e := ref.(type) {
	case *streamName:
		return &e.name, true, nil
	case *uncachedStreamName:
		return &e.name, false, nil
	case error:
		return nil, false, e
	default:
		panic("invalid stream name binding")
	}
}

// BoundName is the first field of streamName; both addresses are identical.
// The decoder holds a real Go pointer and pins the entire enclosing allocation.
func streamNameFromBound(ref *codec.BoundName) *streamName {
	return (*streamName)(unsafe.Pointer(ref))
}

func (s *streamRouter) ReleaseStreamName(ref *codec.BoundName, cached bool) {
	e := streamNameFromBound(ref)
	if cached {
		s.ReleaseName(e)
	} else {
		s.ReleaseName((*uncachedStreamName)(e))
	}
}

func (s *streamRouter) BoundPoint(ref *codec.BoundName, cached bool, value float64, ts int64) {
	e := streamNameFromBound(ref)
	var plan *streamName
	if cached {
		plan = e
	}
	s.point(plan, &e.token, ref.Bytes(), value, ts)
}

func (s *streamRouter) Point(_ uint32, name []byte, value float64, ts int64, slot *any) {
	var plan *streamName
	var tokenSource *uint64
	if slot != nil {
		plan, _ = (*slot).(*streamName)
		if plan != nil {
			tokenSource = &plan.token
		}
		if uncached, ok := (*slot).(*uncachedStreamName); ok {
			tokenSource = &(*streamName)(uncached).token
		}
	}
	s.point(plan, tokenSource, name, value, ts)
}

func (s *streamRouter) point(plan *streamName, tokenSource *uint64, name []byte, value float64, ts int64) {
	if plan == nil {
		// Cache exhaustion preserves delivery. The immutable raw identity can
		// still own a token for a single resulting path.
		plan = &streamName{}
		s.names.build(plan, name)
	} else {
		s.ms.StreamInputRouteHits.Inc()
		if plan.canonical != nil {
			plan = plan.canonical
		}
		tokenSource = &plan.token
	}
	if plan.invalid {
		s.ms.ErrorRecs.Inc()
		return
	}
	if plan.rewritten {
		s.ms.RewrittenRecsTotal.Inc()
	}
	if s.now.IsZero() {
		s.now = time.Now()
	}
	if plan.plans == nil {
		if !s.names.rewrites.Empty() {
			return
		} // A rewrite that emits no paths.
		if plan.typed != nil {
			plan.typed.Push(plan.StreamName(), plan.hash, value, ts, tokenSource, s.batch)
			return
		}
		var route rules.Route
		if plan.route != nil {
			route = *plan.route
		}
		s.pushPoint(plan.StreamName(), plan.hash, route, tokenSource, value, ts)
		return
	}
	if plan.hash != 1 {
		tokenSource = nil
	} // Each rewritten output path needs its own hint.
	for _, p := range unsafe.Slice(plan.plans, int(plan.hash)) {
		s.pushPoint(unsafe.Slice(unsafe.StringData(p.path), len(p.path)), p.hash, p.route, tokenSource, value, ts)
	}
}

func (s *streamRouter) pushPoint(path []byte, hash uint64, route rules.Route, tokenSource *uint64, value float64, ts int64) {
	r := rec.NewTyped(path, value, ts)
	r.Received = s.now
	r.SetPathHash(hash)
	r.ShareStreamToken(tokenSource)
	s.names.rules.PushRoute(route, r, s.lg, s.batch)
}
