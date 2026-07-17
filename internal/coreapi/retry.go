package coreapi

import (
	"context"
	"math/rand/v2"
	"time"
)

// retryPolicy is jittered exponential backoff for retryable calls. Adapted from
// the gateway's internal/retry, inlined here because the CLI needs nothing more.
type retryPolicy struct {
	attempts   int           // total attempts (>=1)
	backoffMin time.Duration // first backoff
	backoffMax time.Duration // cap
}

// do runs fn up to p.attempts times. fn returns (retryable, err): when retryable
// and attempts remain, do backs off (full jitter in [0, cur]) and retries.
func (p retryPolicy) do(ctx context.Context, fn func(attempt int) (retryable bool, err error)) error {
	if p.attempts < 1 {
		p.attempts = 1
	}
	cur := p.backoffMin
	if cur <= 0 {
		cur = 100 * time.Millisecond
	}
	var lastErr error
	for attempt := 1; attempt <= p.attempts; attempt++ {
		retryable, err := fn(attempt)
		if err == nil {
			return nil
		}
		lastErr = err
		if !retryable || attempt == p.attempts {
			return err
		}
		sleep := time.Duration(rand.Int64N(int64(cur) + 1))
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(sleep):
		}
		cur *= 2
		if p.backoffMax > 0 && cur > p.backoffMax {
			cur = p.backoffMax
		}
	}
	return lastErr
}
