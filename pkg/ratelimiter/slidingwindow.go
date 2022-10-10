package ratelimiter

import (
	"github.com/RussellLuo/slidingwindow"
	"github.com/prometheus/client_golang/prometheus"
	"time"
)

type slidingWindow struct {
	base
	*slidingwindow.Limiter
}

func (sw *slidingWindow) Allow(n int64) bool {
	if sw.Limiter.AllowN(time.Now(), n) {
		return true
	}
	if sw.blockingRecords != nil {
		sw.blockingRecords.Add(float64(n))
	}
	return false
}

// NewSlidingWindowRateLimiter creates a RateLimiter which uses a sliding window.
func NewSlidingWindowRateLimiter(windowSize time.Duration, limit int64, blockCounter prometheus.Counter) RateLimiter {
	swLimiter, _ := slidingwindow.NewLimiter(windowSize, limit, func() (slidingwindow.Window, slidingwindow.StopFunc) {
		return slidingwindow.NewLocalWindow()
	})
	return &slidingWindow{
		base: base{
			blockingRecords: blockCounter,
		},
		Limiter: swLimiter,
	}
}
