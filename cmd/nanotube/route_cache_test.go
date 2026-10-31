package main

import (
	"fmt"
	"hash/fnv"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/metrics"
	"github.com/bookingcom/nanotube/pkg/rec"
	"github.com/bookingcom/nanotube/pkg/rewrites"
	"github.com/bookingcom/nanotube/pkg/rules"
	"github.com/bookingcom/nanotube/pkg/target"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.uber.org/zap"
)

type routeObservation struct {
	target, line string
	hash         uint64
	received     time.Time
}
type routeRecorder struct {
	sync.Mutex
	records []routeObservation
}
type cacheTestTarget struct {
	name string
	log  *routeRecorder
}

func (s *cacheTestTarget) PushBytes(r *rec.RecBytes, _ *metrics.Prom) error {
	s.log.Lock()
	defer s.log.Unlock()
	s.log.records = append(s.log.records, routeObservation{s.name, string(r.Bytes()), r.PathHash(), r.Received})
	return nil
}
func (s *cacheTestTarget) Send(*sync.WaitGroup, chan struct{}) {}
func (s *cacheTestTarget) GetName() string                     { return s.name }

func (s *cacheTestTarget) Push(r *rec.RecBytes) { _ = s.PushBytes(r, nil) }
func (*cacheTestTarget) Stream(*sync.WaitGroup) {}
func (*cacheTestTarget) IsAvailable() bool      { return true }
func (*cacheTestTarget) Stop()                  {}

func cacheTestRules(t *testing.T, ms *metrics.Prom, log *routeRecorder) rules.Rules {
	t.Helper()
	cls := target.Clusters{"blackhole": &target.Cluster{Name: "blackhole", Type: conf.BlackholeCluster}}
	for _, n := range []string{"a", "b", "c"} {
		cls[n] = &target.Cluster{Name: n, Type: conf.ToallCluster, Hosts: []target.Target{&cacheTestTarget{n, log}}}
	}
	cfg := conf.Rules{Rule: []conf.Rule{
		{Prefixes: []string{"drop."}}, // matching empty rule stops processing
		{Prefixes: []string{"live."}, Clusters: []string{"a", "a"}, Continue: true},
		{Regexs: []string{"count$"}, Clusters: []string{"a", "b"}, Continue: true},
		{Prefixes: []string{"archive."}, Clusters: []string{"blackhole"}},
		{Regexs: []string{"."}, Clusters: []string{"b", "c"}},
	}}
	rs, err := rules.Build(&cfg, cls, false, ms)
	if err != nil {
		t.Fatal(err)
	}
	return rs
}

func TestRouteCacheDifferential(t *testing.T) {
	for _, normalize := range []bool{false, true} {
		t.Run(fmt.Sprint(normalize), func(t *testing.T) {
			ma, mb := metrics.New(), metrics.New()
			var a, b routeRecorder
			ra, rb := cacheTestRules(t, ma, &a), cacheTestRules(t, mb, &b)
			cfg := conf.Rewrites{Rewrite: []conf.Rewrite{
				{From: `^service\.`, To: "archive.", Copy: true},
				{From: `^service\.`, To: "live."},
				{From: `total$`, To: "count", Copy: true},
				{From: `^empty$`, To: ""},
			}}
			wa, err := rewrites.Build(&cfg, false, ma)
			if err != nil {
				t.Fatal(err)
			}
			wb, err := rewrites.Build(&cfg, false, mb)
			if err != nil {
				t.Fatal(err)
			}
			c := newRouteCache(1<<20, mb)
			nowF := func() time.Time { return time.Unix(1700000000, 0) }
			for repeat := 0; repeat < 10; repeat++ {
				for _, path := range []string{"service.total", "..service..total..", "service.count", "archive.count", "plain.total", "drop.count", "odd!name", "empty", "...", "missing", "a 1 2 3"} {
					in := []byte(fmt.Sprintf("%s %d %d", path, repeat, repeat+100))
					procBatch(in, ra, wa, normalize, false, nowF, zap.NewNop(), ma, nil, nil)
					procBatch(in, rb, wb, normalize, false, nowF, zap.NewNop(), mb, nil, c)
					for i := range in {
						in[i] = 'x'
					} // cache must own the key/path bytes
				}
			}
			if !reflect.DeepEqual(a.records, b.records) {
				t.Fatal("cached deliveries differ in order, bytes, hash or timestamp")
			}
			for _, o := range b.records {
				h := fnv.New64a() // independently check the actual host-selection hash
				r, err := rec.ParseRec([]byte(o.line[:len(o.line)-1]), false, false, nowF, nil)
				if err != nil {
					t.Fatal(err)
				}
				_, _ = h.Write(r.Path)
				if o.hash != h.Sum64() {
					t.Fatal("cached FNV differs")
				}
			}
			for _, pair := range [][2]float64{
				{testutil.ToFloat64(ma.BlackholedRecs), testutil.ToFloat64(mb.BlackholedRecs)},
				{testutil.ToFloat64(ma.RewrittenRecsTotal), testutil.ToFloat64(mb.RewrittenRecsTotal)},
				{testutil.ToFloat64(ma.ErrorRecs), testutil.ToFloat64(mb.ErrorRecs)},
			} {
				if pair[0] != pair[1] {
					t.Fatalf("counter mismatch: %v", pair)
				}
			}
			if testutil.ToFloat64(mb.RouteCacheHits) == 0 {
				t.Fatal("no hits")
			}
		})
	}
}

