// Copyright (c) MathGaps
// SPDX-License-Identifier: MPL-2.0

package play

import (
	"context"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"time"
)

const (
	retryMaxAttempts = 5
	retryBaseDelay   = time.Second
	retryMaxDelay    = 30 * time.Second
)

// retryTransport retries a request the API answered with 429 or a 5xx status,
// backing off exponentially with jitter and honouring Retry-After.
type retryTransport struct {
	base        http.RoundTripper
	maxAttempts int
	baseDelay   time.Duration
	maxDelay    time.Duration
	// sleep waits for d or until ctx is done. Replaced in tests.
	sleep func(ctx context.Context, d time.Duration) error
}

func newRetryTransport(base http.RoundTripper) *retryTransport {
	if base == nil {
		base = http.DefaultTransport
	}

	return &retryTransport{
		base:        base,
		maxAttempts: retryMaxAttempts,
		baseDelay:   retryBaseDelay,
		maxDelay:    retryMaxDelay,
		sleep:       sleepContext,
	}
}

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func retryableStatus(code int) bool {
	switch code {
	case http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

// RoundTrip implements http.RoundTripper.
func (t *retryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	for attempt := 1; ; attempt++ {
		attemptReq := req
		if attempt > 1 && req.GetBody != nil {
			body, err := req.GetBody()
			if err != nil {
				return nil, err
			}
			attemptReq = req.Clone(req.Context())
			attemptReq.Body = body
		}

		resp, err := t.base.RoundTrip(attemptReq)
		if err != nil {
			return nil, err
		}

		// A body that cannot be replayed rules out a second attempt.
		replayable := req.Body == nil || req.Body == http.NoBody || req.GetBody != nil
		if !retryableStatus(resp.StatusCode) || attempt >= t.maxAttempts || !replayable {
			return resp, nil
		}

		delay := t.delay(attempt, resp.Header.Get("Retry-After"))

		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()

		if err := t.sleep(req.Context(), delay); err != nil {
			return nil, err
		}
	}
}

// delay is the wait before the attempt after the given one: the server's
// Retry-After when it sent a number of seconds, otherwise the base delay
// doubled per attempt, capped, with up to half of it replaced by jitter.
func (t *retryTransport) delay(attempt int, retryAfter string) time.Duration {
	if seconds, err := strconv.Atoi(retryAfter); err == nil && seconds >= 0 {
		return min(time.Duration(seconds)*time.Second, t.maxDelay)
	}

	backoff := min(t.baseDelay<<(attempt-1), t.maxDelay)
	if backoff <= 0 {
		return 0
	}

	half := backoff / 2

	return half + rand.N(half+1)
}
