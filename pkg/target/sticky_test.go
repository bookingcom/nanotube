package target

import (
	"fmt"
	"hash/fnv"
	"net"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/metrics"
	"github.com/bookingcom/nanotube/pkg/rec"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.uber.org/zap"
)

func stickyTestCluster(t testing.TB, ports []uint16, limit uint64) (*Cluster, *metrics.Prom) {
	t.Helper()
	cfg := conf.MakeDefault()
	cfg.TCPInitialConnCheck = false
	cfg.HostQueueSize = limit
	cc := conf.Cluster{Name: "test", Type: conf.StickyCluster}
	for _, port := range ports {
		cc.Hosts = append(cc.Hosts, conf.Host{Name: "127.0.0.1", Port: port})
	}
	ms := metrics.New()
	cls, err := NewClusters(&cfg, &conf.Clusters{Cluster: []conf.Cluster{cc}}, zap.NewNop(), ms)
	if err != nil {
		t.Fatal(err)
	}
	return cls[cc.Name], ms
}

func stickyEndpoint(q *stickyQueue) string { return fmt.Sprint(q.host.target) }

func TestStickyMembership(t *testing.T) {
	base, _ := stickyTestCluster(t, []uint16{2001, 2002, 2003, 2004, 2005}, 1000)
	reordered, _ := stickyTestCluster(t, []uint16{2005, 2003, 2001, 2004, 2002}, 1000)
	removed, _ := stickyTestCluster(t, []uint16{2001, 2002, 2004, 2005}, 1000)
	added, _ := stickyTestCluster(t, []uint16{2001, 2002, 2003, 2004, 2005, 2006}, 1000)
	movesRemove, movesAdd := 0, 0
	for _, r := range testRecs(30000) {
		a := stickyEndpoint(base.sticky.route(r))
		if b := stickyEndpoint(reordered.sticky.route(r)); a != b {
			t.Fatalf("order changes %s: %s -> %s", r.Path, a, b)
		}
		if b := stickyEndpoint(removed.sticky.route(r)); a != b {
			movesRemove++
			if a != "127.0.0.1:2003" {
				t.Fatalf("removal moves unrelated name: %s -> %s", a, b)
			}
		}
		if b := stickyEndpoint(added.sticky.route(r)); a != b {
			movesAdd++
			if b != "127.0.0.1:2006" {
				t.Fatalf("addition moves name between existing hosts: %s -> %s", a, b)
			}
		}
	}
	if movesRemove < 5400 || movesRemove > 6600 || movesAdd < 4400 || movesAdd > 5600 {
		t.Fatalf("unexpected movement: remove=%d add=%d of 30000", movesRemove, movesAdd)
	}
	t.Logf("membership movement /30000: remove=%d add=%d", movesRemove, movesAdd)
}

func TestStickyFailoverAndRecovery(t *testing.T) {
	cl, ms := stickyTestCluster(t, []uint16{2001, 2002, 2003, 2004}, 1000)
	rs := testRecs(1000)
	before := make([]string, len(rs))
	for i, r := range rs {
		before[i] = stickyEndpoint(cl.sticky.route(r))
	}
	failed := cl.Hosts[1].(*HostTCP)
	failed.setAvailability(false)
	now := time.Now()
	cl.sticky.updateHealth(now)
	for i, r := range rs {
		after := stickyEndpoint(cl.sticky.route(r))
		if after == failed.String() {
			t.Fatal("selected failed host")
		}
		if before[i] != failed.String() && before[i] != after {
			t.Fatal("unrelated name moved on failure")
		}
	}
	if testutil.ToFloat64(ms.StickyUnhealthy.WithLabelValues("test")) != 1 {
		t.Fatal("missing unhealthy gauge")
	}
	failed.setAvailability(true)
	cl.sticky.updateHealth(now.Add(time.Second))
	cl.sticky.updateHealth(now.Add(2 * time.Second))
	if !cl.sticky.hosts[1].blocked.Load() {
		t.Fatal("recovered before hysteresis")
	}
	// A second failure must restart the healthy interval.
	failed.setAvailability(false)
	cl.sticky.updateHealth(now.Add(2500 * time.Millisecond))
	failed.setAvailability(true)
	cl.sticky.updateHealth(now.Add(3 * time.Second))
	cl.sticky.updateHealth(now.Add(4 * time.Second))
	if !cl.sticky.hosts[1].blocked.Load() {
		t.Fatal("flap did not restart hysteresis")
	}
	cl.sticky.updateHealth(now.Add(5 * time.Second))
	for i, r := range rs {
		if before[i] != stickyEndpoint(cl.sticky.route(r)) {
			t.Fatal("did not return to primary")
		}
	}
	if testutil.ToFloat64(ms.StickyUnhealthy.WithLabelValues("test")) != 0 {
		t.Fatal("unhealthy gauge did not recover")
	}
}

