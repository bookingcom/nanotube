package target

import (
	"fmt"
	"hash/fnv"
	"sync/atomic"
	"time"

	"github.com/bookingcom/nanotube/pkg/metrics"
	"github.com/bookingcom/nanotube/pkg/rec"
	"github.com/prometheus/client_golang/prometheus"
)

const stickyPollInterval = 100 * time.Millisecond
const stickyRecoveryInterval = 2 * time.Second

type stickyCluster struct {
	hosts     []*stickyHost
	unhealthy prometheus.Gauge
	available prometheus.Gauge
	throttled prometheus.Counter
}

type stickyHost struct {
	target   Target
	q        *recQueue
	key      uint64
	identity string
	index    int
	owner    *stickyCluster

	// Connection state is owned by the existing sender. Queue rejection also
	// opens this circuit; only the monitor closes it after stable recovery.
	blocked  atomic.Bool
	failures atomic.Uint64
	// The following fields belong exclusively to the monitor.
	healthySince time.Time
	lastFailure  uint64

	primary, fallback             *stickyQueue
	records, failovers, throttled prometheus.Counter
}

// Two staging targets per relay carry the failover bit through Batch.Flush,
// without hashing a second time or updating a counter for every staged record.
type stickyQueue struct {
	Target
	host     *stickyHost
	id       int
	failover bool
}

func newStickyCluster(cl *Cluster, ms *metrics.Prom) (*stickyCluster, error) {
	if len(cl.Hosts) == 0 {
		return nil, fmt.Errorf("sticky cluster %s has no hosts", cl.Name)
	}
	s := &stickyCluster{
		unhealthy: ms.StickyUnhealthy.WithLabelValues(cl.Name),
		available: ms.NumberOfAvailableTargets.WithLabelValues(cl.Name),
		throttled: ms.ThrottledHostsTotal,
	}
	seen := make(map[string]bool)
	for i, t := range cl.Hosts {
		q := targetQueue(t)
		endpoint, ok := t.(fmt.Stringer)
		if q == nil || !ok {
			return nil, fmt.Errorf("sticky cluster %s requires TCP queue targets", cl.Name)
		}
		addr := endpoint.String()
		if seen[addr] {
			return nil, fmt.Errorf("duplicate sticky endpoint %s in cluster %s", addr, cl.Name)
		}
		seen[addr] = true
		key := fnv.New64a()
		_, _ = key.Write([]byte(addr))
		labels := prometheus.Labels{"cluster": cl.Name, "upstream_host": addr}
		h := &stickyHost{target: t, q: q, key: key.Sum64(), identity: addr, index: i, owner: s,
			records: ms.StickyRecords.With(labels), failovers: ms.StickyFailovers.With(labels),
			throttled: ms.ThrottledHosts.With(labels)}
		h.primary = &stickyQueue{Target: t, host: h, id: nextQueueID()}
		h.fallback = &stickyQueue{Target: t, host: h, id: nextQueueID(), failover: true}
		s.hosts = append(s.hosts, h)
	}
	s.updateHealth(time.Now())
	return s, nil
}

// The FNV path/endpoint hashes and this SplitMix64 finalizer are part of the
// placement contract. Endpoint identity, not configuration order, breaks ties.
func stickyScore(pathHash, endpointHash uint64) uint64 {
	x := pathHash ^ endpointHash
	x = (x ^ (x >> 30)) * 0xbf58476d1ce4e5b9
	x = (x ^ (x >> 27)) * 0x94d049bb133111eb
	return x ^ (x >> 31)
}

func (h *stickyHost) ready() bool {
	if !h.target.IsAvailable() {
		h.blocked.Store(true)
		return false
	}
	return !h.blocked.Load()
}

// Rendezvous ranking makes every subset a consistent ring: removing an endpoint
// moves only its names, even if hosts are reordered or removed from the middle.
// The common path allocates nothing and takes no routing lock.
func (s *stickyCluster) route(r *rec.RecBytes) *stickyQueue {
	hash := r.PathHash()
	primary, selected := -1, -1
	var best, liveBest uint64
	for i, h := range s.hosts {
		score := stickyScore(hash, h.key)
		if primary < 0 || score > best || score == best && h.identity > s.hosts[primary].identity {
			primary, best = i, score
		}
		if h.ready() && (selected < 0 || score > liveBest || score == liveBest && h.identity > s.hosts[selected].identity) {
			selected, liveBest = i, score
		}
	}
	if selected < 0 || selected == primary {
		// As with lb, retain points in an unavailable host's queue when all
		// hosts are down. The normal sender keeps reconnecting and replaying.
		return s.hosts[primary].primary
	}
	return s.hosts[selected].fallback
}

