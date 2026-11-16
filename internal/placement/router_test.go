package placement

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/bookingcom/nanotube/internal/v2placement"
	"github.com/bookingcom/nanotube/pkg/rec"
	"github.com/dgryski/go-jump"
)

var testLane = v2placement.Lane{Producer: "relay-1/inc-1", Stream: "lane-0"}

type testSink struct {
	mu      sync.Mutex
	records []Delivery
	full    bool
	legacy  bool
}

func (s *testSink) Epochs() bool { return !s.legacy }
func (s *testSink) Enqueue(d Delivery) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.full {
		return false
	}
	s.records = append(s.records, d)
	return true
}
func (s *testSink) all() []Delivery {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Delivery(nil), s.records...)
}
func loadFixture(t testing.TB) v2placement.Document {
	t.Helper()
	f, e := os.Open("../v2placement/testdata/map-v1.json")
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	d, e := v2placement.Decode(f)
	if e != nil {
		t.Fatal(e)
	}
	return d
}
func clone(d v2placement.Document) v2placement.Document {
	b, _ := json.Marshal(d)
	var n v2placement.Document
	_ = json.Unmarshal(b, &n)
	return n
}
func newRouter(t testing.TB, s Sink, limit int) *Router {
	t.Helper()
	r, e := New(loadFixture(t), "site-a", testLane, s, limit)
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func hash(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func moved(t testing.TB, d v2placement.Document, group string, final uint64) (v2placement.Document, []v2placement.Activation) {
	t.Helper()
	n := clone(d)
	n.Revision++
	n.Predecessor = d.Digest()
	n.Operation = "move"
	var before, after v2placement.Replica
	for i := range n.Groups {
		if n.Groups[i].Selector.GroupID() == group {
			before = n.Groups[i].Replicas[0]
			p := &n.Groups[i].Replicas[0]
			p.Epoch++
			p.Writer = "d"
			p.Read = append(p.Read, "d")
			after = *p
		}
	}
	h := hash("root")
	roots := v2placement.Roots{Parts: h, Catalog: h, Policy: h, Tombstones: h}
	f := v2placement.Fence{Group: group, Replica: "site-a", Store: before.Writer, Epoch: before.Epoch, MapDigest: d.Digest(), Operation: n.Operation, WriterSet: hash(before.Producers), Roots: roots, Frontier: v2placement.Frontier{{Owner: "owner", Cursor: 1}}}
	for _, l := range before.Producers {
		seq := uint64(0)
		if l == testLane {
			seq = final
		}
		f.Ends = append(f.Ends, v2placement.EndMarker{Lane: l, Final: seq, Durable: seq})
	}
	c := v2placement.Coverage{Group: group, Replica: "site-a", Store: after.Writer, Epoch: after.Epoch, MapDigest: n.Digest(), Operation: n.Operation, FenceDigest: f.Digest(), Roots: roots, Frontier: f.Frontier, Required: after.Required}
	acts := []v2placement.Activation{{Fence: f, Coverage: c}}
	if err := d.ValidateTransition(n, acts); err != nil {
		t.Fatal(err)
	}
	return n, acts
}
func recordsForGroups(r *Router) (*rec.RecBytes, *rec.RecBytes) {
	var first *rec.RecBytes
	var group string
	for i := 0; ; i++ {
		p := &rec.RecBytes{Path: []byte(fmt.Sprintf("test.metric.%d", i))}
		rt, _ := r.current.Load().selectRoute(p)
		if first == nil {
			first = p
			group = rt.group
		} else if rt.group != group {
			return first, p
		}
	}
}

func TestGoldenCompiledSelectors(t *testing.T) {
	var fixture struct {
		Routing v2placement.Routing `json:"routing"`
		Vectors []struct {
			Name, Group string
			VShard      uint32
			Cell        []uint32
		} `json:"vectors"`
	}
	b, e := os.ReadFile("../v2placement/testdata/routing-v1.json")
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(b, &fixture); e != nil {
		t.Fatal(e)
	}
	d := loadFixture(t)
	d.Routing = fixture.Routing
	d.Bootstrap = []v2placement.BootstrapReplica{{ID: "site-a", JumpSize: 3, Slots: []string{"a", "b", "c"}}}
	d.Groups, e = v2placement.Bootstrap(d.Routing, d.Bootstrap)
	if e != nil {
		t.Fatal(e)
	}
	for i := range d.Groups {
		d.Groups[i].Replicas[0].Producers = []v2placement.Lane{testLane}
	}
	sink := &testSink{}
	r, e := New(d, "site-a", testLane, sink, 2)
	if e != nil {
		t.Fatal(e)
	}
	for _, v := range fixture.Vectors {
		record := &rec.RecBytes{Path: []byte(v.Name)}
		if err := r.Push(record); err != nil {
			t.Fatal(err)
		}
		got := sink.all()[len(sink.all())-1]
		if got.Group != v.Group || got.VShard != v.VShard || got.Store != d.Bootstrap[0].Slots[v.Cell[0]] || got.Epoch != 1 || got.MapDigest != d.Digest() {
			t.Fatalf("%q: %+v", v.Name, got)
		}
	}
}

func TestLegacyOwnerEquality(t *testing.T) {
	d := loadFixture(t)
	for _, replica := range d.Bootstrap {
		sink := &testSink{}
		r, e := New(d, replica.ID, testLane, sink, 2)
		if e != nil {
			t.Fatal(e)
		}
		for i := 0; i < 10000; i++ {
			record := &rec.RecBytes{Path: []byte(fmt.Sprintf("metric.%d", i))}
			rt, _ := r.current.Load().selectRoute(record)
			want := replica.Slots[jump.Hash(record.PathHash(), int(replica.JumpSize))]
			if rt.replica.Writer != want {
				t.Fatalf("%s %s got %s want %s", replica.ID, record.Path, rt.replica.Writer, want)
			}
		}
	}
}

func TestBarrierOrderingAndIsolation(t *testing.T) {
	sink := &testSink{}
	r := newRouter(t, sink, 2)
	a, b := recordsForGroups(r)
	if e := r.Push(a); e != nil {
		t.Fatal(e)
	}
	group := sink.all()[0].Group
	barrier, e := r.BeginMove(group)
	if e != nil {
		t.Fatal(e)
	}
	if barrier.Final != 1 || barrier.Epoch != 1 || barrier.Revision != 1 || barrier.MapDigest != loadFixture(t).Digest() {
		t.Fatal(barrier)
	}
	for i := 0; i < 2; i++ {
		if e := r.Push(a); e != nil {
			t.Fatal(e)
		}
	}
	if e := r.Push(a); e != ErrHeldFull {
		t.Fatalf("unbounded hold: %v", e)
	}
	if e := r.Push(b); e != nil {
		t.Fatal(e)
	}
	if len(sink.all()) != 2 {
		t.Fatal("held group escaped or other group stalled")
	}
	next, acts := moved(t, loadFixture(t), group, 1)
	if e := r.Adopt(next, acts); e == nil || r.Revision() != 1 {
		t.Fatal("switched before ACK")
	}
	if e := r.Acknowledge(group, 1, 2); e == nil {
		t.Fatal("ACK beyond dispatched sequence")
	}
	if e := r.Acknowledge(group, 1, 1); e != nil {
		t.Fatal(e)
	}
	if e := r.Adopt(next, nil); e == nil {
		t.Fatal("switched without fence/coverage")
	}
	if e := r.Adopt(next, acts); e != nil {
		t.Fatal(e)
	}
	got := sink.all()
	if len(got) != 4 {
		t.Fatal(got)
	}
	for i, p := range got[2:] {
		if p.Store != "d" || p.Epoch != 2 || p.Revision != 2 || p.Sequence != uint64(i+1) {
			t.Fatal(p)
		}
	}
	if r.Stats.Pending.Load() != 0 || r.Stats.Held.Load() != 2 || r.Stats.Dropped.Load() != 1 {
		t.Fatal("wrong hold metrics")
	}
	if e := r.Acknowledge(group, 1, 1); e == nil {
		t.Fatal("stale ACK accepted")
	}
	if e := r.Push(a); e != nil {
		t.Fatal(e)
	}
	if sink.all()[4].Sequence != 3 {
		t.Fatal("release order changed")
	}
}

func TestBlockedReleasePreservesSuffix(t *testing.T) {
	sink := &testSink{}
	r := newRouter(t, sink, 3)
	a, _ := recordsForGroups(r)
	if e := r.Push(a); e != nil {
		t.Fatal(e)
	}
	group := sink.all()[0].Group
	if _, e := r.BeginMove(group); e != nil {
		t.Fatal(e)
	}
	if e := r.Acknowledge(group, 1, 1); e != nil {
		t.Fatal(e)
	}
	if e := r.Push(a); e != nil {
		t.Fatal(e)
	}
	sink.full = true
	n, acts := moved(t, loadFixture(t), group, 1)
	if e := r.Adopt(n, acts); e != nil {
		t.Fatal(e)
	}
	if r.Stats.Pending.Load() != 1 {
		t.Fatal("full sink dropped held record")
	}
	if e := r.Push(a); e != nil {
		t.Fatal(e)
	}
	sink.full = false
	if e := r.RetryHeld(group); e != nil {
		t.Fatal(e)
	}
	got := sink.all()
	if len(got) != 3 || got[1].Sequence != 1 || got[2].Sequence != 2 || got[2].Epoch != 2 {
		t.Fatal(got)
	}
}

func TestFailClosedLegacyAndRestart(t *testing.T) {
	r := newRouter(t, &testSink{legacy: true}, 1)
	a, _ := recordsForGroups(r)
	rt, _ := r.current.Load().selectRoute(a)
	if _, e := r.BeginMove(rt.group); e != ErrTransport {
		t.Fatal(e)
	}
	if e := r.Acknowledge(rt.group, 1, 0); e != ErrTransport {
		t.Fatal(e)
	}
	d := loadFixture(t)
	d.Revision++
	d.Predecessor = loadFixture(t).Digest()
	d.Operation = "metadata"
	if _, e := New(d, "site-a", testLane, &testSink{}, 1); e == nil {
		t.Fatal("advanced fresh start accepted without durable authority")
	}
}

func TestDetachedAndAtomicReload(t *testing.T) {
	sink := &testSink{}
	d := loadFixture(t)
	r, e := New(d, "site-a", testLane, sink, 1)
	if e != nil {
		t.Fatal(e)
	}
	original := clone(d)
	d.Routing.JumpSizes[0] = 999
	a, _ := recordsForGroups(r)
	if e := r.Push(a); e != nil {
		t.Fatal(e)
	}
	first := sink.all()[0]
	var stop atomic.Bool
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			record := &rec.RecBytes{Path: []byte("metric.concurrent")}
			for !stop.Load() {
				if e := r.Push(record); e != nil {
					t.Error(e)
					return
				}
			}
		}()
	}
	next := clone(original)
	expected := map[uint64]string{1: original.Digest()}
	for i := 0; i < 8; i++ {
		old := clone(next)
		next.Revision++
		next.Predecessor = old.Digest()
		next.Operation = fmt.Sprintf("metadata-%d", i)
		expected[next.Revision] = next.Digest()
		if e := r.Adopt(next, nil); e != nil {
			t.Fatal(e)
		}
	}
	stop.Store(true)
	wg.Wait()
	next.Stores[0].Write = "mutated:2003"
	if e := r.Adopt(original, nil); e == nil || r.Revision() != 9 {
		t.Fatal("stale reload replaced active map")
	}
	if e := r.Push(a); e != nil {
		t.Fatal(e)
	}
	for _, p := range sink.all() {
		if p.Epoch != 1 || p.Store == "mutated" || p.Revision == 0 || p.Revision > 9 || p.MapDigest != expected[p.Revision] {
			t.Fatal(p)
		}
	}
	if first.MapDigest != original.Digest() {
		t.Fatal("caller mutation leaked into map")
	}
}

