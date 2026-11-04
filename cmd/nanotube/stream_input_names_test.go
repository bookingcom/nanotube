package main

import (
	"bytes"
	"errors"
	"fmt"
	"hash/maphash"
	"sync"
	"testing"

	"github.com/bookingcom/nanotube/pkg/metrics"
	"github.com/bookingcom/nanotube/pkg/rewrites"
	"github.com/bookingcom/nanotube/pkg/rules"
	codec "github.com/bookingcom/nanotube/pkg/streamcodec"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.uber.org/zap"
)

func assertEmptyStreamNames(t testing.TB, s *streamRouter) {
	t.Helper()
	s.publishNames()
	for _, g := range []float64{testutil.ToFloat64(s.ms.StreamInputSharedNames), testutil.ToFloat64(s.ms.StreamInputRouteBytes), float64(s.NameBytes()), float64(s.bytes)} {
		if g != 0 {
			t.Fatalf("released table retains state: %v", g)
		}
	}
	if n := testutil.ToFloat64(s.ms.StreamInputSharedBytes); n != float64(s.names.fixed) {
		t.Fatalf("index did not shrink: %v != %v", n, s.names.fixed)
	}
	if n := testutil.ToFloat64(s.ms.StreamInputDictionaryBytes); n != float64(s.names.fixed) {
		t.Fatalf("dictionary gauge leaked: %v", n)
	}
	for i := range s.names.shards {
		shard := &s.names.shards[i]
		shard.mu.Lock()
		empty := shard.count == 0 && len(shard.index) == 16
		for _, entry := range shard.index {
			empty = empty && entry == nil
		}
		shard.mu.Unlock()
		if !empty {
			t.Fatal("released entries remain reachable from the shared index")
		}
	}
}

func TestStreamInputSharedCanonicalPlansAndLifetime(t *testing.T) {
	rs, rw, ms, points := differentialPipeline(t)
	s := newStreamRouter(rs, rw, true, zap.NewNop(), ms)
	var refs []any
	var canonical *streamName
	for _, name := range []string{".example.good.", "example..good", "example.good"} {
		for range 20 {
			ref := s.BindName([]byte(name))
			refs = append(refs, ref)
			e := ref.(*streamName)
			if e.canonical != nil {
				e = e.canonical
			}
			if canonical != nil && e != canonical {
				t.Fatal("normalization did not reuse the canonical plan")
			}
			canonical = e
			s.Point(0, []byte(name), 1, 2, &ref)
		}
	}
	s.Flush()
	if len(*points) != 60 || testutil.ToFloat64(ms.StreamInputRouteMisses) != 1 {
		t.Fatal("name sharing changed routing or rebuilt a plan")
	}
	// Paths retained by output remain readable after every decoder releases.
	queued := canonical.plans[0].path
	for _, ref := range refs {
		s.ReleaseName(ref)
	}
	assertEmptyStreamNames(t, s)
	if string(queued) != "example.good" {
		t.Fatal("released shared name changed queued output")
	}
}

func TestStreamInputSharedRouteBudgetDoesNotPinUnchargedPlans(t *testing.T) {
	rs, rw, ms, points := differentialPipeline(t)
	s := newStreamRouter(rs, rw, true, zap.NewNop(), ms)
	other := s.NewSink()
	other.maxBytes = 1
	cached := s.BindName([]byte("example.good"))
	uncached := other.BindName([]byte("example.good"))
	if _, ok := uncached.(*uncachedStreamName); !ok {
		t.Fatal("over-budget stream pinned another connection's plan")
	}
	s.ReleaseName(cached)
	s.Flush()
	if testutil.ToFloat64(ms.StreamInputRouteBytes) != 0 || other.bytes != 0 {
		t.Fatal("uncached stream retained a plan")
	}
	for range 10 {
		other.Point(0, []byte("example.good"), 1, 2, &uncached)
	}
	other.Flush()
	if len(*points) != 10 {
		t.Fatal("route allowance changed delivery")
	}
	other.ReleaseName(uncached)
	other.Close()
	assertEmptyStreamNames(t, s)
}

func TestStreamInputSharedChurnAndIndexDeletion(t *testing.T) {
	s := newStreamRouter(rules.Rules{}, rewrites.Rewrites{}, true, zap.NewNop(), metrics.New())
	// One busy shard forces collisions, wraparound, growth and shrink.
	var names [][]byte
	for i := 0; len(names) < 256; i++ {
		name := fmt.Appendf(nil, "example.churn.%d", i)
		if s.names.shard(maphash.Bytes(s.names.seed, name)) == &s.names.shards[0] {
			names = append(names, name)
		}
	}
	for range 10 {
		var refs []any
		for _, name := range names {
			refs = append(refs, s.BindName(name))
		}
		for i := 0; i < len(refs); i += 2 {
			s.ReleaseName(refs[i])
		}
		for i := 1; i < len(refs); i += 2 {
			duplicate := s.BindName(names[i])
			if duplicate != refs[i] {
				t.Fatal("deletion broke a probe chain")
			}
			s.ReleaseName(duplicate)
			s.ReleaseName(refs[i])
		}
		assertEmptyStreamNames(t, s)
	}
}