func (q *stickyQueue) queueID() int { return q.id }

func (q *stickyQueue) Push(r *rec.RecBytes) {
	q.pushBulk([]*rec.RecBytes{r})
}

func (q *stickyQueue) pushBulk(rs []*rec.RecBytes) {
	h := q.host
	if !h.ready() {
		// Health can change between staging and Flush. No queue has been
		// attempted yet, so retain all candidates for the all-down case.
		for _, r := range rs {
			h.owner.retry(r, -1)
		}
		return
	}
	dropped := h.q.push(rs)
	accepted := len(rs) - dropped
	h.records.Add(float64(accepted))
	if q.failover {
		h.failovers.Add(float64(accepted))
	}
	if dropped == 0 {
		return
	}
	h.failures.Add(1)
	h.blocked.Store(true)
	for _, r := range rs[accepted:] {
		h.owner.retry(r, h.index)
	}
}

// Queue admission is authoritative, including after a batch was staged. Try
// every remaining healthy queue in rank order, then unavailable queues, before
// counting a drop. Already accepted/in-flight points stay with their sender;
// moving those would break its replay accounting and introduce duplicates.
func (s *stickyCluster) retry(r *rec.RecBytes, excluded int) {
	tried := make([]bool, len(s.hosts))
	attempts := len(s.hosts)
	if excluded >= 0 {
		tried[excluded] = true
		attempts--
	}
	hash := r.PathHash()
	primary := s.primary(hash)
	for range attempts {
		selected := -1
		var best uint64
		live := false
		for i, h := range s.hosts {
			if tried[i] {
				continue
			}
			ready := h.ready()
			score := stickyScore(hash, h.key)
			if selected < 0 || ready && !live || ready == live && (score > best || score == best && h.identity > s.hosts[selected].identity) {
				selected, best, live = i, score, ready
			}
		}
		h := s.hosts[selected]
		if h.q.pushOne(r) {
			h.records.Inc()
			if selected != primary {
				h.failovers.Inc()
			}
			return
		}
		h.failures.Add(1)
		h.blocked.Store(true)
		tried[selected] = true
	}
	s.hosts[primary].throttled.Inc()
	s.throttled.Inc()
}

func (s *stickyCluster) primary(hash uint64) int {
	selected := 0
	best := stickyScore(hash, s.hosts[0].key)
	for i, h := range s.hosts[1:] {
		score := stickyScore(hash, h.key)
		if score > best || score == best && h.identity > s.hosts[selected].identity {
			selected, best = i+1, score
		}
	}
	return selected
}

func (s *stickyCluster) monitor(finish <-chan struct{}) {
	ticker := time.NewTicker(stickyPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-finish:
			return
		case now := <-ticker.C:
			s.updateHealth(now)
		}
	}
}

func (s *stickyCluster) updateHealth(now time.Time) {
	unhealthy := 0
	for _, h := range s.hosts {
		h.q.mu.Lock()
		low := !h.q.closed && h.q.n <= h.q.limit/2
		h.q.mu.Unlock()
		failure := h.failures.Load()
		if !h.target.IsAvailable() || failure != h.lastFailure || !low {
			h.healthySince = time.Time{}
			if !h.target.IsAvailable() || failure != h.lastFailure {
				h.blocked.Store(true)
			}
		} else if h.blocked.Load() {
			if h.healthySince.IsZero() {
				h.healthySince = now
			} else if now.Sub(h.healthySince) >= stickyRecoveryInterval {
				h.blocked.Store(false)
				h.healthySince = time.Time{}
				if h.failures.Load() != failure {
					h.blocked.Store(true)
				}
			}
		}
		h.lastFailure = failure
		if h.blocked.Load() || !h.target.IsAvailable() {
			unhealthy++
		}
	}
	s.unhealthy.Set(float64(unhealthy))
	s.available.Set(float64(len(s.hosts) - unhealthy))
}
