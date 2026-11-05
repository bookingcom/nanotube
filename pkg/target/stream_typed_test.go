package target

import (
	"fmt"
	"math"
	"math/rand"
	"reflect"
	"testing"

	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/metrics"
	"github.com/bookingcom/nanotube/pkg/rec"
	codec "github.com/bookingcom/nanotube/pkg/streamcodec"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.uber.org/zap"
)

func typedTestClusters(t *testing.T) ([]ClusterTarget, []*HostMTCP) {
	t.Helper()
	cfg := conf.MakeDefault()
	cfg.TCPInitialConnCheck = false
	cfg.StreamDictionaryBytes = 16 << 20
	cfg.HostQueueSize = 5000
	ms := metrics.New()
	var clusters []ClusterTarget
	var hosts []*HostMTCP
	for replica := range 3 {
		cl := &Cluster{Name: fmt.Sprint(replica), Type: conf.JumpCluster}
		for i := range 6 {
			host := ConstructHostMTCP(cl.Name, cfg, conf.Host{Name: fmt.Sprintf("127.0.%d.%d", replica, i+1), MTCP: 5, Stream: true}, zap.NewNop(), ms)
			cl.Hosts = append(cl.Hosts, host)
			hosts = append(hosts, host)
		}
		clusters = append(clusters, cl)
	}
	return clusters, hosts
}

func TestTypedRoutesMatchRegularRoutesFieldsAndAccounting(t *testing.T) {
	fast, fastHosts := typedTestClusters(t)
	slow, slowHosts := typedTestClusters(t)
	var plans TypedRoutes
	typed, regular := NewBatch(), NewBatch()
	var tokens [31]uint64
	for i := range 1500 {
		index := i % 31
		path := []byte(fmt.Sprintf("example.metric.%d", index))
		r := rec.NewTyped(path, math.Float64frombits(uint64(i)*0x9e3779b97f4a7c15), int64(i)-1000)
		hash := r.PathHash()
		plan := plans.Resolve(fast, hash)
		if plan == nil {
			t.Fatal("eligible route not selected")
		}
		value, ts, _ := r.TypedValues()
		plan.Push(path, hash, value, ts, &tokens[index], typed)
		for _, cl := range slow {
			if err := cl.(*Cluster).PushBytesBatch(r, slowHosts[0].Ms, regular); err != nil {
				t.Fatal(err)
			}
		}
	}
	typed.Flush()
	regular.Flush()
	for i, host := range fastHosts {
		host.q.close()
		slowHosts[i].q.close()
		var got []streamPoint
		var want []*rec.RecBytes
		for c := host.q.pop(); c != nil; c = host.q.pop() {
			if c.points == nil {
				t.Fatal("typed route queued record objects")
			}
			got = append(got, c.points[:c.n]...)
			host.q.recycle(c)
		}
		for c := slowHosts[i].q.pop(); c != nil; c = slowHosts[i].q.pop() {
			want = append(want, c.recs[:c.n]...)
			slowHosts[i].q.recycle(c)
		}
		if len(got) != len(want) {
			t.Fatalf("host %d count %d != %d", i, len(got), len(want))
		}
		for j, p := range got {
			v, ts, _ := want[j].TypedValues()
			if string(p.name) != string(want[j].Path) || math.Float64bits(p.value) != math.Float64bits(v) || p.timestamp != ts || p.plainBytes != want[j].PlainSize() {
				t.Fatalf("host %d record %d differs", i, j)
			}
		}
		if host.q.output.pendingRecords != slowHosts[i].q.output.pendingRecords || host.q.output.pendingBytes != slowHosts[i].q.output.pendingBytes {
			t.Fatal("pending accounting differs")
		}
	}
	if plans.Resolve([]ClusterTarget{&Cluster{Type: conf.StickyCluster}}, 1) != nil {
		t.Fatal("dynamic route admitted")
	}
	if plans.Resolve([]ClusterTarget{&Cluster{Type: conf.BlackholeCluster}}, 1) != nil {
		t.Fatal("blackhole bypassed")
	}
}

func TestTypedQueueSharesCapacityFIFOAndClearsOwners(t *testing.T) {
	q := newRecQueue(2)
	r := rec.NewTyped([]byte("plain"), 1, 1)
	if !q.pushOne(r) {
		t.Fatal("record rejected")
	}
	p := streamPoint{name: []byte("typed"), value: 2, timestamp: 3, plainBytes: 12}
	if dropped := q.pushTyped([]*streamPoint{&p, &p}); dropped != 1 {
		t.Fatalf("dropped %d", dropped)
	}
	q.close()
	first := q.pop()
	if first.points != nil || first.recs[0] != r {
		t.Fatal("FIFO changed")
	}
	q.recycle(first)
	second := q.pop()
	if second.points == nil || string(second.points[0].name) != "typed" {
		t.Fatal("typed FIFO changed")
	}
	owner := second.points
	q.recycle(second)
	if owner[0].name != nil {
		t.Fatal("recycled chunk retains names")
	}
	if q.pop() != nil {
		t.Fatal("close did not drain")
	}
	if dropped := q.pushTyped([]*streamPoint{&p}); dropped != 1 {
		t.Fatal("closed queue accepted point")
	}
}

