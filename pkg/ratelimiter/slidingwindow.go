package ratelimiter

import (
	"github.com/RussellLuo/slidingwindow"
	"time"
)

type slidingWindow struct {
	*slidingwindow.Limiter
}

func (sw *slidingWindow) Allow(n int64) bool {
	return sw.Limiter.AllowN(time.Now(), n)
}

// NewSlidingWindowRateLimiter creates a RateLimiter which uses a sliding window.
func NewSlidingWindowRateLimiter(windowSize time.Duration, limit int64) RateLimiter {
	swLimiter, _ := slidingwindow.NewLimiter(windowSize, limit, func() (slidingwindow.Window, slidingwindow.StopFunc) {
		return slidingwindow.NewLocalWindow()
	})
	return &slidingWindow{
		Limiter: swLimiter,
	}
}
