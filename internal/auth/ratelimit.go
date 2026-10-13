package auth

import (
	"sync"
	"time"
)

// Limit allows Max events per Window.
type Limit struct {
	Max    int
	Window time.Duration
}

// Limiter is an in-memory fixed-window rate limiter keyed by an arbitrary
// string (a hashed client IP). It is per-process: fine for the single
// `retail serve` instance this app runs as.
type Limiter struct {
	limits []Limit
	now    func() time.Time

	mu      sync.Mutex
	windows map[string][]window
	lastGC  time.Time
}

type window struct {
	start time.Time
	count int
}

func NewLimiter(limits ...Limit) *Limiter {
	return &Limiter{limits: limits, now: time.Now, windows: make(map[string][]window)}
}

// Allow records one event for key and reports whether it is within every
// limit. A refused event isn't counted, so a blocked client doesn't keep
// pushing its own reset further out.
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.gc(now)

	ws, ok := l.windows[key]
	if !ok {
		ws = make([]window, len(l.limits))
	}
	for i, lim := range l.limits {
		if now.Sub(ws[i].start) >= lim.Window {
			ws[i] = window{start: now}
		}
		if ws[i].count >= lim.Max {
			l.windows[key] = ws
			return false
		}
	}
	for i := range ws {
		ws[i].count++
	}
	l.windows[key] = ws
	return true
}

// gc drops keys whose every window has expired, at most once a minute.
func (l *Limiter) gc(now time.Time) {
	if now.Sub(l.lastGC) < time.Minute {
		return
	}
	l.lastGC = now
	for k, ws := range l.windows {
		live := false
		for i, lim := range l.limits {
			if now.Sub(ws[i].start) < lim.Window {
				live = true
				break
			}
		}
		if !live {
			delete(l.windows, k)
		}
	}
}