func TestSameEpochEndpointEditRejected(t *testing.T) {
	r := newRouter(t, &testSink{}, 1)
	d := loadFixture(t)
	n := clone(d)
	n.Revision++
	n.Predecessor = d.Digest()
	n.Operation = "endpoint"
	n.Stores[0].Write = "replacement:2003"
	if e := r.Adopt(n, nil); e == nil || r.Revision() != 1 {
		t.Fatal("endpoint handoff bypassed epoch")
	}
}

func TestNoRoutingAllocations(t *testing.T) {
	r := newRouter(t, discardSink{}, 1)
	p := &rec.RecBytes{Path: []byte("metric.allocation")}
	_ = r.Push(p)
	if n := testing.AllocsPerRun(1000, func() { _ = r.Push(p) }); n != 0 {
		t.Fatalf("%g allocations/record", n)
	}
}

type discardSink struct{}

func (discardSink) Epochs() bool          { return true }
func (discardSink) Enqueue(Delivery) bool { return true }

var benchmarkSlot int32
var benchmarkRoute *route

func BenchmarkRouting(b *testing.B) {
	for _, mode := range []string{"jump/3", "placement/3/select", "placement/3/push", "placement/3+4/select", "placement/3+4/push"} {
		b.Run(mode, func(b *testing.B) {
			d := loadFixture(b)
			if mode == "placement/3/select" || mode == "placement/3/push" {
				d.Routing.JumpSizes = []uint32{3}
				d.Bootstrap = d.Bootstrap[:1]
				var err error
				d.Groups, err = v2placement.Bootstrap(d.Routing, d.Bootstrap)
				if err != nil {
					b.Fatal(err)
				}
				for i := range d.Groups {
					d.Groups[i].Replicas[0].Producers = []v2placement.Lane{testLane}
				}
			}
			r, err := New(d, "site-a", testLane, discardSink{}, 1)
			if err != nil {
				b.Fatal(err)
			}
			records := make([]*rec.RecBytes, 4096)
			for i := range records {
				records[i] = &rec.RecBytes{Path: []byte(fmt.Sprintf("example.application.worker.%d.requests", i))}
				records[i].PathHash()
			}
			b.ReportAllocs()
			b.ResetTimer()
			var result int32
			var rt *route
			for i := 0; i < b.N; i++ {
				p := records[i%len(records)]
				switch mode {
				case "jump/3":
					result = jump.Hash(p.PathHash(), 3)
				case "placement/3/select", "placement/3+4/select":
					rt, _ = r.current.Load().selectRoute(p)
				default:
					if err := r.Push(p); err != nil {
						b.Fatal(err)
					}
				}
			}
			b.StopTimer()
			benchmarkSlot = result
			benchmarkRoute = rt
		})
	}
}

