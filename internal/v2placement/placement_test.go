package v2placement

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

func fixture(t *testing.T) Document {
	t.Helper()
	r := Routing{NameVersion: NameVersion, JumpSizes: []uint32{3, 4}, Span: 4096, Namespaces: []Namespace{{ID: "metrics", Prefix: ""}}}
	b := []BootstrapReplica{{ID: "site-a", JumpSize: 3, Slots: []string{"a", "b", "c"}}, {ID: "site-b", JumpSize: 4, Slots: []string{"a", "b", "c", "a"}}}
	groups, e := Bootstrap(r, b)
	if e != nil {
		t.Fatal(e)
	}
	d := Document{Schema: 1, Domain: "example", Revision: 1, Operation: "bootstrap", Routing: r, Bootstrap: b, Groups: groups}
	for _, id := range []string{"a", "b", "c", "d"} {
		d.Stores = append(d.Stores, Store{ID: id, Write: id + ":2003", Read: id + ":8080", Copy: id + ":8081"})
	}
	for i := range d.Groups {
		for j := range d.Groups[i].Replicas {
			d.Groups[i].Replicas[j].Producers = []Lane{{Producer: "relay-1/inc-1", Stream: "lane-0"}, {Producer: "relay-1/inc-1", Stream: "lane-1"}}
		}
	}
	if e = d.Validate(); e != nil {
		t.Fatal(e)
	}
	return d
}

func certificate(t *testing.T, d Document) (Document, Activation) {
	t.Helper()
	next := cloneDocument(d)
	next.Revision++
	next.Predecessor = d.Digest()
	next.Operation = "move-1"
	r := d.Groups[0].Replicas[0]
	n := &next.Groups[0].Replicas[0]
	n.Writer = "d"
	n.Epoch++
	n.Read = append(n.Read, "d")
	h := digest("coherent snapshot")
	roots := Roots{h, h, h, h}
	f := Fence{Group: d.Groups[0].Selector.GroupID(), Replica: r.ID, Store: r.Writer, Epoch: r.Epoch, MapDigest: d.Digest(), Operation: next.Operation, WriterSet: digest(r.Producers), Frontier: Frontier{{Owner: "owner-0", Cursor: 7}, {Owner: "owner-1", Cursor: 8}}, Roots: roots}
	for _, l := range r.Producers {
		f.Ends = append(f.Ends, EndMarker{Lane: l, Final: 5, Durable: 5})
	}
	c := Coverage{Group: f.Group, Replica: f.Replica, Store: n.Writer, Epoch: n.Epoch, MapDigest: next.Digest(), Operation: next.Operation, FenceDigest: f.Digest(), Frontier: append(Frontier(nil), f.Frontier...), Roots: roots, Required: n.Required}
	a := Activation{Fence: f, Coverage: c}
	if e := d.ValidateTransition(next, []Activation{a}); e != nil {
		t.Fatal(e)
	}
	return next, a
}

