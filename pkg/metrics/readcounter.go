package metrics

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

// readCounterVec collects counters already maintained under a target's queue
// lock. Scraping reads those values instead of adding another atomic update to
// every plaintext flush. Binding occurs once, before the target starts.
type readCounterVec struct {
	mu       sync.Mutex
	counters map[[2]string]*readCounter
}

type readCounter struct {
	labels [2]string
	desc   *prometheus.Desc
	mu     sync.RWMutex
	read   func() float64
}

func (v *readCounterVec) With(labels prometheus.Labels) *readCounter {
	v.mu.Lock()
	defer v.mu.Unlock()
	key := [2]string{labels["cluster"], labels["upstream_host"]}
	if c := v.counters[key]; c != nil {
		return c
	}
	c := &readCounter{desc: readCounterDesc(), labels: key}
	if v.counters == nil {
		v.counters = make(map[[2]string]*readCounter)
	}
	v.counters[key] = c
	return c
}

// Bind adds one connection owner's counter to this host/cluster series.
func (c *readCounter) Bind(read func() float64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	old := c.read
	c.read = func() float64 {
		n := read()
		if old != nil {
			n += old()
		}
		return n
	}
}

func (c *readCounter) Describe(ch chan<- *prometheus.Desc) { ch <- c.desc }
func (c *readCounter) Collect(ch chan<- prometheus.Metric) {
	c.mu.RLock()
	read := c.read
	c.mu.RUnlock()
	var value float64
	if read != nil {
		value = read()
	}
	ch <- prometheus.MustNewConstMetric(c.desc, prometheus.CounterValue, value, c.labels[0], c.labels[1])
}
func readCounterDesc() *prometheus.Desc {
	return prometheus.NewDesc("nanotube_tcp_plaintext_bytes_total", "Plaintext bytes completed by successful TCP buffer writes, before compression.", []string{"cluster", "upstream_host"}, nil)
}
func (v *readCounterVec) Describe(ch chan<- *prometheus.Desc) { ch <- readCounterDesc() }

func (v *readCounterVec) Collect(ch chan<- prometheus.Metric) {
	v.mu.Lock()
	counters := make([]*readCounter, 0, len(v.counters))
	for _, c := range v.counters {
		counters = append(counters, c)
	}
	v.mu.Unlock()
	for _, c := range counters {
		c.Collect(ch)
	}
}
