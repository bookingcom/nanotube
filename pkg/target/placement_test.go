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
	r, e := placement.New(placementDocument(t), "site-a", v2placement.Lane{Producer: "relay-1/inc-1", Stream: "lane-0"}, &legacyPlacementSink{targets: targets}, 2)
	if e != nil {
		t.Fatal(e)
	}
	return &Cluster{Type: conf.PlacementCluster, placement: r}, hosts
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
	ms.Placement.Attach(cl.Name, &cl.placement.Stats)
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
	cls := Clusters{cl.Name: cl}
	if !cls.HasPlacement() {
		t.Fatal("missing placement restart guard")
	}
	cls.RejectPlacementRestart()
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
			if n := family.Metric[0].Counter.GetValue(); n != 3 {
				t.Fatalf("rejections %g", n)
			}
		}
	}
	if !found {
		t.Fatal("missing rejection metric")
	}
	if n := testutil.CollectAndCount(ms.Placement); n != 8 {
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
	cc := conf.Cluster{Name: "example", Type: conf.PlacementCluster, PlacementMap: path, PlacementReplica: "site-a", PlacementProducer: "relay-1/inc-1", PlacementStream: "lane-0", PlacementHoldLimit: 2, PlacementLegacyWire: true}
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
	if _, e = cl.placement.BeginMove(placementDocument(t).Groups[0].Selector.GroupID()); e != placement.ErrTransport {
		t.Fatalf("legacy transport permitted move: %v", e)
	}
	cc.Hosts = cc.Hosts[:3]
	if _, e = NewClusters(&conf.Main{HostQueueSize: 16}, &conf.Clusters{Cluster: []conf.Cluster{cc}}, zap.NewNop(), metrics.New()); e == nil {
		t.Fatal("missing map endpoint accepted")
	}
	cc.PlacementLegacyWire = false
	if _, e = NewClusters(&conf.Main{}, &conf.Clusters{Cluster: []conf.Cluster{cc}}, zap.NewNop(), metrics.New()); e == nil {
		t.Fatal("implicit legacy downgrade accepted")
	}
}

func TestPlacementCannotUseStaticTypedHostPlan(t *testing.T) {
	cl, _ := placedTestCluster(t)
	plans := &TypedRoutes{}
	if plans.Resolve([]ClusterTarget{cl}, 1) != nil {
		t.Fatal("placement pinned a static host plan across map reload")
	}
}

func TestPlacementDurableConfiguredScalarBatchAndRestart(t *testing.T) {
	root := t.TempDir()
	mapPath := filepath.Join(root, "map.json")
	keyPath := filepath.Join(root, "key")
	data, e := os.ReadFile("../../internal/v2placement/testdata/map-v1.json")
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(mapPath, data, 0600); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(keyPath, []byte("0123456789abcdef0123456789abcdef"), 0600); e != nil {
		t.Fatal(e)
	}
	cc := conf.Cluster{Name: "example", Type: conf.PlacementCluster, PlacementMap: mapPath, PlacementReplica: "site-a", PlacementProducer: "relay-1/inc-1", PlacementStream: "lane-0", PlacementHoldLimit: 2, PlacementSpool: filepath.Join(root, "spool"), PlacementMaxRecords: 2, PlacementMaxBytes: 4 << 20, PlacementKeyFile: keyPath}
	ms := metrics.New()
	clusters, e := NewClusters(&conf.Main{}, &conf.Clusters{Cluster: []conf.Cluster{cc}}, zap.NewNop(), ms)
	if e != nil {
		t.Fatal(e)
	}
	cl := clusters[cc.Name]
	if cl.placementRuntime == nil || len(cl.Hosts) != 0 {
		t.Fatal("durable config used legacy host queues")
	}
	p := rec.NewTyped([]byte("example.metric"), 1, 100)
	if e = cl.PushBytes(p, ms); e != nil {
		t.Fatal(e)
	}
	selector, _, e := placementDocument(t).Routing.Select(string(p.Path))
	if e != nil {
		t.Fatal(e)
	}
	barrier, e := cl.BeginPlacementMove(selector.GroupID())
	if e != nil {
		t.Fatal(e)
	}
	if barrier.Final != 1 {
		t.Fatal("wrong persisted final", barrier)
	}
	if _, drained, e := cl.PlacementDrainStatus(selector.GroupID()); e != nil || drained {
		t.Fatal("unacknowledged queue appears drained", e)
	}
	batch := NewBatch()
	if e = cl.PushBytesBatch(p, ms, batch); e != nil {
		t.Fatal(e)
	}
	if e = batch.Flush(); e != nil {
		t.Fatal(e)
	}
	if e = cl.PushBytes(p, ms); e != placement.ErrHeldFull {
		t.Fatal("configured spool not bounded", e)
	}
	if e = cl.placementRuntime.output.Close(); e != nil {
		t.Fatal(e)
	}
	clusters, e = NewClusters(&conf.Main{}, &conf.Clusters{Cluster: []conf.Cluster{cc}}, zap.NewNop(), metrics.New())
	if e != nil {
		t.Fatal(e)
	}
	cl = clusters[cc.Name]
	defer cl.placementRuntime.output.Close()
	if cl.placement.Stats.Queued.Load() != 1 || cl.placement.Stats.Pending.Load() != 1 {
		t.Fatal("configured restart lost old dispatch or held suffix")
	}
}
