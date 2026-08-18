package ratelimit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockRedis is a minimal in-memory stub for unit tests.
// For real Redis behavior, use testcontainers integration tests.
type mockRedisClient struct {
	data map[string]map[string]string
}

func TestIPKeyFunc_WithPrivateProxy(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("X-Forwarded-For", "203.0.113.42, 10.0.0.1")
	assert.Equal(t, "203.0.113.42", IPKeyFunc(req))
}

func TestIPKeyFunc_WithPublicRemote(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "203.0.113.1:12345"
	req.Header.Set("X-Forwarded-For", "1.2.3.4")
	// Public remote IP should NOT trust X-Forwarded-For
	assert.Equal(t, "203.0.113.1", IPKeyFunc(req))
}

func TestUserIDKeyFunc_FallsBackToIP(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "192.168.1.10:9999"
	// No user_id in context → fallback to IP
	key := UserIDKeyFunc(req)
	assert.Equal(t, "192.168.1.10", key)
}

func TestIsPrivateIP(t *testing.T) {
	cases := []struct {
		ip      string
		private bool
	}{
		{"127.0.0.1", true},
		{"10.0.0.1", true},
		{"172.16.5.5", true},
		{"192.168.0.1", true},
		{"8.8.8.8", false},
		{"203.0.113.42", false},
		{"::1", true},
		{"2001:db8::1", false},
	}
	for _, tc := range cases {
		t.Run(tc.ip, func(t *testing.T) {
			assert.Equal(t, tc.private, isPrivateIP(tc.ip), "ip=%s", tc.ip)
		})
	}
}

// TestMiddleware_FailOpen verifies that when Redis is unavailable and policy is FailOpen,
// the request is allowed through.
func TestMiddleware_FailOpen(t *testing.T) {
	// Use an address that will never connect
	rdb := redis.NewClient(&redis.Options{
		Addr:        "localhost:19999",
		DialTimeout: 50 * time.Millisecond,
	})
	defer rdb.Close()

	limiter := NewLimiter(rdb, nil)
	cfg := Config{
		Requests:      10,
		Window:        time.Minute,
		FailurePolicy: FailOpen,
		KeyFunc:       IPKeyFunc,
	}

	called := false
	handler := limiter.Middleware("test", cfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "1.2.3.4:5678"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	require.True(t, called, "FailOpen: request should reach handler when Redis is down")
	assert.Equal(t, http.StatusOK, w.Code)
}

// TestMiddleware_FailClosed verifies that when Redis is unavailable and policy is FailClosed,
// the request is rejected with 429.
func TestMiddleware_FailClosed(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{
		Addr:        "localhost:19999",
		DialTimeout: 50 * time.Millisecond,
	})
	defer rdb.Close()

	limiter := NewLimiter(rdb, nil)
	cfg := Config{
		Requests:      10,
		Window:        time.Minute,
		FailurePolicy: FailClosed,
		KeyFunc:       IPKeyFunc,
	}

	called := false
	handler := limiter.Middleware("test", cfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "1.2.3.4:5678"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	require.False(t, called, "FailClosed: handler should NOT be called when Redis is down")
	assert.Equal(t, http.StatusTooManyRequests, w.Code)
}

// TestMiddleware_WithRealRedis runs against a real Redis if REDIS_TEST_ADDR is set.
// Otherwise skipped. Run via: REDIS_TEST_ADDR=localhost:6379 go test ./...
func TestMiddleware_WithRealRedis(t *testing.T) {
	addr := redisTestAddr(t)

	rdb := redis.NewClient(&redis.Options{Addr: addr})
	defer rdb.Close()

	ctx := context.Background()
	require.NoError(t, rdb.Ping(ctx).Err(), "Redis must be reachable")

	limiter := NewLimiter(rdb, nil)
	cfg := Config{
		Requests:      3,
		Window:        10 * time.Second,
		FailurePolicy: FailOpen,
		KeyFunc:       IPKeyFunc,
	}

	// Clean up key before test
	key := "ratelimit:rl_test:1.2.3.4"
	rdb.Del(ctx, key)

	handler := limiter.Middleware("rl_test", cfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// First 3 requests should succeed
	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "1.2.3.4:5678"
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code, "request %d should succeed", i+1)
	}

	// 4th request should be rate limited
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "1.2.3.4:5678"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	assert.Equal(t, http.StatusTooManyRequests, w.Code, "4th request should be rate limited")
	assert.NotEmpty(t, w.Header().Get("Retry-After"))

	rdb.Del(ctx, key)
}

func redisTestAddr(t *testing.T) string {
	t.Helper()
	addr := "localhost:6379"
	rdb := redis.NewClient(&redis.Options{Addr: addr, DialTimeout: 200 * time.Millisecond})
	defer rdb.Close()
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		t.Skipf("Redis not available at %s: %v", addr, err)
	}
	return addr
}
