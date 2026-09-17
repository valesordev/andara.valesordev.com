// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package auth

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// RateLimit is `N/period`: N attempts per period, per key. Zero N disables
// limiting.
type RateLimit struct {
	N      int
	Period time.Duration
}

// ParseRateLimit reads `10/m`, `100/h`, `5/s`, or `30/5m`.
func ParseRateLimit(s string) (RateLimit, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == "0" || s == "off" {
		return RateLimit{}, nil
	}
	n, per, ok := strings.Cut(s, "/")
	if !ok {
		return RateLimit{}, fmt.Errorf("auth.rate_limit must be N/period such as 10/m, got %q", s)
	}
	count, err := strconv.Atoi(strings.TrimSpace(n))
	if err != nil || count < 0 {
		return RateLimit{}, fmt.Errorf("auth.rate_limit must be N/period such as 10/m, got %q", s)
	}
	per = strings.TrimSpace(per)
	var d time.Duration
	switch per {
	case "s":
		d = time.Second
	case "m":
		d = time.Minute
	case "h":
		d = time.Hour
	default:
		d, err = time.ParseDuration(per)
		if err != nil || d <= 0 {
			return RateLimit{}, fmt.Errorf("auth.rate_limit must be N/period such as 10/m, got %q", s)
		}
	}
	return RateLimit{N: count, Period: d}, nil
}

// String renders the limit in the form ParseRateLimit reads.
func (r RateLimit) String() string {
	if r.N == 0 {
		return "off"
	}
	return fmt.Sprintf("%d/%s", r.N, r.Period)
}

// limiter is a token bucket per key. There is no lockout and no penalty
// beyond the bucket: an attacker who can name a username must not be able
// to deny its owner the game (AW-SRV-008 configuration notes).
type limiter struct {
	limit RateLimit
	now   func() time.Time

	mu      sync.Mutex
	buckets map[string]*bucket
	lastGC  time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

func newLimiter(limit RateLimit, now func() time.Time) *limiter {
	return &limiter{limit: limit, now: now, buckets: map[string]*bucket{}, lastGC: now()}
}

// allow takes one token from key's bucket, reporting false when it is empty.
func (l *limiter) allow(key string) bool {
	if l.limit.N <= 0 {
		return true
	}
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Sub(l.lastGC) > l.limit.Period {
		l.gc(now)
	}
	b := l.buckets[key]
	if b == nil {
		b = &bucket{tokens: float64(l.limit.N), last: now}
		l.buckets[key] = b
	}
	refill := now.Sub(b.last).Seconds() / l.limit.Period.Seconds() * float64(l.limit.N)
	b.tokens = min(float64(l.limit.N), b.tokens+refill)
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// gc drops buckets that have refilled completely; they are indistinguishable
// from absent ones. Bounds the map to the keys seen in the last period.
func (l *limiter) gc(now time.Time) {
	for k, b := range l.buckets {
		if now.Sub(b.last) >= l.limit.Period {
			delete(l.buckets, k)
		}
	}
	l.lastGC = now
}
