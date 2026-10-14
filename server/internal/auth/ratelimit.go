package auth

import (
	"sync"
	"time"
)

// FailLimiter allows `max` failed attempts per key within `window`.
type FailLimiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	fails  map[string][]time.Time
	now    func() time.Time
}

func NewFailLimiter(max int, window time.Duration) *FailLimiter {
	return &FailLimiter{max: max, window: window, fails: map[string][]time.Time{}, now: time.Now}
}

// Blocked reports whether key has used up its failed attempts.
func (l *FailLimiter) Blocked(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.prune(key)) >= l.max
}

func (l *FailLimiter) Fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.fails[key] = append(l.prune(key), l.now())
}

func (l *FailLimiter) prune(key string) []time.Time {
	cutoff := l.now().Add(-l.window)
	kept := l.fails[key][:0]
	for _, t := range l.fails[key] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) == 0 {
		delete(l.fails, key)
		return nil
	}
	l.fails[key] = kept
	return kept
}
