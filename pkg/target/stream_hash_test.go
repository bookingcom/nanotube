package target

import (
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/rec"
	"github.com/dgryski/go-jump"
)

func TestStreamHashPinningDistribution(t *testing.T) {
	const names, connections = 1_000_000, 10
	s := streamSender{connections: make([]streamConnection, connections)}
	other := streamSender{connections: make([]streamConnection, connections)}
	for _, hosts := range []int{1, 3, 10, 31, 127} {
		counts := make([]int, hosts*connections)
		for i := range names {
			r := &rec.RecBytes{Path: fmt.Appendf(nil, "example.application.%02d.shared.component.worker.%08d.requests", i%32, i)}
			hash := r.PathHash()
			index := s.connectionIndex(hash)
			if index != other.connectionIndex(hash) || index != s.connectionIndex(r.PathHash()) {
				t.Fatal("pinning depends on sender identity or changes on repeated lookup")
			}
			counts[int(jump.Hash(hash, hosts))*connections+index]++
		}
		want := float64(names) / float64(len(counts))
		chi, maxDeviation := 0.0, 0.0
		for _, count := range counts {
			delta := float64(count) - want
			chi += delta * delta / want
			maxDeviation = math.Max(maxDeviation, math.Abs(delta)/want)
		}
		// Check the joint host/connection distribution, not just independent
		// marginals: the latter can hide badly correlated partitions.
		if chi/float64(len(counts)-1) > 1.4 || maxDeviation > .25 {
			t.Fatalf("hosts=%d correlated or uneven: chi/dof=%.3f max deviation=%.1f%%", hosts, chi/float64(len(counts)-1), maxDeviation*100)
		}
		t.Logf("hosts=%d chi/dof=%.3f max deviation=%.1f%%", hosts, chi/float64(len(counts)-1), maxDeviation*100)
	}
}

func TestStreamFanoutInitializesHashBeforePublish(t *testing.T) {
	// To-all routing can publish an unhashed record to a stream before a later
	// jump cluster reads its hash. Populate it on the routing goroutine, even
	// when multiple stream outputs are already consuming their queues.
	type bulkTarget interface{ pushBulk([]*rec.RecBytes) }
	var targets []Target
	var done []chan struct{}
	var wires []*faultTCPConn
	for _, multi := range []bool{false, true} {
		wire := &faultTCPConn{prefix: 1 << 24}
		h, sender, _ := newStreamTestTarget(multi, 0, conf.StreamOptions{MaxRows: 64}, wire)
		targets = append(targets, h)
		wires = append(wires, wire)
		finished := make(chan struct{})
		done = append(done, finished)
		go func() { sender.run(); close(finished) }()
	}
	for i := range 1000 {
		r := &rec.RecBytes{Path: fmt.Appendf(nil, "example.fanout.%d", i), Val: []byte("1.25"), Time: []byte("1700000000")}
		for _, h := range targets {
			if i%2 == 0 {
				h.Push(r)
			} else {
				h.(bulkTarget).pushBulk([]*rec.RecBytes{r})
			}
		}
		r.PathHash()
	}
	for _, h := range targets {
		h.Stop()
	}
	for i, finished := range done {
		select {
		case <-finished:
		case <-time.After(10 * time.Second):
			t.Fatal("stream fanout did not drain")
		}
		points, _ := decodeStream(t, wires[i].wire.Bytes())
		if len(points) != 1000 {
			t.Fatalf("fanout delivered %d points", len(points))
		}
	}
}
