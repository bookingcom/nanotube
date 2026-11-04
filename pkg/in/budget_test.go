package in

import (
	"bytes"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/bookingcom/nanotube/pkg/metrics"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestBudgetBackpressureAndDrain(t *testing.T) {
	first := [][]byte{[]byte("first 1 2")}
	second := [][]byte{[]byte("second 3 4")}
	budget := NewBatchBudget(max(batchCharge(first), batchCharge(second)))
	q := make(chan [][]byte, 1)
	ms := metrics.New()
	a, b := NewBatchChan(q, 0, 0, ms, budget), NewBatchChan(q, 0, 0, ms, budget)
	a.Push(first[0])
	done := make(chan struct{})
	go func() { b.Push(second[0]); b.Close(); close(done) }()
	select {
	case <-done:
		t.Fatal("full budget did not backpressure the reader")
	case <-time.After(20 * time.Millisecond):
	}
	got := <-q
	// Dequeue alone must not release memory while routing holds the raw batch.
	if budget.Bytes() == 0 {
		t.Fatal("dequeue released the active worker's batch")
	}
	if !bytes.Equal(got[0], first[0]) {
		t.Fatal("changed first record")
	}
	budget.Release(got)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("processing did not unblock admission")
	}
	got = <-q
	if !bytes.Equal(got[0], second[0]) {
		t.Fatal("changed second record")
	}
	budget.Release(got)
	a.Close()
	if budget.Bytes() != 0 || testutil.ToFloat64(ms.InRecs) != 2 || testutil.ToFloat64(ms.ThrottledRecs) != 0 {
		t.Fatal("backpressure changed accounting")
	}
}

func TestBudgetOversizedAndMixedInput(t *testing.T) {
	budget := NewBatchBudget(1)
	batch := [][]byte{bytes.Repeat([]byte{'x'}, 64<<10)}
	budget.acquire(batch)
	if budget.Bytes() != float64(batchCharge(batch)) {
		t.Fatal("oversized batch must run alone")
	}
	budget.Release([][]byte{[]byte("uncharged datagram")})
	if budget.Bytes() == 0 {
		t.Fatal("uncharged input released a reservation")
	}
	budget.Release(batch)
	budget.Release(batch)
	if budget.Bytes() != 0 {
		t.Fatal("release must be idempotent")
	}
	if NewBatchBudget(0) != nil {
		t.Fatal("zero must preserve legacy behavior")
	}
}

func TestBudgetConcurrentReadersExactAccounting(t *testing.T) {
	const readers, records = 8, 50
	budget := NewBatchBudget(3 * batchCharge(make([][]byte, 1)))
	q := make(chan [][]byte, 2)
	ms := metrics.New()
	var wg sync.WaitGroup
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			b := NewBatchChan(q, 3, 0, ms, budget)
			defer b.Close()
			for j := 0; j < records; j++ {
				b.Push([]byte(fmt.Sprintf("%d.%d 1 2", i, j)))
			}
		}(i)
	}
	go func() { wg.Wait(); close(q) }()
	seen := make(map[string]bool)
	for batch := range q {
		if got := budget.Bytes(); got > float64(budget.limit) {
			t.Fatalf("budget exceeded: %v", got)
		}
		for _, r := range batch {
			if seen[string(r)] {
				t.Fatal("duplicate record")
			}
			seen[string(r)] = true
		}
		budget.Release(batch)
	}
	if len(seen) != readers*records || budget.Bytes() != 0 || testutil.ToFloat64(ms.ThrottledRecs) != 0 {
		t.Fatal("concurrent drain lost records or leaked reservations")
	}
}
