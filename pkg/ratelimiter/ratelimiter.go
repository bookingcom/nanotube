package ratelimiter

// RateLimiter is responsible for allowing records and in and keep track of the
// records already allowed. It denies records when the limit is reached.
type RateLimiter interface {
	Allow(count int64) bool
}
