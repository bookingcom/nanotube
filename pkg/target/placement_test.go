package target

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/bookingcom/nanotube/internal/placement"
	"github.com/bookingcom/nanotube/internal/v2placement"
	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/metrics"
	"github.com/bookingcom/nanotube/pkg/rec"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.uber.org/zap"
)

func placementDocument(t testing.TB) v2placement.Document {
	t.Helper()
	d, e := readPlacement("../../internal/v2placement/testdata/map-v1.json")
	if e != nil {
		t.Fatal(e)
	}
	return d
}
func placedTestCluster(t testing.TB) (*Cluster, []*fakeQueued) {
	t.Helper()
	hosts := []*fakeQueued{{id: nextQueueID()}, {id: nextQueueID()}, {id: nextQueueID()}, {id: nextQueueID()}}
	targets := map[string]Target{}
	for i, id := range []string{"a", "b", "c", "d"} {
		targets[id] = hosts[i]
	}
	r, e := placement.New(placementDocument(t), "site-a")
	if e != nil {
		t.Fatal(e)
	}
	return &Cluster{Type: conf.PlacementCluster, placement: r, placementTargets: targets}, hosts
}
func TestPlacementScalarAndBatchMatchLegacy(t *testing.T) {
	for _, batching := range []bool{false, true} {
		t.Run(fmt.Sprint(batching), func(t *testing.T) {
			cl, got := placedTestCluster(t)
			want := []*fakeQueued{{id: nextQueueID()}, {id: nextQueueID()}, {id: nextQueueID()}}
			legacy := &Cluster{Type: conf.JumpCluster, Hosts: []Target{want[0], want[1], want[2]}}
			ms := metrics.New()
			batch := NewBatch()
			records := testRecs(10000)
			for _, r := range records {
				var e error
				if batching {
					e = cl.PushBytesBatch(r, ms, batch)
				} else {
					e = cl.PushBytes(r, ms)
				}
				if e != nil {
					t.Fatal(e)
				}
				if e = legacy.PushBytes(r, ms); e != nil {
					t.Fatal(e)
				}
			}
			batch.Flush()
			for i := range want {
				if !reflect.DeepEqual(got[i].got, want[i].got) {
					t.Fatalf("host %d changed legacy owner or order", i)
				}
			}
			if len(got[3].got) != 0 {
				t.Fatal("bootstrap sent to future owner")
			}
		})
	}
}

func TestPlacementReloadRetainsGoodMapAndMetersFailures(t *testing.T) {
	cl, _ := placedTestCluster(t)
	ms := metrics.New()
	cl.Name = "example"
	cl.placementMetrics = ms.Placement
	ms.Placement.Attach(cl.Name, &cl.placement.Stats.Rejected)
	cl.placementPath = filepath.Join(t.TempDir(), "placement.json")
	write := func(b []byte) {
		t.Helper()
		if e := os.WriteFile(cl.placementPath, b, 0600); e != nil {
			t.Fatal(e)
		}
	}
	write([]byte(`{"schema":1,"schema":1}`))
	if e := cl.ReloadPlacement(); e == nil {
		t.Fatal("bad map accepted")
	}
	d := placementDocument(t)
	d.Revision++

	old := placementDocument(t)
	d.Predecessor = old.Digest()
	d.Operation = "metadata"
	b, e := json.Marshal(d)
	if e != nil {
		t.Fatal(e)
	}
	write(b)
	if e := cl.ReloadPlacement(); e != nil || cl.placement.Revision() != 2 {
		t.Fatalf("reload: %v", e)
	}
	if e := cl.ReloadPlacement(); e == nil {
		t.Fatal("stale map accepted")
	}
	registry := prometheus.NewRegistry()
	registry.MustRegister(ms.Placement)
	metricFamilies, e := registry.Gather()
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, family := range metricFamilies {
		if family.GetName() == "nanotube_placement_reload_rejections_total" {
			found = true
			if n := family.Metric[0].Counter.GetValue(); n != 2 {
				t.Fatalf("rejections %g", n)
			}
		}
	}
	if !found {
		t.Fatal("missing rejection metric")
	}
	if n := testutil.CollectAndCount(ms.Placement); n != 1 {
		t.Fatalf("metric count %d", n)
	}
}

func TestPlacementConfigBindsExplicitEndpoints(t *testing.T) {
	path := filepath.Join(t.TempDir(), "placement.json")
	b, e := os.ReadFile("../../internal/v2placement/testdata/map-v1.json")
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(path, b, 0600); e != nil {
		t.Fatal(e)
	}
	cc := conf.Cluster{Name: "example", Type: conf.PlacementCluster, PlacementMap: path, PlacementReplica: "site-a"}
	for _, id := range []string{"d", "b", "a", "c"} {
		cc.Hosts = append(cc.Hosts, conf.Host{Name: id, Port: 2003})
	}
	// Constructors do not connect; the send loops are never launched by this test.
	cls, e := NewClusters(&conf.Main{HostQueueSize: 16}, &conf.Clusters{Cluster: []conf.Cluster{cc}}, zap.NewNop(), metrics.New())
	if e != nil {
		t.Fatal(e)
	}
	cl := cls[cc.Name]
	if e = cl.PushBytes(&rec.RecBytes{Path: []byte("metric.path")}, metrics.New()); e != nil {
		t.Fatal(e)
	}
	cc.Hosts = cc.Hosts[:3]
	if _, e = NewClusters(&conf.Main{HostQueueSize: 16}, &conf.Clusters{Cluster: []conf.Cluster{cc}}, zap.NewNop(), metrics.New()); e == nil {
		t.Fatal("missing map endpoint accepted")
	}

}