func TestRouteCacheBoundAndCollisions(t *testing.T) {
	ms := metrics.New()
	const limit = 65536
	c := newRouteCache(limit, ms)
	// Force all names into one bucket and use identical hashes: equality must
	// still compare every raw byte, and collisions must evict rather than grow.
	for i := 0; i < 10000; i++ {
		raw := []byte(fmt.Sprintf("metric.%d", i))
		c.put(raw, raw, uint64(i), []routePlan{{hash: uint64(i)}}, 0)
		if got := c.get(raw, 0); got == nil || got.hash != uint64(i) {
			t.Fatal("wrong collision result")
		}
		if bytes := testutil.ToFloat64(ms.RouteCacheBytes); bytes > limit || bytes < 0 {
			t.Fatalf("budget exceeded: %v", bytes)
		}
	}
	if c.get([]byte("absent"), 0) != nil {
		t.Fatal("false hit")
	}
	if testutil.ToFloat64(ms.RouteCacheEvictions) == 0 {
		t.Fatal("no eviction")
	}
	before := testutil.ToFloat64(ms.RouteCacheBytes)
	c.put(make([]byte, limit), nil, 0, nil, 0)
	if testutil.ToFloat64(ms.RouteCacheBytes) != before {
		t.Fatal("oversized entry retained")
	}
}

func TestRouteCacheConcurrentAndFreshPipeline(t *testing.T) {
	ms := metrics.New()
	c := newRouteCache(1<<20, ms)
	var wg sync.WaitGroup
	for n := 0; n < 16; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 1000; i++ {
				raw := []byte(fmt.Sprintf("metric.%d", i%100))
				key := c.key(raw)
				if e := c.get(raw, key); e != nil {
					if string(e.raw) != string(raw) {
						t.Error("wrong key")
					}
				} else {
					c.put(raw, raw, 7, []routePlan{{hash: 7}}, key)
				}
			}
		}()
	}
	wg.Wait()
	// Managed reload creates a replacement process and pipeline. No cache
	// state is global or inherited, including destination identities.
	fresh := newRouteCache(1<<20, metrics.New())
	if fresh.get([]byte("metric.0"), fresh.key([]byte("metric.0"))) != nil {
		t.Fatal("replacement inherited stale routes")
	}
}

type routeAvailableHost struct {
	*cacheTestTarget
	available bool
}

func (h *routeAvailableHost) IsAvailable() bool { return h.available }

func TestRouteCacheChecksAvailabilityPerRecord(t *testing.T) {
	ms := metrics.New()
	var log routeRecorder
	a := &routeAvailableHost{&cacheTestTarget{"a", &log}, true}
	b := &routeAvailableHost{&cacheTestTarget{"b", &log}, false}
	cls := target.Clusters{"live": &target.Cluster{Name: "live", Type: conf.LB, Hosts: []target.Target{a, b}}}
	rs, err := rules.Build(&conf.Rules{Rule: []conf.Rule{{Regexs: []string{".*"}, Clusters: []string{"live"}}}}, cls, false, ms)
	if err != nil {
		t.Fatal(err)
	}
	cache := newRouteCache(65536, ms)
	var rw rewrites.Rewrites
	batch := target.NewBatch()
	nowF := func() time.Time { return time.Unix(1700000000, 0) }
	for i := 0; i < 2; i++ {
		procBatch([]byte("metric.count 1 2"), rs, rw, true, false, nowF, zap.NewNop(), ms, batch, cache)
		batch.Flush()
		a.available = false
		b.available = true
	}
	if len(log.records) != 2 || log.records[0].target != "a" || log.records[1].target != "b" {
		t.Fatal("cache froze host availability")
	}
	if testutil.ToFloat64(ms.RouteCacheHits) != 1 {
		t.Fatal("second record missed cache")
	}
}

func TestRouteCacheConcurrentProcessing(t *testing.T) {
	ms := metrics.New()
	var log routeRecorder
	rs := cacheTestRules(t, ms, &log)
	cache := newRouteCache(1<<20, ms)
	var rw rewrites.Rewrites
	var wg sync.WaitGroup
	nowF := func() time.Time { return time.Unix(1700000000, 0) }
	for worker := 0; worker < 16; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				line := []byte(fmt.Sprintf("plain.count %d %d", worker, i))
				procBatch(line, rs, rw, true, false, nowF, zap.NewNop(), ms, nil, cache)
				for i := range line {
					line[i] = 'x'
				}
			}
		}(worker)
	}
	wg.Wait()
	if len(log.records) != 16*100*3 {
		t.Fatalf("wrong delivery count: %d", len(log.records))
	}
}
