package main

import (
	"hash/fnv"
	"sync"
	"testing"

	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/metrics"
	"github.com/bookingcom/nanotube/pkg/rec"
	"github.com/bookingcom/nanotube/pkg/rewrites"
	"github.com/bookingcom/nanotube/pkg/rules"
	"github.com/bookingcom/nanotube/pkg/target"
	"go.uber.org/zap"
)

type tokenCaptureHost struct {
	records     []*rec.RecBytes
	resolutions int
	invalidHash bool
}

func (h *tokenCaptureHost) Push(r *rec.RecBytes) {
	hash := fnv.New64a()
	_, _ = hash.Write(r.Path)
	if r.PathHash() != hash.Sum64() {
		h.invalidHash = true
	}
	if r.StreamToken() == 0 {
		h.resolutions++
		r.SetStreamToken(123)
	}
	h.records = append(h.records, r)
}
func (*tokenCaptureHost) Stream(*sync.WaitGroup) {}
func (*tokenCaptureHost) Stop()                  {}
func (*tokenCaptureHost) IsAvailable() bool      { return true }

func TestStreamInputTokenSurvivesUncachedPlansAndRelease(t *testing.T) {
	for _, raw := range []string{"example.shared", ".example..shared."} {
		for _, compact := range []bool{true, false} {
			for _, cached := range []bool{true, false} {
				ms := metrics.New()
				host := &tokenCaptureHost{}
				rs, err := rules.Build(&conf.Rules{Rule: []conf.Rule{{Prefixes: []string{"example."}, Clusters: []string{"out"}}}}, target.Clusters{"out": {Name: "out", Type: conf.ToallCluster, Hosts: []target.Target{host}}}, false, ms)
				if err != nil {
					t.Fatal(err)
				}
				s := newStreamRouter(rs, rewrites.Rewrites{}, true, zap.NewNop(), ms)
				if !cached {
					s.maxBytes = 1
				}
				if compact {
					ref, hit, err := s.BindStreamName([]byte(raw))
					if err != nil {
						t.Fatal(err)
					}
					for range 20 {
						s.BoundPoint(ref, hit, 1, 2)
					}
					s.Flush()
					s.ReleaseStreamName(ref, hit)
				} else {
					ref := s.BindName([]byte(raw))
					for range 20 {
						s.Point(0, []byte(raw), 1, 2, &ref)
					}
					s.Flush()
					s.ReleaseName(ref)
				}
				s.Close()
				if host.resolutions != 1 {
					t.Fatalf("cached=%v resolved %d times", cached, host.resolutions)
				}
				for _, r := range host.records {
					if r.StreamToken() != 123 || string(r.Path) != "example.shared" {
						t.Fatal("queued identity or path changed after release")
					}
				}
				if host.invalidHash {
					t.Fatal("path hash differs from plaintext")
				}
				assertEmptyStreamNames(t, s)
			}
		}
	}
}