func TestMapAndReceiptValidation(t *testing.T) {
	d := fixture(t)
	next, a := certificate(t, d)
	for _, tc := range []struct {
		name   string
		change func(*Document)
	}{
		{"missing group", func(d *Document) { d.Groups = d.Groups[1:] }},
		{"duplicate group", func(d *Document) { d.Groups[1] = d.Groups[0] }},
		{"impossible cell", func(d *Document) { d.Groups[0].Selector.Cell = []uint32{0, 1} }},
		{"bootstrap reshuffle", func(d *Document) { d.Groups[0].Replicas[0].Writer = "d" }},
		{"duplicate lane", func(d *Document) { r := &d.Groups[0].Replicas[0]; r.Producers = append(r.Producers, r.Producers[0]) }},
		{"unknown reader", func(d *Document) { d.Groups[0].Replicas[0].Read = []string{"absent"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := cloneDocument(d)
			tc.change(&bad)
			if bad.Validate() == nil {
				t.Fatal("accepted invalid map")
			}
		})
	}
	if d.ValidateTransition(next, nil) == nil {
		t.Fatal("activation without receipts")
	}
	for _, tc := range []struct {
		name   string
		change func(*Activation)
	}{
		{"missing lane", func(a *Activation) { a.Fence.Ends = a.Fence.Ends[:1] }},
		{"lane undrained", func(a *Activation) { a.Fence.Ends[0].Durable-- }},
		{"missing owner", func(a *Activation) { a.Coverage.Frontier = a.Coverage.Frontier[:1] }},
		{"target behind", func(a *Activation) { a.Coverage.Frontier[0].Cursor-- }},
		{"wrong incarnation", func(a *Activation) { a.Coverage.Store = "a" }},
		{"wrong map", func(a *Activation) { a.Coverage.MapDigest = d.Digest() }},
		{"wrong metadata", func(a *Activation) { a.Coverage.Roots.Tombstones = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, _ := json.Marshal(a)
			var bad Activation
			_ = json.Unmarshal(b, &bad)
			tc.change(&bad)
			if d.ValidateTransition(next, []Activation{bad}) == nil {
				t.Fatal("accepted invalid activation")
			}
		})
	}
	stale := cloneDocument(next)
	stale.Revision = d.Revision
	if d.ValidateSuccessor(stale) == nil {
		t.Fatal("accepted same revision different content")
	}
	retired := cloneDocument(next)
	retired.Groups[0].Replicas[0].Read = []string{"d"}
	if d.ValidateSuccessor(retired) == nil {
		t.Fatal("retired historical reader")
	}
}

func TestDecodeRejectsAmbiguity(t *testing.T) {
	d := fixture(t)
	b, _ := json.Marshal(d)
	if _, e := Decode(strings.NewReader(string(b))); e != nil {
		t.Fatal(e)
	}
	for _, b := range []string{strings.Replace(string(b), `"schema":1`, `"schema":1,"schema":1`, 1), string(b) + "{}", strings.Replace(string(b), `"schema":1`, `"unknown":1,"schema":1`, 1)} {
		if _, e := Decode(strings.NewReader(b)); e == nil {
			t.Fatal("ambiguous JSON accepted")
		}
	}
}

func TestGoldenRouting(t *testing.T) {
	b, e := os.ReadFile("testdata/routing-v1.json")
	if e != nil {
		t.Fatal(e)
	}
	var v struct {
		Routing Routing `json:"routing"`
		Vectors []struct {
			Name   string   `json:"name"`
			Group  string   `json:"group"`
			VShard uint32   `json:"vshard"`
			Cell   []uint32 `json:"cell"`
		} `json:"vectors"`
	}
	if e = json.Unmarshal(b, &v); e != nil {
		t.Fatal(e)
	}
	for _, x := range v.Vectors {
		s, shard, e := v.Routing.Select(x.Name)
		if e != nil || shard != x.VShard || s.GroupID() != x.Group {
			t.Fatalf("golden %q: %v %d %v", x.Name, s, shard, e)
		}
		for i, n := range s.Cell {
			if n != x.Cell[i] {
				t.Fatalf("golden legacy slot %q", x.Name)
			}
		}
	}
}

func TestGoldenMapDigest(t *testing.T) {
	b, e := os.ReadFile("testdata/map-v1.json")
	if e != nil {
		t.Fatal(e)
	}
	d, e := Decode(strings.NewReader(string(b)))
	if e != nil {
		t.Fatal(e)
	}
	if d.Digest() != fixture(t).Digest() {
		t.Fatal("map golden differs from contract fixture")
	}
	const expected = "943319b0cc9be1b2061acf9db29fa03bbbe56d24e51112207b06e723d4988354"
	if d.Digest() != expected {
		t.Fatalf("map digest %s", d.Digest())
	}
}

func TestBlackholesAndBootstrapIdentity(t *testing.T) {
	d := fixture(t)
	for i := range d.Stores {
		if d.Stores[i].ID == "a" {
			d.Stores[i] = Store{ID: "a", Blackhole: true}
		}
	}
	for i := range d.Groups {
		for j := range d.Groups[i].Replicas {
			r := &d.Groups[i].Replicas[j]
			if r.Writer == "a" {
				r.Read = nil
				r.CopySources = nil
				r.Producers = nil
			}
		}
	}
	if e := d.Validate(); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 10000; i++ {
		name := fmt.Sprintf("probe.metric.%d", i)
		s, e := d.Select(name, "site-b")
		if e != nil {
			t.Fatal(e)
		}
		slot, _ := LegacySlot(name, 4)
		if s.Replica.Writer != d.Bootstrap[1].Slots[slot] {
			t.Fatal("bootstrap moved ownership")
		}
	}
}

func TestCoverageRequirementsAndFrozenInventory(t *testing.T) {
	d := fixture(t)
	q := Requirement{Source: d.Groups[0].Replicas[0].Writer, Root: digest("historical gap window")}
	d.Groups[0].Replicas[0].Required = []Requirement{q}
	next, a := certificate(t, d)
	a.Coverage.Required = nil
	if d.ValidateTransition(next, []Activation{a}) == nil {
		t.Fatal("missing historical source coverage accepted")
	}
	next = cloneDocument(d)
	next.Revision++
	next.Predecessor = d.Digest()
	next.Operation = "inventory-edit"
	next.Groups[0].Replicas[0].Producers = next.Groups[0].Replicas[0].Producers[:1]
	if d.ValidateSuccessor(next) == nil {
		t.Fatal("old producer silently removed")
	}
}

func cloneDocument(d Document) Document {
	b, _ := json.Marshal(d)
	var n Document
	_ = json.Unmarshal(b, &n)
	return n
}
