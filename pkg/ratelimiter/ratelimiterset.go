package ratelimiter

import (
	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/metrics"
	"sync"
	"time"
)

// Set consists of all the RateLimiters in use.
type Set struct {
	containerLock         sync.RWMutex
	containerRateLimiters map[string]RateLimiter
	globalRateLimiter     RateLimiter
}

// NewRateLimiterSet creates a new ratelimiter Set using main configuration.
func NewRateLimiterSet(cfg *conf.Main, prom *metrics.Prom) *Set {
	rls := &Set{
		containerRateLimiters: make(map[string]RateLimiter),
	}
	if cfg.RateLimiterGlobalRecordLimit > 0 {
		windowSize := time.Duration(cfg.RateLimiterWindowSizeSec) * time.Second
		rls.globalRateLimiter = NewSlidingWindowRateLimiter(windowSize, int64(cfg.RateLimiterGlobalRecordLimit), prom.GlobalRateLimiterBlockedRecords)
	}
	return rls
}

// GetOrCreateContainerRateLimiterWithID creates a container RateLimiter by id and main configuration
// if it doesn't exist. It'll return the RateLimiter for the corresponding container.
func (s *Set) GetOrCreateContainerRateLimiterWithID(id string, cfg *conf.Main, prom *metrics.Prom) RateLimiter {
	s.containerLock.Lock()
	defer s.containerLock.Unlock()
	if ipRateLimiter, exists := s.containerRateLimiters[id]; exists {
		return ipRateLimiter
	}
	windowSize := time.Duration(cfg.RateLimiterWindowSizeSec) * time.Second
	s.containerRateLimiters[id] = NewSlidingWindowRateLimiter(windowSize, int64(cfg.RateLimiterContainerRecordLimit),
		prom.ContainerRateLimiterBlockedRecords.WithLabelValues(id))
	return s.containerRateLimiters[id]
}

// GlobalRateLimiter returns global RateLimiter.
func (s *Set) GlobalRateLimiter() RateLimiter {
	return s.globalRateLimiter
}
