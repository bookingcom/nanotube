package main

import (
	"fmt"
	"math"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/metrics"
	"github.com/bookingcom/nanotube/pkg/rec"
	"github.com/bookingcom/nanotube/pkg/rewrites"
	"github.com/bookingcom/nanotube/pkg/rules"
	codec "github.com/bookingcom/nanotube/pkg/streamcodec"
	"github.com/bookingcom/nanotube/pkg/target"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.uber.org/zap"
)

type routedPoint struct {
	destination, name string
	timestamp         int64
	bits              uint64
}
type captureTypedHost struct {
	destination string
	points      *[]routedPoint
}

func (h *captureTypedHost) Push(r *rec.RecBytes) {
	v, ts, ok := r.Values()
	if ok {
		*h.points = append(*h.points, routedPoint{h.destination, string(r.Path), ts, math.Float64bits(v)})
	}
}
func (*captureTypedHost) Stream(*sync.WaitGroup) {}
func (*captureTypedHost) Stop()                  {}
func (*captureTypedHost) IsAvailable() bool      { return true }
func differentialPipeline(t testing.TB) (rules.Rules, rewrites.Rewrites, *metrics.Prom, *[]routedPoint) {
	t.Helper()
	ms := metrics.New()
	points := &[]routedPoint{}
	clusters := target.Clusters{}
	for _, name := range []string{"primary", "special", "copy"} {
		clusters[name] = &target.Cluster{Name: name, Type: conf.ToallCluster, Hosts: []target.Target{&captureTypedHost{name, points}}}
	}
	clusters["blackhole"] = &target.Cluster{Name: "blackhole", Type: conf.BlackholeCluster}
	rs, err := rules.Build(&conf.Rules{Rule: []conf.Rule{
		{Regexs: []string{`^example\.blocked\.`}, Clusters: []string{"blackhole"}},
		{Prefixes: []string{"example.copy."}, Clusters: []string{"copy"}},
		{Regexs: []string{`^example\.special\..*`}, Clusters: []string{"special"}, Continue: true},
		{Prefixes: []string{"example."}, Clusters: []string{"primary"}},
	}}, clusters, false, ms)
	if err != nil {
		t.Fatal(err)
	}
	rw, err := rewrites.Build(&conf.Rewrites{Rewrite: []conf.Rewrite{
		{From: `^example\.rewrite\.(.*)$`, To: `example.special.${1}`},
		{From: `^example\.duplicate\.(.*)$`, To: `example.copy.${1}`, Copy: true},
	}}, false, ms)
	if err != nil {
		t.Fatal(err)
	}
	return rs, rw, ms, points
}
func TestStreamInputRoutingMatchesPlaintext(t *testing.T) {
	lines := []string{"example.good 1 100", ".example..good. -0 101", "example.special.value 1.25 102", "example.rewrite.value 2.5 103", "example.duplicate.value 3 104", "example.blocked.secret 4 105", "unrouted.value 5 106", "example.bad-name@ 6 107", "... 7 108", "example.nan NaN 109", "example.blocked.nan NaN 112", "example.duplicate.nan NaN 113", "example.inf +Inf 110", "example.duplicate.value -0 111", "example.good 99 100"}
	rs, rw, plainMS, want := differentialPipeline(t)
	for _, line := range append(lines, "example.invalid garbage 123", "missing-fields") {
		procBatch([]byte(line), rs, rw, true, false, time.Now, zap.NewNop(), plainMS, nil, nil)
	}
	sortPoints(*want)
	for _, flags := range []byte{0, codec.FlagZstd} {
		rs, rw, ms, got := differentialPipeline(t)
		sink := newStreamRouter(rs, rw, true, zap.NewNop(), ms)
		e, err := codec.NewEncoder(codec.Options{Flags: flags, MaxNames: 5})
		if err != nil {
			t.Fatal(err)
		}
		wire, _ := codec.AppendPreamble(nil, flags)
		for _, line := range lines {
			var name, value string
			var ts int64
			_, err := fmt.Sscan(line, &name, &value, &ts)
			if err != nil {
				t.Fatal(err)
			}
			v, err := func() (float64, error) { var v float64; _, err := fmt.Sscan(value, &v); return v, err }()
			if err != nil {
				t.Fatal(err)
			}
			if err = e.Add([]byte(name), v, ts); err != nil {
				t.Fatal(err)
			}
		}
		wire, err = e.Flush(wire)
		if err != nil {
			t.Fatal(err)
		}
		e.Close()
		d, err := codec.NewDecoder(codec.Options{Flags: flags})
		if err != nil {
			t.Fatal(err)
		}
		for rest := wire[codec.PreambleSize:]; len(rest) > 0; {
			n := codec.HeaderSize + int(uint32(rest[4])<<24|uint32(rest[5])<<16|uint32(rest[6])<<8|uint32(rest[7]))
			frame := rest[:n]
			if frame[0] == codec.ResetFrame {
				sink.Reset()
			}
			_, err = d.Decode(frame, func(id uint32, name []byte, v float64, ts int64, slot *any) { sink.Point(id, name, v, ts, slot) })
			if err != nil {
				t.Fatal(err)
			}
			sink.Flush()
			rest = rest[n:]
		}
		d.Close()
		sortPoints(*got)
		if !reflect.DeepEqual(*got, *want) {
			t.Fatalf("flags %d routed points differ\n got %#v\nwant %#v", flags, *got, *want)
		}
		if testutil.ToFloat64(ms.BlackholedRecs) != testutil.ToFloat64(plainMS.BlackholedRecs) {
			t.Fatal("blackhole count differs")
		}
		if testutil.ToFloat64(ms.RewrittenRecsTotal) != testutil.ToFloat64(plainMS.RewrittenRecsTotal) {
			t.Fatal("rewrite point count differs")
		}
	}
}
func sortPoints(p []routedPoint) {
	sort.Slice(p, func(i, j int) bool { return fmt.Sprint(p[i]) < fmt.Sprint(p[j]) })
}
func TestStreamInputKnownIDReusesRouteAndResetRebinds(t *testing.T) {
	rs, rw, ms, points := differentialPipeline(t)
	s := newStreamRouter(rs, rw, true, zap.NewNop(), ms)
	var slot any
	for i := range 100 {
		s.Point(0, []byte("example.good"), float64(i), int64(i), &slot)
	}
	s.Flush()
	if testutil.ToFloat64(ms.StreamInputRouteHits) != 99 || testutil.ToFloat64(ms.StreamInputRouteMisses) != 1 {
		t.Fatal("expected one normalization/rule resolution")
	}
	s.Reset()
	slot = nil
	s.Point(0, []byte("example.special.other"), 4, 101, &slot)
	s.Flush()
	if (*points)[100].name != "example.special.other" {
		t.Fatal("reset retained stale plan")
	}
}

func TestStreamInputRouteBudgetPreservesDelivery(t *testing.T) {
	rs, rw, ms, points := differentialPipeline(t)
	s := newStreamRouter(rs, rw, true, zap.NewNop(), ms)
	s.maxBytes = 4096
	for i := range 1000 {
		var slot any
		s.Point(uint32(i), []byte(fmt.Sprintf("example.good.%d", i)), float64(i), int64(i), &slot)
		if s.retainedBytes() > s.maxBytes {
			t.Fatalf("route cache retained %d > %d", s.retainedBytes(), s.maxBytes)
		}
	}
	s.Flush()
	if len(*points) != 1000 {
		t.Fatalf("budget changed delivery: %d", len(*points))
	}
	if testutil.ToFloat64(ms.StreamInputRouteBytes) != float64(s.retainedBytes()) {
		t.Fatal("route bytes gauge differs")
	}
	s.Reset()
	if s.retainedBytes() != 0 || testutil.ToFloat64(ms.StreamInputRouteBytes) != 0 {
		t.Fatal("reset retained old plans")
	}
}
