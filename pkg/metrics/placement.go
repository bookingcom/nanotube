package metrics

import (
	"sync"
	"sync/atomic"

	"github.com/bookingcom/nanotube/internal/placement"
	"github.com/prometheus/client_golang/prometheus"
)

type placementState struct {
	stats    *placement.Counters
	rejected atomic.Uint64
}

// PlacementMetrics reads atomics at scrape time; no Prometheus label lookup is
// added to the routing path. Rejection counters include malformed file reloads.
type PlacementMetrics struct {
	mu                                                    sync.RWMutex
	clusters                                              map[string]*placementState
	rejected, held, dropped, pending                      *prometheus.Desc
	queued, outputErrors, persistenceErrors, acknowledged *prometheus.Desc
}

func newPlacementMetrics() *PlacementMetrics {
	desc := func(name, help string) *prometheus.Desc {
		return prometheus.NewDesc("nanotube_placement_"+name, help, []string{"cluster"}, nil)
	}
	return &PlacementMetrics{queued: desc("unacknowledged_records", "Durable records awaiting store acknowledgement."), outputErrors: desc("output_errors_total", "Failed acknowledged output attempts."), persistenceErrors: desc("persistence_errors_total", "Terminal durable spool persistence failures."), acknowledged: desc("acknowledged_records_total", "Authenticated durable store acknowledgements persisted by relay."), clusters: map[string]*placementState{}, rejected: desc("reload_rejections_total", "Rejected placement documents and unsafe process reloads."), held: desc("held_records_total", "Records admitted to a moving group hold."), dropped: desc("dropped_records_total", "Records rejected by a bounded placement queue."), pending: desc("pending_records", "Records currently held for a moving group.")}
}
func (m *PlacementMetrics) Attach(name string, stats *placement.Counters) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.clusters[name]
	if s == nil {
		s = &placementState{}
		m.clusters[name] = s
	}
	s.stats = stats
}
func (m *PlacementMetrics) Reject(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.clusters[name]
	if s == nil {
		s = &placementState{}
		m.clusters[name] = s
	}
	s.rejected.Add(1)
}
func (m *PlacementMetrics) Describe(ch chan<- *prometheus.Desc) {
	ch <- m.queued
	ch <- m.outputErrors
	ch <- m.persistenceErrors
	ch <- m.acknowledged
	ch <- m.rejected
	ch <- m.held
	ch <- m.dropped
	ch <- m.pending
}
func (m *PlacementMetrics) Collect(ch chan<- prometheus.Metric) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for name, s := range m.clusters {
		rejected := s.rejected.Load()
		var held, dropped uint64
		var pending int64
		if s.stats != nil {
			rejected += s.stats.Rejected.Load()
			held = s.stats.Held.Load()
			dropped = s.stats.Dropped.Load()
			pending = s.stats.Pending.Load()
		}
		var queued int64
		var outputErrors, persistenceErrors, acknowledged uint64
		if s.stats != nil {
			queued = s.stats.Queued.Load()
			outputErrors = s.stats.OutputErrors.Load()
			persistenceErrors = s.stats.PersistenceErrors.Load()
			acknowledged = s.stats.Acknowledged.Load()
		}
		ch <- prometheus.MustNewConstMetric(m.queued, prometheus.GaugeValue, float64(queued), name)
		ch <- prometheus.MustNewConstMetric(m.outputErrors, prometheus.CounterValue, float64(outputErrors), name)
		ch <- prometheus.MustNewConstMetric(m.persistenceErrors, prometheus.CounterValue, float64(persistenceErrors), name)
		ch <- prometheus.MustNewConstMetric(m.acknowledged, prometheus.CounterValue, float64(acknowledged), name)
		ch <- prometheus.MustNewConstMetric(m.rejected, prometheus.CounterValue, float64(rejected), name)
		ch <- prometheus.MustNewConstMetric(m.held, prometheus.CounterValue, float64(held), name)
		ch <- prometheus.MustNewConstMetric(m.dropped, prometheus.CounterValue, float64(dropped), name)
		ch <- prometheus.MustNewConstMetric(m.pending, prometheus.GaugeValue, float64(pending), name)
	}
}
