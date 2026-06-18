package handlers

import (
	"sync"
	"time"
)

type loginRateLimiter struct {
	mu       sync.Mutex
	limit    int
	window   time.Duration
	attempts map[string]loginAttempt
}

type loginAttempt struct {
	count      int
	firstSeen  time.Time
	blockedTil time.Time
}

func newLoginRateLimiter(limit int, window time.Duration) *loginRateLimiter {
	if limit < 1 {
		limit = 10
	}
	if window <= 0 {
		window = 5 * time.Minute
	}
	return &loginRateLimiter{limit: limit, window: window, attempts: map[string]loginAttempt{}}
}

func (l *loginRateLimiter) allow(key string, now time.Time) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cleanup(now)
	attempt, ok := l.attempts[key]
	if !ok || attempt.blockedTil.IsZero() || !now.Before(attempt.blockedTil) {
		return true, 0
	}
	return false, attempt.blockedTil.Sub(now)
}

func (l *loginRateLimiter) recordFailure(key string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	attempt := l.attempts[key]
	if attempt.firstSeen.IsZero() || now.Sub(attempt.firstSeen) > l.window {
		attempt = loginAttempt{firstSeen: now}
	}
	attempt.count++
	if attempt.count >= l.limit {
		attempt.blockedTil = attempt.firstSeen.Add(l.window)
		if !attempt.blockedTil.After(now) {
			attempt.blockedTil = now.Add(l.window)
		}
	}
	l.attempts[key] = attempt
}

func (l *loginRateLimiter) reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.attempts, key)
}

func (l *loginRateLimiter) cleanup(now time.Time) {
	for key, attempt := range l.attempts {
		if !attempt.blockedTil.IsZero() {
			if !now.Before(attempt.blockedTil) {
				delete(l.attempts, key)
			}
			continue
		}
		if now.Sub(attempt.firstSeen) >= l.window {
			delete(l.attempts, key)
		}
	}
}
