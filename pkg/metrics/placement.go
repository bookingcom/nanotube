package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"sync"
	"sync/atomic"
)

type placementState struct {
	rejections *atomic.Uint64
	rejected   atomic.Uint64
}
type PlacementMetrics struct {
	mu       sync.RWMutex
	clusters map[string]*placementState
	rejected *prometheus.Desc
}

func newPlacementMetrics() *PlacementMetrics {
	return &PlacementMetrics{clusters: map[string]*placementState{}, rejected: prometheus.NewDesc("nanotube_placement_reload_rejections_total", "Rejected placement documents.", []string{"cluster"}, nil)}
}
func (m *PlacementMetrics) state(name string) *placementState {
	s := m.clusters[name]
	if s == nil {
		s = &placementState{}
		m.clusters[name] = s
	}
	return s
}
func (m *PlacementMetrics) Attach(name string, rejections *atomic.Uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.state(name).rejections = rejections
}
func (m *PlacementMetrics) Reject(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.state(name).rejected.Add(1)
}
func (m *PlacementMetrics) Describe(ch chan<- *prometheus.Desc) { ch <- m.rejected }
func (m *PlacementMetrics) Collect(ch chan<- prometheus.Metric) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for name, s := range m.clusters {
		n := s.rejected.Load()
		if s.rejections != nil {
			n += s.rejections.Load()
		}
		ch <- prometheus.MustNewConstMetric(m.rejected, prometheus.CounterValue, float64(n), name)
	}
}