func TestStreamInputSharedConcurrentResetCloseAndFallback(t *testing.T) {
	root := newStreamRouter(rules.Rules{}, rewrites.Rewrites{}, true, zap.NewNop(), metrics.New())
	var wg sync.WaitGroup
	for worker := range 80 {
		wg.Go(func() {
			s := root.NewSink()
			if worker%2 == 0 {
				s.maxBytes = 1
			}
			for i := range 100 {
				// Shared normalized names and aliases exercise the two-stage
				// acquisition even when both names land on the same shard.
				name := fmt.Appendf(nil, ".example..concurrent.%d.", i%10)
				ref := s.BindName(name)
				for range 3 {
					s.Point(0, name, 1, 2, &ref)
				}
				s.Flush()
				s.ReleaseName(ref)
			}
			if s.NameBytes() != 0 || s.bytes != 0 {
				t.Error("connection accounting leaked")
			}
			s.Close()
		})
	}
	wg.Wait()
	assertEmptyStreamNames(t, root)
}

func TestStreamInputSharedSnapshotRebindAndAdmissionFailure(t *testing.T) {
	rs, rw, ms, points := differentialPipeline(t)
	s := newStreamRouter(rs, rw, true, zap.NewNop(), ms)
	e, _ := codec.NewEncoder(codec.Options{})
	for i := range 32 {
		if err := e.Add(fmt.Appendf(nil, "example.good.%d", i), 1, 2); err != nil {
			t.Fatal(err)
		}
	}
	wire, err := e.Flush(nil)
	if err != nil {
		t.Fatal(err)
	}
	e.Close()
	d, _ := codec.NewDecoder(codec.Options{})
	d.SetNameResolver(s.BindName)
	d.SetNameReleaser(s.ReleaseName)
	if _, err := d.ReadFrame(bytes.NewReader(wire), nil); err != nil {
		t.Fatal(err)
	}
	snapshot, err := d.Snapshot(nil)
	if err != nil {
		t.Fatal(err)
	}
	for pass := range 2 {
		next := s.NewSink()
		nd, _ := codec.NewDecoder(codec.Options{})
		nd.SetNameResolver(next.BindName)
		nd.SetNameReleaser(next.ReleaseName)
		if pass == 1 {
			next.SetNameLimit(nameSizeAllowance(20) * 2)
		}
		r := bytes.NewReader(snapshot)
		var replayErr error
		for r.Len() > 0 {
			if _, replayErr = nd.ReadFrame(r, nil); replayErr != nil {
				break
			}
		}
		next.Flush()
		s.Flush()
		if pass == 1 && !errors.Is(replayErr, codec.ErrLimit) {
			t.Fatal("snapshot bypassed connection dictionary admission")
		}
		if pass == 0 && (replayErr != nil || testutil.ToFloat64(ms.StreamInputSharedNames) != 32 || testutil.ToFloat64(ms.StreamInputRouteMisses) != 32) {
			t.Fatal("snapshot did not reuse names and plans")
		}
		nd.Close()
		nd.Close()
		next.Close()
		if next.NameBytes() != 0 || next.bytes != 0 {
			t.Fatal("partial STATE binding leaked references")
		}
	}
	d.Close()
	assertEmptyStreamNames(t, s)
	if len(*points) != 0 {
		t.Fatal("STATE replay emitted data")
	}
}

// A sender filling its peer allowance must fit a relay whose dictionary limit
// equals it: shared-entry metadata must not tighten connection admission.
func TestStreamInputSharedAdmitsFullPeerAllowance(t *testing.T) {
	rs, rw, ms, _ := differentialPipeline(t)
	s := newStreamRouter(rs, rw, true, zap.NewNop(), ms)
	const limit = 1 << 20
	e, err := codec.NewEncoder(codec.Options{DictionaryBytes: 64 << 20, PeerDictionaryBytes: limit})
	if err != nil {
		t.Fatal(err)
	}
	d, _ := codec.NewDecoder(codec.Options{DictionaryBytes: limit})
	d.SetNameResolver(func(name []byte) any { // As readStream binds names.
		s.SetNameLimit(limit - d.RetainedBytes())
		return s.BindName(name)
	})
	d.SetNameReleaser(s.ReleaseName)
	resets, peak := 0, 0
	for i := range 40000 {
		if _, reset, err := e.AddHashed(fmt.Appendf(nil, "example.good.%080d", i), uint64(i), 1, 2); err != nil {
			t.Fatal(err)
		} else if reset {
			resets++
		}
		if i%1000 != 999 {
			continue
		}
		wire, err := e.Flush(nil)
		if err != nil {
			t.Fatal(err)
		}
		for r := bytes.NewReader(wire); r.Len() > 0; {
			if _, err := d.ReadFrame(r, nil); err != nil {
				t.Fatalf("relay rejected a sender within its peer allowance: %v", err)
			}
			peak = max(peak, d.RetainedBytes()+s.NameBytes())
		}
	}
	if resets == 0 || peak < limit*3/4 || peak > limit {
		t.Fatalf("dictionary never filled the peer allowance: resets=%d peak=%d", resets, peak)
	}
	e.Close()
	d.Close()
	s.Close()
	assertEmptyStreamNames(t, s)
}

func TestStreamInputSharedProductionShapeChurn(t *testing.T) {
	streams, ms := modelStreams(t)
	for epoch := range 3 {
		wires, _ := modelWires(t, 1600, 20, epoch)
		var wg sync.WaitGroup
		for i, m := range streams {
			wg.Go(func() { m.decode(t, wires[i%len(wires)]) })
		}
		wg.Wait()
		if testutil.ToFloat64(ms.StreamInputSharedNames) != 1600 {
			t.Fatal("old epoch names retained")
		}
	}
	if streams[0].receiver.count.Load() != 1600*20*3 {
		t.Fatal("model changed receiver count")
	}
	closeModel(streams)
	assertEmptyStreamNames(t, streams[0].s)
}
