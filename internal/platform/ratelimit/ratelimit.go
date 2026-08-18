// Package ratelimit implements token bucket rate limiting via Redis Lua script.
// On Redis unavailability, behavior is configurable per route: allow-through (public reads)
// or deny-with-429 (critical write endpoints like order creation).
package ratelimit

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// FailurePolicy determines behavior when Redis is unavailable.
type FailurePolicy int

const (
	// FailOpen allows requests through when Redis is unavailable (safe for read endpoints).
	FailOpen FailurePolicy = iota
	// FailClosed denies requests with 429 when Redis is unavailable (safe for write endpoints).
	FailClosed
)

// Config holds rate limiter settings for a single route group.
type Config struct {
	// Requests is the maximum number of requests allowed per Window.
	Requests int
	// Window is the time window for the token bucket.
	Window time.Duration
	// FailurePolicy controls behavior when Redis is unavailable.
	FailurePolicy FailurePolicy
	// KeyFunc extracts the rate limit key from the request (e.g. user ID or IP).
	KeyFunc func(r *http.Request) string
}

// Limiter wraps a Redis client and provides HTTP middleware.
type Limiter struct {
	rdb    *redis.Client
	logger *slog.Logger
}

// NewLimiter creates a new rate limiter backed by Redis.
func NewLimiter(rdb *redis.Client, logger *slog.Logger) *Limiter {
	if logger == nil {
		logger = slog.Default()
	}
	return &Limiter{rdb: rdb, logger: logger}
}

// tokenBucketScript implements a sliding window token bucket in Lua.
// KEYS[1] = rate limit key
// ARGV[1] = max requests (capacity)
// ARGV[2] = window in seconds
// ARGV[3] = current timestamp (unix seconds)
// Returns: {allowed (0|1), remaining, reset_after_seconds}
var tokenBucketScript = redis.NewScript(`
local key      = KEYS[1]
local capacity = tonumber(ARGV[1])
local window   = tonumber(ARGV[2])
local now      = tonumber(ARGV[3])

local data     = redis.call("HMGET", key, "tokens", "last_refill")
local tokens   = tonumber(data[1])
local last     = tonumber(data[2])

if tokens == nil then
	tokens = capacity
	last   = now
end

-- Refill tokens proportionally to elapsed time
local elapsed = math.max(0, now - last)
local refill  = math.floor(elapsed * capacity / window)
tokens        = math.min(capacity, tokens + refill)
last          = now

if tokens > 0 then
	tokens = tokens - 1
	redis.call("HMSET", key, "tokens", tokens, "last_refill", last)
	redis.call("EXPIRE", key, window * 2)
	return {1, tokens, 0}
else
	redis.call("HMSET", key, "tokens", tokens, "last_refill", last)
	redis.call("EXPIRE", key, window * 2)
	local reset = window - elapsed
	return {0, 0, math.ceil(reset)}
end
`)

// Middleware returns an HTTP middleware that enforces the given rate limit Config.
func (l *Limiter) Middleware(prefix string, cfg Config) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := fmt.Sprintf("ratelimit:%s:%s", prefix, cfg.KeyFunc(r))
			windowSec := int64(cfg.Window.Seconds())
			now := time.Now().Unix()

			ctx, cancel := context.WithTimeout(r.Context(), 100*time.Millisecond)
			defer cancel()

			result, err := tokenBucketScript.Run(ctx, l.rdb, []string{key},
				cfg.Requests, windowSec, now,
			).Int64Slice()

			if err != nil {
				l.logger.Warn("rate limiter redis error",
					"key", key,
					"error", err,
					"policy", cfg.FailurePolicy,
				)
				if cfg.FailurePolicy == FailClosed {
					http.Error(w, `{"error":"rate limit service unavailable"}`, http.StatusTooManyRequests)
					return
				}
				// FailOpen: allow through
				next.ServeHTTP(w, r)
				return
			}

			allowed := result[0] == 1
			remaining := result[1]
			resetAfter := result[2]

			w.Header().Set("X-RateLimit-Limit", strconv.Itoa(cfg.Requests))
			w.Header().Set("X-RateLimit-Remaining", strconv.FormatInt(remaining, 10))
			w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(now+resetAfter, 10))

			if !allowed {
				w.Header().Set("Retry-After", strconv.FormatInt(resetAfter, 10))
				l.logger.Info("rate limit exceeded",
					"key", key,
					"retry_after_sec", resetAfter,
				)
				http.Error(w, `{"error":"rate limit exceeded","retry_after":`+strconv.FormatInt(resetAfter, 10)+`}`, http.StatusTooManyRequests)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// IPKeyFunc extracts the client IP from the request, respecting X-Forwarded-For
// only from trusted proxies (localhost/private ranges). Use for anonymous endpoints.
func IPKeyFunc(r *http.Request) string {
	// Only trust X-Forwarded-For from private/loopback IPs (i.e. your reverse proxy)
	remoteIP, _, _ := net.SplitHostPort(r.RemoteAddr)
	if isPrivateIP(remoteIP) {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			return strings.TrimSpace(parts[0])
		}
	}
	return remoteIP
}

// UserIDKeyFunc extracts the authenticated user ID from context.
// Falls back to IP if no user ID is present.
// The user ID must be stored in context under the "user_id" key by auth middleware.
func UserIDKeyFunc(r *http.Request) string {
	if uid, ok := r.Context().Value(userIDContextKey{}).(string); ok && uid != "" {
		return uid
	}
	return IPKeyFunc(r)
}

type userIDContextKey struct{}

// UserIDContextKey is the context key for the authenticated user ID.
// Auth middleware must store the user ID using this key.
var UserIDContextKey = userIDContextKey{}

func isPrivateIP(ip string) bool {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return false
	}
	privateRanges := []string{
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"127.0.0.0/8",
		"::1/128",
		"fc00::/7",
	}
	for _, cidr := range privateRanges {
		_, network, err := net.ParseCIDR(cidr)
		if err != nil {
			continue
		}
		if network.Contains(parsed) {
			return true
		}
	}
	return false
}
