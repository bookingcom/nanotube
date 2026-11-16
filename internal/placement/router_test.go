package placement

import (
	"encoding/json"
	"fmt"
	"github.com/bookingcom/nanotube/internal/v2placement"
	"github.com/bookingcom/nanotube/pkg/rec"
	"github.com/dgryski/go-jump"
	"os"
	"sync"
	"testing"
)

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
		d.Groups[i].Replicas[0].Producers = []v2placement.Lane{{Producer: "relay-1/inc-1", Stream: "lane-0"}}
	}
	r, e := New(d, "site-a")
	if e != nil {
		t.Fatal(e)
	}
	for _, v := range fixture.Vectors {
		record := &rec.RecBytes{Path: []byte(v.Name)}
		got, vshard := r.current.Load().selectRoute(record)
		if got.group != v.Group || vshard != v.VShard || got.replica.Writer != d.Bootstrap[0].Slots[v.Cell[0]] {
			t.Fatalf("%q: %+v", v.Name, got)
		}
	}
}

func TestLegacyOwnerEquality(t *testing.T) {
	d := loadFixture(t)
	for _, replica := range d.Bootstrap {
		r, e := New(d, replica.ID)
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

func successor(d v2placement.Document) v2placement.Document {
	data, _ := json.Marshal(d)
	var n v2placement.Document
	_ = json.Unmarshal(data, &n)
	n.Revision++
	n.Predecessor = d.Digest()
	n.Operation = fmt.Sprintf("step-%d", n.Revision)
	return n
}
func TestMovingValidation(t *testing.T) {
	d := loadFixture(t)
	for _, dest := range []string{"missing", d.Groups[0].Replicas[0].Writer} {
		n := successor(d)
		n.Groups[0].Replicas[0].MovingTo = dest
		r, _ := New(d, "site-a")
		if e := r.Adopt(n); e == nil {
			t.Fatalf("accepted destination %s", dest)
		}
		if r.Revision() != 1 {
			t.Fatal("bad map published")
		}
	}
	r, _ := New(d, "site-a")
	bypass := successor(d)
	bypass.Groups[0].Replicas[0].Writer = "d"
	bypass.Groups[0].Replicas[0].Epoch++
	bypass.Groups[0].Replicas[0].Read = append(bypass.Groups[0].Replicas[0].Read, "d")
	if e := r.Adopt(bypass); e == nil {
		t.Fatal("skipped dual write")
	}
	moving := successor(d)
	moving.Groups[0].Replicas[0].MovingTo = "d"
	if e := r.Adopt(moving); e != nil {
		t.Fatal(e)
	}
	if e := r.Adopt(d); e == nil {
		t.Fatal("accepted stale map")
	}
	bad := successor(moving)
	bad.Groups[0].Replicas[0].MovingTo = "b"
	if e := r.Adopt(bad); e == nil {
		t.Fatal("retargeted active move")
	}
	bad = successor(moving)
	bad.Stores[0].Write = "changed:2003"
	if e := r.Adopt(bad); e == nil {
		t.Fatal("changed immutable endpoint")
	}
	// Caller mutation must not alter the published routing table.
	moving.Groups[0].Replicas[0].MovingTo = "b"
	if r.current.Load().routes[0].replica.MovingTo != "d" {
		t.Fatal("caller mutated map")
	}
}

func TestConcurrentMapAdoption(t *testing.T) {
	d := loadFixture(t)
	r, e := New(d, "site-a")
	if e != nil {
		t.Fatal(e)
	}
	moving := successor(d)
	moving.Groups[0].Replicas[0].MovingTo = "d"
	final := successor(moving)
	p := &final.Groups[0].Replicas[0]
	p.Writer = "d"
	p.MovingTo = ""
	p.Epoch++
	p.Read = append(p.Read, "d")
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			record := &rec.RecBytes{Path: []byte("metric.0")}
			for range 100000 {
				a, b := r.Owners(record)
				if a == "" || a == b {
					t.Errorf("invalid owner pair %q %q", a, b)
					return
				}
			}
		}()
	}
	if e = r.Adopt(moving); e != nil {
		t.Fatal(e)
	}
	if e = r.Adopt(final); e != nil {
		t.Fatal(e)
	}
	wg.Wait()
}
