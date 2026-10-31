package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"regexp"
	"runtime/pprof"
	"sort"
	"strconv"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"sync"
	"testing"
	"time"

	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/metrics"
	"github.com/bookingcom/nanotube/pkg/rec"
	"github.com/bookingcom/nanotube/pkg/rewrites"
	"github.com/bookingcom/nanotube/pkg/rules"
	"github.com/bookingcom/nanotube/pkg/target"
	"go.uber.org/zap"
)

// No captured names are stored in the repository or printed by this benchmark.
// Run with NANOTUBE_ROUTE_CAPTURE pointing to a gzip plaintext capture.
func routeCapture(b *testing.B) [][]byte {
	b.Helper()
	path := os.Getenv("NANOTUBE_ROUTE_CAPTURE")
	if path == "" {
		b.Skip("set NANOTUBE_ROUTE_CAPTURE to a gzip line-protocol capture")
	}
	f, err := os.Open(path)
	if err != nil {
		b.Fatal(err)
	}
	defer f.Close()
	z, err := gzip.NewReader(f)
	if err != nil {
		b.Fatal(err)
	}
	defer z.Close()
	data, err := io.ReadAll(z)
	if err != nil {
		b.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSuffix(data, []byte("\n")), []byte("\n"))
	b.Logf("capture records: %d", len(lines))
	return lines
}

// Discard after real jump selection; output queues and syscalls are deliberately
// outside this CPU benchmark. Each logical destination has three replica rings.
type routeBenchHost struct{ records uint64 }

func (h *routeBenchHost) Push(*rec.RecBytes)   { h.records++ }
func (*routeBenchHost) Stream(*sync.WaitGroup) {}
func (*routeBenchHost) IsAvailable() bool      { return true }
func (*routeBenchHost) Stop()                  {}

func captureRules(b *testing.B, lines [][]byte, ms *metrics.Prom) rules.Rules {
	b.Helper()
	counts := make(map[string]int)
	for _, line := range lines {
		end := bytes.IndexByte(line, ' ')
		if end < 0 {
			continue
		}
		path := line[:end]
		// Three components give a useful mix even for captures with one root.
		n, depth := 0, 0
		for ; n < len(path); n++ {
			if path[n] == '.' {
				depth++
				if depth == 3 {
					n++
					break
				}
			}
		}
		counts[string(path[:n])]++
	}
	prefixes := make([]string, 0, len(counts))
	for p := range counts {
		prefixes = append(prefixes, p)
	}
	sort.Slice(prefixes, func(i, j int) bool {
		if counts[prefixes[i]] != counts[prefixes[j]] {
			return counts[prefixes[i]] > counts[prefixes[j]]
		}
		return prefixes[i] < prefixes[j]
	})
	clusters := make(target.Clusters)
	for i := 0; i < 45*3; i++ {
		cl := &target.Cluster{Name: fmt.Sprintf("destination-%d", i), Type: conf.JumpCluster}
		for j := 0; j < 64; j++ {
			cl.Hosts = append(cl.Hosts, &routeBenchHost{})
		}
		clusters[cl.Name] = cl
	}
	var cfg conf.Rules
	for i := 0; i < 44; i++ {
		p := fmt.Sprintf("unused%d.", i)
		if i < len(prefixes) {
			p = prefixes[i]
		}
		r := conf.Rule{Regexs: []string{`^` + regexp.QuoteMeta(p) + `[^.]+\.(count|total)$`}}
		if i%3 != 0 {
			r.Prefixes = []string{p}
		}
		for j := 0; j < 3; j++ {
			r.Clusters = append(r.Clusters, fmt.Sprintf("destination-%d", 3*i+j))
		}
		cfg.Rule = append(cfg.Rule, r)
	}
	cfg.Rule = append(cfg.Rule, conf.Rule{Regexs: []string{`.*`}, Clusters: []string{"destination-132", "destination-133", "destination-134"}})
	rs, err := rules.Build(&cfg, clusters, false, ms)
	if err != nil {
		b.Fatal(err)
	}
	return rs
}

func BenchmarkRouteCapture(b *testing.B) {
	lines := routeCapture(b)
	ms, lg := metrics.New(), zap.NewNop()
	rs := captureRules(b, lines, ms)
	var rw rewrites.Rewrites
	now := time.Unix(1700000000, 0)
	nowF := func() time.Time { return now }
	var cache *routeCache
	if v := os.Getenv("NANOTUBE_ROUTE_CACHE_BYTES"); v != "" {
		limit, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			b.Fatal(err)
		}
		cache = newRouteCache(limit, ms)
	}
	if os.Getenv("NANOTUBE_ROUTE_STICKY") == "1" {
		seen := make(map[string]bool)
		var names [][]byte
		for _, line := range lines {
			end := bytes.IndexByte(line, ' ')
			if end < 0 {
				continue
			}
			key := string(line[:end])
			if !seen[key] {
				seen[key] = true
				names = append(names, line)
			}
		}
		b.Logf("sticky working set: %d names; periods 10..60 simulated seconds; 120-second measurement after initial fill", len(names))
		for _, line := range names {
			procBatch(line, rs, rw, true, false, nowF, lg, ms, nil, cache)
		}
		bins := make([][][]byte, 121)
		for i, line := range names {
			period := 10 + i%51
			for at := period; at <= 120; at += period {
				bins[at] = append(bins[at], line)
			}
		}
		lines = nil
		for _, bin := range bins {
			lines = append(lines, bin...)
		}
	}
	hits, misses := testutil.ToFloat64(ms.RouteCacheHits), testutil.ToFloat64(ms.RouteCacheMisses)
	b.ReportAllocs()
	b.ResetTimer()
	pprof.Do(context.Background(), pprof.Labels("phase", "parse-route"), func(context.Context) {
		for i := 0; i < b.N; i++ {
			for _, line := range lines {
				procBatch(line, rs, rw, true, false, nowF, lg, ms, nil, cache)
			}
		}
	})
	b.StopTimer()
	n := float64(b.N) * float64(len(lines))
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/n, "ns/record")
	b.ReportMetric(float64(len(lines)), "records/pass")
	h, m := testutil.ToFloat64(ms.RouteCacheHits)-hits, testutil.ToFloat64(ms.RouteCacheMisses)-misses
	if h+m > 0 {
		b.ReportMetric(100*h/(h+m), "hit-percent")
	}
	b.ReportMetric(testutil.ToFloat64(ms.RouteCacheBytes), "cache-bytes")
	b.ReportMetric(testutil.ToFloat64(ms.RouteCacheEvictions), "evictions")
}
