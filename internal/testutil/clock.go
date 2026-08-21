// Package testutil provides the shared deterministic test harness:
// fake clock, fake LLM/OIDC providers, and real-infrastructure helpers
// (Postgres, Redis, DynamoDB via LocalStack) for integration tests.
package testutil

import (
	"sync"
	"time"
)

// Clock abstracts time for determinism. Production code receives a RealClock;
// tests inject a FakeClock and advance it manually.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
	Since(t time.Time) time.Duration
}

// RealClock delegates to the time package.
type RealClock struct{}

// Now returns the current wall-clock time.
func (RealClock) Now() time.Time { return time.Now() }

// After delegates to time.After.
func (RealClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// Since delegates to time.Since.
func (RealClock) Since(t time.Time) time.Duration { return time.Since(t) }

// FakeClock is a manually advanced clock. Safe for concurrent use.
type FakeClock struct {
	mu   sync.Mutex
	now  time.Time
	timers []fakeTimer
}

type fakeTimer struct {
	at    time.Time
	ch    chan time.Time
	fired bool
}

// NewFakeClock starts at the given time.
func NewFakeClock(start time.Time) *FakeClock {
	return &FakeClock{now: start}
}

// Now returns the current fake time.
func (f *FakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

// After returns a channel that receives the fake time when it reaches now+d.
// If the deadline has already passed, the channel fires immediately.
func (f *FakeClock) After(d time.Duration) <-chan time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	ch := make(chan time.Time, 1)
	deadline := f.now.Add(d)
	if !deadline.After(f.now) {
		ch <- f.now
		return ch
	}
	f.timers = append(f.timers, fakeTimer{at: deadline, ch: ch})
	return ch
}

// Since reports time elapsed since t according to fake time.
func (f *FakeClock) Since(t time.Time) time.Duration {
	return f.Now().Sub(t)
}

// Advance moves the clock forward and fires any timers that have come due.
func (f *FakeClock) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
	for i := range f.timers {
		timer := &f.timers[i]
		if !timer.fired && !timer.at.After(f.now) {
			timer.fired = true
			timer.ch <- f.now
		}
	}
}
