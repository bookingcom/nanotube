package placement

import (
	"bytes"
	"context"
	"github.com/bookingcom/nanotube/internal/v2placement"
	"github.com/bookingcom/nanotube/pkg/rec"
	"math"
	"net"
	"os"
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
	r, d, e := OpenDurable(doc, "site-a", lane, 4, DurableConfig{Root: os.Getenv("V2_INTEROP_SPOOL"), MaxRecords: 8, MaxBytes: 4 << 20})
	if e != nil {
		t.Fatal(e)
	}
	defer d.Close()
	if e = r.Push(rec.NewTyped([]byte("example.metric"), math.Copysign(0, -1), 100)); e != nil {
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
	deadline := time.Now().Add(10 * time.Second)
	for r.Stats.Acknowledged.Load() != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	if e = <-done; e != nil {
		t.Fatal(e)
	}
	if r.Stats.Acknowledged.Load() != 1 || r.Stats.Queued.Load() != 0 {
		t.Fatal("real store did not durably acknowledge")
	}
	t.Logf("map=%s durable acknowledgements=%d", doc.Digest(), r.Stats.Acknowledged.Load())
}
