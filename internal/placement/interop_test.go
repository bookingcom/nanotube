package placement

import (
	"bytes"
	"context"
	"github.com/bookingcom/nanotube/internal/v2placement"
	"github.com/bookingcom/nanotube/pkg/rec"
	"math"
	"net"
	"os"
	"runtime"
	"sort"
	"strconv"
	"testing"
	"time"
)

// TestExternalStoreACK is an opt-in loopback interoperability fixture. Its map
// matches the store's TestRelayACKSurvivesStoreSIGKILL helper. Run it twice with
// both persistent roots retained and kill/restart the store between runs.
func TestExternalStoreACK(t *testing.T) {
	addr := os.Getenv("V2_INTEROP_ADDRESS")
	if addr == "" {
		t.Skip("requires loopback go-carbon store fixture")
	}
	host, _, e := net.SplitHostPort(addr)
	if e != nil || !net.ParseIP(host).IsLoopback() {
		t.Fatal("interop fixture requires loopback")
	}
	routing := v2placement.Routing{NameVersion: v2placement.NameVersion, JumpSizes: []uint32{1}, Span: 4096, Namespaces: []v2placement.Namespace{{ID: "metrics", Prefix: ""}}}
	bootstrap := []v2placement.BootstrapReplica{{ID: "site-a", JumpSize: 1, Slots: []string{"store-a/inc-1"}}}
	groups, e := v2placement.Bootstrap(routing, bootstrap)
	if e != nil {
		t.Fatal(e)
	}
	lane := v2placement.Lane{Producer: "relay-a/inc-1", Stream: "lane-0"}
	groups[0].Replicas[0].Producers = []v2placement.Lane{lane}
	doc := v2placement.Document{Schema: 1, Domain: "relay-crash-test", Revision: 1, Operation: "bootstrap", Routing: routing, Bootstrap: bootstrap, Stores: []v2placement.Store{{ID: "store-a/inc-1", Write: "a:2003", Read: "a:8080", Copy: "a:8081"}}, Groups: groups}
	r, d, e := OpenDurable(doc, "site-a", lane, 4, DurableConfig{Root: os.Getenv("V2_INTEROP_SPOOL"), MaxRecords: 512, MaxBytes: 4 << 20})
	if e != nil {
		t.Fatal(e)
	}
	defer d.Close()
	n := 1
	if value := os.Getenv("V2_INTEROP_BATCH"); value != "" {
		n, e = strconv.Atoi(value)
		if e != nil || n < 1 || n > 256 {
			t.Fatal("invalid interop batch")
		}
	}
	records := make([]*rec.RecBytes, n)
	for i := range records {
		records[i] = rec.NewTyped([]byte("example.metric"), math.Copysign(0, -1), 100)
	}
	rounds := 1
	if value := os.Getenv("V2_INTEROP_ROUNDS"); value != "" {
		rounds, e = strconv.Atoi(value)
		if e != nil || rounds < 1 || rounds > 1024 {
			t.Fatal("invalid rounds")
		}
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	started := time.Now()
	groupStart := started
	latency := make([]time.Duration, 0, rounds)
	syncBefore := d.syncs
	if e = r.PushBatch(records); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- d.Run(ctx, bytes.Repeat([]byte{0x4b}, 32), func(ctx context.Context, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", addr)
		})
	}()
	for round := 1; round <= rounds; round++ {
		target := uint64(n * round)
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			d.mu.Lock()
			done := r.Stats.Acknowledged.Load() == target && len(d.queue) == 0
			d.mu.Unlock()
			if done {
				break
			}
			time.Sleep(50 * time.Microsecond)
		}
		d.mu.Lock()
		done := r.Stats.Acknowledged.Load() == target && len(d.queue) == 0
		d.mu.Unlock()
		if !done {
			t.Fatal("real store ACK timeout")
		}
		latency = append(latency, time.Since(groupStart))
		if round < rounds {
			groupStart = time.Now()
			if e = r.PushBatch(records); e != nil {
				t.Fatal(e)
			}
		}
	}
	elapsed := time.Since(started)
	runtime.ReadMemStats(&after)
	cancel()
	if e = <-done; e != nil {
		t.Fatal(e)
	}
	if r.Stats.Acknowledged.Load() != uint64(n*rounds) || r.Stats.Queued.Load() != 0 {
		t.Fatal("real store did not durably acknowledge")
	}
	sort.Slice(latency, func(i, j int) bool { return latency[i] < latency[j] })
	t.Logf("PERF records=%d records/s=%.2f ns/record=%.2f relay-fsyncs/s=%.2f relay-allocs/record=%.2f p99-durable-group-us=%d", n*rounds, float64(n*rounds)/elapsed.Seconds(), float64(elapsed.Nanoseconds())/float64(n*rounds), float64(d.syncs-syncBefore)/elapsed.Seconds(), float64(after.Mallocs-before.Mallocs)/float64(n*rounds), latency[(len(latency)-1)*99/100].Microseconds())
	t.Logf("map=%s durable acknowledgements=%d", doc.Digest(), r.Stats.Acknowledged.Load())
}