func TestMixedBatchPreservesAtomicAdmissionAndDropIdentity(t *testing.T) {
	_, hosts := typedTestClusters(t)
	host := hosts[0]
	host.q.limit = 2
	b := NewBatch()
	first := streamPoint{name: []byte("first"), value: 1, timestamp: 1, plainBytes: 10}
	third := streamPoint{name: []byte("third"), value: 3, timestamp: 3, plainBytes: 10}
	b.addTyped(host, &first)
	b.add(host, rec.NewTyped([]byte("second"), 2, 2))
	b.addTyped(host, &third)
	b.Flush()
	host.q.close()
	var names []string
	for c := host.q.pop(); c != nil; c = host.q.pop() {
		for _, r := range c.recs[:c.n] {
			names = append(names, string(r.Path))
		}
		host.q.recycle(c)
	}
	if len(names) != 2 || names[0] != "first" || names[1] != "second" {
		t.Fatalf("mixed admission reordered records: %v", names)
	}
}

func TestTypedFailedAdmissionHintRemainsPointLocal(t *testing.T) {
	route, _ := typedTestClusters(t)
	var plans TypedRoutes
	p := plans.Resolve(route, 1)
	table, err := codec.NewNameTable(4096, false)
	if err != nil {
		t.Fatal(err)
	}
	p.table = table
	for i := 0; i < 1000; i++ {
		token := table.Resolve([]byte(fmt.Sprint(i)), uint64(i))
		if uint32(token) == 0 {
			break
		}
		if !table.Acquire(token, uint64(i)) {
			t.Fatal("could not pin shared identity")
		}
		if i == 999 {
			t.Fatal("table did not reach pressure")
		}
	}
	var source uint64
	b := NewBatch()
	p.Push([]byte("new.name"), 3000, 1, 2, &source, b)
	if source != 0 {
		t.Fatal("failed admission escaped into retained input identity")
	}
	if len(b.typed) != 1 || uint32(b.typed[0].global) != 0 || b.typed[0].global == 0 {
		t.Fatal("point-local failure hint missing")
	}
}

// A paused consumer makes overflow deterministic. Compare the entire accepted
// ledger and drop/byte accounting to the unchanged legacy publication path.
func TestTypedAdmissionDifferential(t *testing.T) {
	type row struct {
		Name      string
		Bits      uint64
		Timestamp int64
	}
	snapshot := func(q *recQueue) []row {
		q.close()
		var rows []row
		for c := q.pop(); c != nil; c = q.pop() {
			if c.points != nil {
				for _, p := range c.points[:c.n] {
					rows = append(rows, row{string(p.name), math.Float64bits(p.value), p.timestamp})
				}
			} else {
				for _, r := range c.recs[:c.n] {
					v, ts, _ := r.TypedValues()
					rows = append(rows, row{string(r.Path), math.Float64bits(v), ts})
				}
			}
			q.recycle(c)
		}
		return rows
	}
	for _, seed := range []int64{1, 7, 42} {
		for _, capacity := range []int{2, 17, 257} {
			for _, mixed := range []bool{false, true} {
				t.Run(fmt.Sprintf("seed%d/cap%d/mixed%v", seed, capacity, mixed), func(t *testing.T) {
					_, a := typedTestClusters(t)
					_, b := typedTestClusters(t)
					old, next := a[0], b[0]
					old.q.limit = capacity
					next.q.limit = capacity
					reference, candidate := NewBatch(), NewBatch()
					random := rand.New(rand.NewSource(seed))
					for i := range 1000 {
						name := []byte(fmt.Sprintf("example.%d.%d", i%31, i))
						value := math.Float64frombits(random.Uint64())
						ts := int64(random.Uint64())
						r := rec.NewTyped(name, value, ts)
						reference.add(old, r)
						if mixed && i%3 == 1 {
							candidate.add(next, r)
						} else {
							p := &streamPoint{name: name, hash: r.PathHash(), value: value, timestamp: ts, plainBytes: r.PlainSize()}
							candidate.addTyped(next, p)
						}
					}
					reference.Flush()
					candidate.Flush()
					got, want := snapshot(next.q), snapshot(old.q)
					if !reflect.DeepEqual(got, want) {
						t.Fatalf("accepted point ledger differs: %d / %d", len(got), len(want))
					}
					if testutil.ToFloat64(old.throttledTotal) != testutil.ToFloat64(next.throttledTotal) {
						t.Fatal("drop counts differ")
					}
					if old.q.output.pendingRecords != next.q.output.pendingRecords || old.q.output.pendingBytes != next.q.output.pendingBytes {
						t.Fatal("accepted-byte accounting differs")
					}
				})
			}
		}
	}
}
