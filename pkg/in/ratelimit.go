package in

import (
	"github.com/bookingcom/nanotube/pkg/ratelimiter"
	"time"
)

func rateLimit(rateLimiters []ratelimiter.RateLimiter, n int, retryDuration time.Duration) {
	for _, rl := range rateLimiters {
		for !rl.Allow(int64(n)) {
			time.Sleep(retryDuration)
		}
	}
}

func newRateLimiterTicker(intervalDuration time.Duration) (c <-chan time.Time, stop func()) {
	if intervalDuration <= 0 {
		// This will cause ratelimiter to check on each record
		ch := make(chan time.Time)
		close(ch)
		c = ch
		stop = func() {}
	} else {
		rateLimiterUpdateTicker := time.NewTicker(intervalDuration)
		c = rateLimiterUpdateTicker.C
		stop = rateLimiterUpdateTicker.Stop
	}
	return
}