func TestStickyQueueFailover(t *testing.T) {
	for _, batch := range []bool{false, true} {
		t.Run(fmt.Sprint("batch=", batch), func(t *testing.T) {
			cl, ms := stickyTestCluster(t, []uint16{2001, 2002, 2003}, 2)
			b := NewBatch()
			for i := 0; i < 8; i++ {
				r := &rec.RecBytes{Path: []byte("same.name"), Val: []byte(fmt.Sprint(i)), Time: []byte("1")}
				var err error
				if batch {
					err = cl.PushBytesBatch(r, ms, b)
				} else {
					err = cl.PushBytes(r, ms)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			b.Flush()
			got := make(map[string]bool)
			records, failovers := 0.0, 0.0
			for _, h := range cl.sticky.hosts {
				h.q.close()
				for c := h.q.pop(); c != nil; c = h.q.pop() {
					for _, r := range c.recs[:c.n] {
						if got[string(r.Val)] {
							t.Fatal("duplicate queue admission")
						}
						got[string(r.Val)] = true
					}
					h.q.recycle(c)
				}
				records += testutil.ToFloat64(h.records)
				failovers += testutil.ToFloat64(h.failovers)
			}
			if len(got) != 6 || records != 6 || failovers != 4 || testutil.ToFloat64(ms.ThrottledHostsTotal) != 2 {
				t.Fatalf("received=%d records=%v failovers=%v drops=%v", len(got), records, failovers, testutil.ToFloat64(ms.ThrottledHostsTotal))
			}
		})
	}
}

func TestStickyAllDownRetainsRecords(t *testing.T) {
	cl, ms := stickyTestCluster(t, []uint16{2001, 2002, 2003}, 1000)
	for _, h := range cl.Hosts {
		h.(*HostTCP).setAvailability(false)
	}
	for _, r := range testRecs(100) {
		if err := cl.PushBytes(r, ms); err != nil {
			t.Fatal(err)
		}
	}
	total := 0
	for _, h := range cl.sticky.hosts {
		total += queueLen(h.q)
	}
	if total != 100 {
		t.Fatalf("all-down queues contain %d, want 100", total)
	}
}

func TestStickyBatchParity(t *testing.T) {
	direct, ms := stickyTestCluster(t, []uint16{2001, 2002, 2003}, 1000)
	batch, bs := stickyTestCluster(t, []uint16{2001, 2002, 2003}, 1000)
	b := NewBatch()
	for _, r := range testRecs(500) {
		if err := direct.PushBytes(r, ms); err != nil {
			t.Fatal(err)
		}
		if err := batch.PushBytesBatch(r, bs, b); err != nil {
			t.Fatal(err)
		}
	}
	b.Flush()
	drain := func(q *recQueue) []string {
		q.close()
		var out []string
		for c := q.pop(); c != nil; c = q.pop() {
			for _, r := range c.recs[:c.n] {
				out = append(out, string(r.Path))
			}
			q.recycle(c)
		}
		return out
	}
	for i, h := range direct.sticky.hosts {
		if !reflect.DeepEqual(drain(h.q), drain(batch.sticky.hosts[i].q)) {
			t.Fatal("batch/direct records differ")
		}
	}
}

func TestStickyNormalizedPathAndRanking(t *testing.T) {
	cl, _ := stickyTestCluster(t, []uint16{2001, 2002, 2003}, 1000)
	a, err := rec.ParseRec([]byte("a..b 1 1"), true, false, time.Now, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	b, err := rec.ParseRec([]byte("a.b 2 2"), true, false, time.Now, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	if string(a.Path) != string(b.Path) || stickyEndpoint(cl.sticky.route(a)) != stickyEndpoint(cl.sticky.route(b)) {
		t.Fatal("normalized names do not agree")
	}
	// Pin independently calculated placement vectors, not just repeatability.
	for name, want := range map[string]string{"a.b": "127.0.0.1:2001", "metric.requests": "127.0.0.1:2001", "x.y.z": "127.0.0.1:2002", "harness.metric.9999": "127.0.0.1:2002"} {
		r := &rec.RecBytes{Path: []byte(name)}
		if got := stickyEndpoint(cl.sticky.route(r)); got != want {
			t.Fatalf("placement vector %s: got %s want %s", name, got, want)
		}
	}
	// Verify ranking against sorting all endpoint scores, including two failures.
	for _, r := range testRecs(100) {
		hash := fnv.New64a()
		_, _ = hash.Write(r.Path)
		order := append([]*stickyHost(nil), cl.sticky.hosts...)
		sort.Slice(order, func(i, j int) bool {
			return stickyScore(hash.Sum64(), order[i].key) > stickyScore(hash.Sum64(), order[j].key)
		})
		for _, want := range order {
			if got := cl.sticky.route(r).host; got != want {
				t.Fatalf("ranking got %s want %s", got.target, want.target)
			}
			want.target.(*HostTCP).setAvailability(false)
		}
		for _, h := range order {
			h.target.(*HostTCP).setAvailability(true)
			h.blocked.Store(false)
		}
	}
}

func TestStickyStagedFailure(t *testing.T) {
	cl, ms := stickyTestCluster(t, []uint16{2001, 2002, 2003}, 1000)
	r := testRecs(1)[0]
	primary := cl.sticky.route(r).host
	batch := NewBatch()
	if err := cl.PushBytesBatch(r, ms, batch); err != nil {
		t.Fatal(err)
	}
	primary.target.(*HostTCP).setAvailability(false)
	batch.Flush()
	if queueLen(primary.q) != 0 {
		t.Fatal("batch admitted to a relay that failed before flush")
	}
	admitted := 0
	for _, h := range cl.sticky.hosts {
		admitted += queueLen(h.q)
	}
	if admitted != 1 {
		t.Fatalf("admitted %d records, want 1", admitted)
	}
}

func TestStickyInitialConnectionCheck(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		c, e := ln.Accept()
		if e == nil {
			accepted <- c
		}
	}()
	cfg := conf.MakeDefault()
	cfg.TCPInitialConnCheck = true
	port := uint16(ln.Addr().(*net.TCPAddr).Port)
	h := NewHostTCP("initial", cfg, conf.Host{Name: "127.0.0.1", Port: port}, zap.NewNop(), metrics.New())
	defer func() { h.Conn.Lock(); defer h.Conn.Unlock(); _ = h.Conn.Close() }()
	select {
	case c := <-accepted:
		defer c.Close()
	case <-time.After(3 * time.Second):
		t.Fatal("initial connection did not complete")
	}
	deadline := time.Now().Add(3 * time.Second)
	for !h.IsAvailable() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !h.IsAvailable() {
		t.Fatal("initial check updated a copied Host instead of the target")
	}
	h.Conn.Lock()
	connected := h.Conn.Conn != nil
	h.Conn.Unlock()
	if !connected {
		t.Fatal("target lost the initial connection")
	}
}

func TestStickyConcurrentAdmission(t *testing.T) {
	const producers, perProducer = 4, 2000
	cl, ms := stickyTestCluster(t, []uint16{2001, 2002, 2003}, 64)
	finish, monitored := make(chan struct{}), make(chan struct{})
	go func() { defer close(monitored); cl.sticky.monitor(finish) }()
	var readers, writers sync.WaitGroup
	var seenMu sync.Mutex
	seen := make(map[*rec.RecBytes]bool)
	duplicate := false
	for _, h := range cl.sticky.hosts {
		readers.Add(1)
		go func(h *stickyHost) {
			defer readers.Done()
			for c := h.q.pop(); c != nil; c = h.q.pop() {
				seenMu.Lock()
				for _, r := range c.recs[:c.n] {
					if seen[r] {
						duplicate = true
					}
					seen[r] = true
				}
				seenMu.Unlock()
				h.q.recycle(c)
			}
		}(h)
	}
	for range producers {
		writers.Add(1)
		go func() {
			defer writers.Done()
			b := NewBatch()
			for i, r := range testRecs(perProducer) {
				if err := cl.PushBytesBatch(r, ms, b); err != nil {
					panic(err)
				}
				if i%37 == 0 {
					b.Flush()
				}
			}
			b.Flush()
		}()
	}
	writers.Wait()
	close(finish)
	<-monitored
	for _, h := range cl.sticky.hosts {
		h.q.close()
	}
	readers.Wait()
	admitted := 0.0
	for _, h := range cl.sticky.hosts {
		admitted += testutil.ToFloat64(h.records)
	}
	dropped := testutil.ToFloat64(ms.ThrottledHostsTotal)
	if duplicate || admitted != float64(len(seen)) || admitted+dropped != producers*perProducer {
		t.Fatalf("duplicate=%v seen=%d admitted=%v dropped=%v", duplicate, len(seen), admitted, dropped)
	}
}

func TestStickyPressureRecovery(t *testing.T) {
	cl, ms := stickyTestCluster(t, []uint16{2001, 2002}, 2)
	r := testRecs(1)[0]
	h := cl.sticky.route(r).host
	for range 3 {
		if err := cl.PushBytes(r, ms); err != nil {
			t.Fatal(err)
		}
	}
	if !h.blocked.Load() {
		t.Fatal("full queue did not open circuit")
	}
	now := time.Now()
	cl.sticky.updateHealth(now)
	c := h.q.pop()
	h.q.recycle(c)
	cl.sticky.updateHealth(now.Add(100 * time.Millisecond))
	cl.sticky.updateHealth(now.Add(2 * time.Second))
	if !h.blocked.Load() {
		t.Fatal("queue circuit closed before stable recovery")
	}
	cl.sticky.updateHealth(now.Add(2100 * time.Millisecond))
	if h.blocked.Load() || cl.sticky.route(r).host != h {
		t.Fatal("queue circuit did not return names to primary")
	}
}
