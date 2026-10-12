package target

import (
	"bufio"
	"fmt"
	"math/rand"
	"net"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/metrics"
	"github.com/bookingcom/nanotube/pkg/rec"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.uber.org/zap"
)

func testRecs(n int) []*rec.RecBytes {
	rs := make([]*rec.RecBytes, n)
	for i := range rs {
		r, err := rec.ParseRec([]byte(fmt.Sprintf("a.b.%d %d 1", i, i)), true, false, time.Now, zap.NewNop())
		if err != nil {
			panic(err)
		}
		rs[i] = r
	}
	return rs
}

func queueLen(q *recQueue) int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.n
}

// TestRecQueueMatchesChannel replays random push/pop sequences against a
// buffered channel with non-blocking sends, which is what the queue replaces.
func TestRecQueueMatchesChannel(t *testing.T) {
	rnd := rand.New(rand.NewSource(1))
	recs := testRecs(5000)

	for _, limit := range []int{0, 1, 2, 255, 256, 257, 1000} {
		q := newRecQueue(limit)
		ch := make(chan *rec.RecBytes, limit)
		next := 0
		for step := 0; step < 3000; step++ {
			if rnd.Intn(3) > 0 {
				n := rnd.Intn(600)
				batch := make([]*rec.RecBytes, 0, n)
				for i := 0; i < n; i++ {
					batch = append(batch, recs[next%len(recs)])
					next++
				}
				chDropped := 0
				for _, r := range batch {
					select {
					case ch <- r:
					default:
						chDropped++
					}
				}
				if dropped := q.push(batch); dropped != chDropped {
					t.Fatalf("limit %d step %d: dropped %d, channel dropped %d", limit, step, dropped, chDropped)
				}
			} else if len(ch) > 0 {
				c := q.pop()
				for _, r := range c.recs[:c.n] {
					if want := <-ch; r != want {
						t.Fatalf("limit %d step %d: popped %s, channel gave %s", limit, step, r.Path, want.Path)
					}
				}
				q.recycle(c)
			}
			if queueLen(q) != len(ch) {
				t.Fatalf("limit %d step %d: len %d, channel len %d", limit, step, queueLen(q), len(ch))
			}
		}
	}
}

func TestRecQueueConcurrent(t *testing.T) {
	const producers, perProducer, limit = 8, 20000, 5000
	q := newRecQueue(limit)
	recs := testRecs(producers * perProducer)

	var dropped sync.Map
	var wg sync.WaitGroup
	for p := 0; p < producers; p++ {
		wg.Add(1)
		go func(p int) {
			defer wg.Done()
			mine := recs[p*perProducer : (p+1)*perProducer]
			d := 0
			for len(mine) > 0 {
				n := min(len(mine), 1+rand.Intn(300))
				if n == 1 {
					if !q.pushOne(mine[0]) {
						d++
					}
				} else {
					d += q.push(mine[:n])
				}
				mine = mine[n:]
			}
			dropped.Store(p, d)
		}(p)
	}

	got := map[*rec.RecBytes]int{}
	lastSeen := map[int]int{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for c := q.pop(); c != nil; c = q.pop() {
			for _, r := range c.recs[:c.n] {
				got[r]++
				// records of one producer must arrive in push order
				i, _ := strconv.Atoi(string(r.Val))
				p := i / perProducer
				if prev, ok := lastSeen[p]; ok && i <= prev {
					t.Errorf("producer %d: record %d after %d", p, i, prev)
				}
				lastSeen[p] = i
			}
			q.recycle(c)
		}
	}()

	wg.Wait()
	q.close()
	<-done

	totalDropped := 0
	dropped.Range(func(_, v any) bool { totalDropped += v.(int); return true })
	for r, n := range got {
		if n != 1 {
			t.Fatalf("record %s delivered %d times", r.Path, n)
		}
	}
	if len(got)+totalDropped != len(recs) {
		t.Fatalf("delivered %d + dropped %d != pushed %d", len(got), totalDropped, len(recs))
	}
	if q.pushOne(recs[0]) || q.push(recs[:3]) != 3 {
		t.Fatal("push after close must drop")
	}
}

