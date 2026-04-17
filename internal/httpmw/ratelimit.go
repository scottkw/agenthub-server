// Package httpmw provides HTTP middlewares shared across the API surface.
package httpmw

import (
	"net"
	"net/http"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

type RateLimitConfig struct {
	RequestsPerSecond float64
	Burst             int
	TTL               time.Duration
}

type bucket struct {
	lim  *rate.Limiter
	seen time.Time
}

// NewRateLimit returns middleware that allows up to Burst requests immediately
// per IP, then refills at RequestsPerSecond. Stale buckets older than TTL are
// evicted lazily on each request.
func NewRateLimit(cfg RateLimitConfig) func(http.Handler) http.Handler {
	if cfg.TTL == 0 {
		cfg.TTL = 5 * time.Minute
	}
	var (
		mu      sync.Mutex
		buckets = map[string]*bucket{}
	)

	get := func(ip string) *rate.Limiter {
		mu.Lock()
		defer mu.Unlock()
		now := time.Now()
		for k, v := range buckets {
			if now.Sub(v.seen) > cfg.TTL {
				delete(buckets, k)
			}
		}
		b, ok := buckets[ip]
		if !ok {
			b = &bucket{lim: rate.NewLimiter(rate.Limit(cfg.RequestsPerSecond), cfg.Burst)}
			buckets[ip] = b
		}
		b.seen = now
		return b.lim
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := clientIP(r)
			lim := get(ip)
			if !lim.Allow() {
				http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