func TestConcurrentMoveNoOldEpochAfterActivation(t *testing.T) {
	sink := &testSink{}
	r := newRouter(t, sink, 5000)
	a, b := recordsForGroups(r)
	if e := r.Push(a); e != nil {
		t.Fatal(e)
	}
	group := sink.all()[0].Group
	if _, e := r.BeginMove(group); e != nil {
		t.Fatal(e)
	}
	if e := r.Acknowledge(group, 1, 1); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			first := &rec.RecBytes{Path: append([]byte(nil), a.Path...)}
			other := &rec.RecBytes{Path: append([]byte(nil), b.Path...)}
			for j := 0; j < 1000; j++ {
				if e := r.Push(first); e != nil {
					t.Error(e)
					return
				}
				if e := r.Push(other); e != nil {
					t.Error(e)
					return
				}
			}
		}()
	}
	n, acts := moved(t, loadFixture(t), group, 1)
	if e := r.Adopt(n, acts); e != nil {
		t.Fatal(e)
	}
	wg.Wait()
	var epoch, sequence uint64
	count := 0
	for _, d := range sink.all() {
		if d.Group != group {
			if d.Epoch != 1 {
				t.Fatal("unrelated group epoch advanced")
			}
			continue
		}
		count++
		if d.Epoch < epoch {
			t.Fatal("old epoch dispatched after new epoch")
		}
		if d.Epoch > epoch {
			epoch = d.Epoch
			sequence = 0
		}
		sequence++
		if d.Sequence != sequence {
			t.Fatalf("sequence %d want %d", d.Sequence, sequence)
		}
	}
	if count != 4001 || r.Stats.Pending.Load() != 0 {
		t.Fatalf("lost move records: %d", count)
	}
}
