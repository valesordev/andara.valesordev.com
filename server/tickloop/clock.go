// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package tickloop

import (
	"context"
	"sync"
	"time"
)

// Clock is the loop's notion of time. The core never sees it; the loop
// uses it for scheduling, lag, and duration and nothing else.
type Clock interface {
	Now() time.Time
	// Sleep blocks for d or until ctx is done, reporting which.
	Sleep(ctx context.Context, d time.Duration) bool
}

// RealClock is the wall clock.
type RealClock struct{}

// Now implements Clock.
func (RealClock) Now() time.Time { return time.Now() }

// Sleep implements Clock.
func (RealClock) Sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// SteppedClock is a test clock that advances only when told to, or when
// the loop sleeps. A test drives it to prove scheduling without waiting for
// wall time: a tick that "takes" 150 ms is a handler that calls Advance.
type SteppedClock struct {
	mu  sync.Mutex
	now time.Time
	// OnSleep, if set, is called with the time a Sleep would advance to,
	// before it does. Returning false makes Sleep report ctx done — how a
	// test says "stop after ten seconds of stepped time".
	OnSleep func(until time.Time) bool
}

// NewSteppedClock starts at t.
func NewSteppedClock(t time.Time) *SteppedClock { return &SteppedClock{now: t} }

// Now implements Clock.
func (c *SteppedClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Advance moves time forward: the cost of work done "now".
func (c *SteppedClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// Sleep implements Clock by advancing to the wake time.
func (c *SteppedClock) Sleep(ctx context.Context, d time.Duration) bool {
	if ctx.Err() != nil {
		return false
	}
	if d <= 0 {
		return true
	}
	until := c.Now().Add(d)
	if c.OnSleep != nil && !c.OnSleep(until) {
		return false
	}
	c.mu.Lock()
	c.now = until
	c.mu.Unlock()
	return true
}