func TestPlacementCannotUseStaticTypedHostPlan(t *testing.T) {
	cl, _ := placedTestCluster(t)
	plans := &TypedRoutes{}
	if plans.Resolve([]ClusterTarget{cl}, 1) != nil {
		t.Fatal("placement pinned a static host plan across map reload")
	}
}

// nextMap models the controller's reviewed successor, independent of relay I/O.
func nextMap(d v2placement.Document) v2placement.Document {
	data, _ := json.Marshal(d)
	var n v2placement.Document
	_ = json.Unmarshal(data, &n)
	n.Revision++
	n.Predecessor = d.Digest()
	n.Operation = fmt.Sprintf("step-%d", n.Revision)
	return n
}
func TestPlacementMovingScalarBatchAndFinalFlip(t *testing.T) {
	for _, batching := range []bool{false, true} {
		t.Run(fmt.Sprint(batching), func(t *testing.T) {
			cl, hosts := placedTestCluster(t)
			d := placementDocument(t)
			moving := nextMap(d)
			moving.Groups[0].Replicas[0].MovingTo = "d"
			if e := cl.placement.Adopt(moving); e != nil {
				t.Fatal(e)
			}
			records := testRecs(10000)
			batch := NewBatch()
			counts := map[string]int{}
			movingCount := 0
			for _, r := range records {
				a, b := cl.placement.Owners(r)
				counts[a]++
				if b != "" {
					counts[b]++
					movingCount++
				}
				var err error
				if batching {
					err = cl.PushBytesBatch(r, nil, batch)
				} else {
					err = cl.PushBytes(r, nil)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			// Records already staged retain their selected destinations over a map flip.
			final := nextMap(moving)
			rep := &final.Groups[0].Replicas[0]
			rep.Writer = "d"
			rep.MovingTo = ""
			rep.Epoch++
			rep.Read = append(rep.Read, "d")
			if e := cl.placement.Adopt(final); e != nil {
				t.Fatal(e)
			}
			batch.Flush()
			if movingCount == 0 {
				t.Fatal("no moving records exercised")
			}
			total := 0
			for i, id := range []string{"a", "b", "c", "d"} {
				if len(hosts[i].got) != counts[id] {
					t.Fatalf("%s: got %d want %d", id, len(hosts[i].got), counts[id])
				}
				total += len(hosts[i].got)
			}
			t.Logf("groups=%d input=%d moving=%d deliveries=%d extra=%.4f%%", len(d.Groups), len(records), movingCount, total, 100*float64(movingCount)/float64(len(records)))
			if total != len(records)+movingCount {
				t.Fatal("wrong fan-out")
			}
			for _, h := range hosts {
				h.got = nil
			}
			for _, r := range records {
				a, b := cl.placement.Owners(r)
				if b != "" {
					t.Fatal("still dual writing")
				}
				counts[a] = 0
				_ = cl.PushBytes(r, nil)
			}
			if len(hosts[3].got) != movingCount {
				t.Fatal("final destination differs")
			}
			restarted, e := placement.New(final, "site-a")
			if e != nil {
				t.Fatal(e)
			}
			for _, r := range records {
				a, b := cl.placement.Owners(r)
				c, d := restarted.Owners(r)
				if a != c || b != d {
					t.Fatal("restart changed routing")
				}
			}
		})
	}
}
func TestPlacementZeroAllocations(t *testing.T) {
	for _, moving := range []bool{false, true} {
		d := placementDocument(t)
		if moving {
			d = nextMap(d)
			for i := range d.Groups {
				d.Groups[i].Replicas[0].MovingTo = "d"
			}
		}
		r, e := placement.New(d, "site-a")
		if e != nil {
			t.Fatal(e)
		}
		cl := &Cluster{Type: conf.PlacementCluster, placement: r, placementTargets: map[string]Target{}}
		for _, id := range []string{"a", "b", "c", "d"} {
			cl.placementTargets[id] = &campaignQueuedTarget{id: nextQueueID()}
		}
		record := &rec.RecBytes{Path: []byte("example.metric")}
		record.PathHash()
		for _, batching := range []bool{false, true} {
			batch := NewBatch()
			n := testing.AllocsPerRun(1000, func() {
				if batching {
					_ = cl.PushBytesBatch(record, nil, batch)
					batch.Flush()
				} else {
					_ = cl.PushBytes(record, nil)
				}
			})
			if n != 0 {
				t.Fatalf("moving=%t batch=%t: %g allocations", moving, batching, n)
			}
		}
	}
}