type fakeQueued struct {
	id  int
	got []*rec.RecBytes
}

func (f *fakeQueued) Stream(*sync.WaitGroup)      {}
func (f *fakeQueued) Push(r *rec.RecBytes)        { f.got = append(f.got, r) }
func (f *fakeQueued) IsAvailable() bool           { return true }
func (f *fakeQueued) Stop()                       {}
func (f *fakeQueued) queueID() int                { return f.id }
func (f *fakeQueued) pushBulk(rs []*rec.RecBytes) { f.got = append(f.got, rs...) }

func TestBatchKeepsOrderPerHost(t *testing.T) {
	a, b := &fakeQueued{id: nextQueueID()}, &fakeQueued{id: nextQueueID()}
	recs := testRecs(10)
	bt := NewBatch()
	var wantA, wantB []*rec.RecBytes
	for round := 0; round < 3; round++ {
		flushedA, flushedB := len(a.got), len(b.got)
		for i, r := range recs {
			if i%3 == 0 {
				bt.add(b, r)
				wantB = append(wantB, r)
			} else {
				bt.add(a, r)
				wantA = append(wantA, r)
			}
		}
		if len(a.got) != flushedA || len(b.got) != flushedB {
			t.Fatal("records reached the host before Flush")
		}
		bt.Flush()
	}
	if fmt.Sprint(a.got) != fmt.Sprint(wantA) || fmt.Sprint(b.got) != fmt.Sprint(wantB) {
		t.Fatal("batch changed per-host order")
	}
}

// TestHostMTCPDeliversAll streams records through a real HostMTCP to a local
// listener and checks every queued record arrives exactly once.
func TestHostMTCPDeliversAll(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	var mu sync.Mutex
	var lines []string
	var connWG sync.WaitGroup
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			connWG.Add(1)
			go func() {
				defer connWG.Done()
				sc := bufio.NewScanner(c)
				for sc.Scan() {
					mu.Lock()
					lines = append(lines, sc.Text())
					mu.Unlock()
				}
			}()
		}
	}()

	cfg := conf.MakeDefault()
	cfg.HostQueueSize = 100000
	cfg.TCPOutBufSize = 4096
	ms := metrics.New()
	h := ConstructHostMTCP("c", cfg, conf.Host{Name: "127.0.0.1", Port: uint16(l.Addr().(*net.TCPAddr).Port), MTCP: 4}, zap.NewNop(), ms)

	var wg sync.WaitGroup
	wg.Add(1)
	go h.Stream(&wg)

	recs := testRecs(50000)
	bt := NewBatch()
	for i, r := range recs {
		if i%1000 == 0 {
			h.Push(r)
			continue
		}
		bt.add(h, r)
		if i%777 == 0 {
			bt.Flush()
		}
	}
	bt.Flush()
	h.Stop()
	wg.Wait()
	for i := range h.MTCPs {
		if h.MTCPs[i].Conn != nil {
			h.MTCPs[i].Close()
		}
	}
	l.Close()
	connWG.Wait()

	want := make([]string, len(recs))
	for i, r := range recs {
		want[i] = string(r.Bytes()[:len(r.Bytes())-1])
	}
	sort.Strings(want)
	sort.Strings(lines)
	if len(lines) != len(want) {
		t.Fatalf("received %d lines, want %d", len(lines), len(want))
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Fatalf("line %d: %q, want %q", i, lines[i], want[i])
		}
	}
	if got := testutil.ToFloat64(h.outRecs); got != float64(len(recs)) {
		t.Fatalf("out records metric %v, want %d", got, len(recs))
	}
	if got := testutil.ToFloat64(h.throttled); got != 0 {
		t.Fatalf("throttled %v", got)
	}
}
