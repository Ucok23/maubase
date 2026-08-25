// Package ratelimit is a minimal in-memory fixed-window rate limiter, used
// to throttle login attempts (see internal/server's use on
// /api/auth/login and /admin/auth/login). It's process-local, in-memory
// state — fine at the single-VPS scale this project targets; a
// multi-instance deployment would need a shared store (Redis, etc.)
// instead, which is out of scope for v1.
package ratelimit

import (
	"sync"
	"time"
)

// Limiter enforces "at most Limit attempts per Window" per key (typically
// a client IP). It never blocks: Allow returns immediately with whether
// the attempt is permitted and, if not, how long until the window resets.
type Limiter struct {
	Limit  int
	Window time.Duration

	mu      sync.Mutex
	buckets map[string]*bucket
}

type bucket struct {
	count       int
	windowStart time.Time
}

func New(limit int, window time.Duration) *Limiter {
	return &Limiter{Limit: limit, Window: window, buckets: make(map[string]*bucket)}
}

// Allow records one attempt for key and reports whether it's within the
// limit. When it isn't, retryAfter is how long the caller should wait
// before the window resets.
func (l *Limiter) Allow(key string) (ok bool, retryAfter time.Duration) {
	now := time.Now()

	l.mu.Lock()
	defer l.mu.Unlock()

	b, found := l.buckets[key]
	if !found || now.Sub(b.windowStart) >= l.Window {
		// A fresh window: either we've never seen this key, or its last
		// window has fully elapsed. Opportunistically sweep other
		// expired buckets here too, so the map doesn't grow unbounded
		// under many distinct IPs without a dedicated cleanup goroutine.
		l.sweepLocked(now)
		l.buckets[key] = &bucket{count: 1, windowStart: now}
		return true, 0
	}

	b.count++
	if b.count > l.Limit {
		return false, l.Window - now.Sub(b.windowStart)
	}
	return true, 0
}

// sweepLocked removes buckets whose window has already elapsed. Callers
// must hold l.mu.
func (l *Limiter) sweepLocked(now time.Time) {
	for k, b := range l.buckets {
		if now.Sub(b.windowStart) >= l.Window {
			delete(l.buckets, k)
		}
	}
}
