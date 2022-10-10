package ratelimiter

import "github.com/prometheus/client_golang/prometheus"

// RateLimiter is responsible for allowing records and in and keep track of the
// records already allowed. It denies records when the limit is reached.
type RateLimiter interface {
	Allow(count int64) bool
}

type base struct {
	blockingRecords prometheus.Counter
}
