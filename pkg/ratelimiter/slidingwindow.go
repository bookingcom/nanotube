package ratelimiter

import (
	"time"

	"github.com/RussellLuo/slidingwindow"
	"github.com/prometheus/client_golang/prometheus"
)

// SlidingWindow is a rate limiter using a sliding window for limiting the records.
type SlidingWindow struct {
	blockingReaders prometheus.Counter
	*slidingwindow.Limiter
}

// Allow checks if n records can be allowed by the sliding window rate limiter.
// It increases the number of blocking readers when denies the records.
func (sw *SlidingWindow) Allow(n int64) bool {
	if sw.Limiter.AllowN(time.Now(), n) {
		return true
	}
	if sw.blockingReaders != nil {
		sw.blockingReaders.Inc()
	}
	return false
}

// NewSlidingWindowRateLimiter creates a rate limiter which uses a sliding window.
func NewSlidingWindowRateLimiter(windowSize time.Duration, limit int64, blockCounter prometheus.Counter) *SlidingWindow {
	swLimiter, _ := slidingwindow.NewLimiter(windowSize, limit, func() (slidingwindow.Window, slidingwindow.StopFunc) {
		return slidingwindow.NewLocalWindow()
	})
	return &SlidingWindow{
		blockingReaders: blockCounter,
		Limiter:         swLimiter,
	}
}

// RateLimit checks the rate limiters given and blocks/retries until all rate limiters allow n records.
func RateLimit(rateLimiters []*SlidingWindow, n int, retryDuration time.Duration) {
	for _, rl := range rateLimiters {
		for !rl.Allow(int64(n)) {
			time.Sleep(retryDuration)
		}
	}
}
