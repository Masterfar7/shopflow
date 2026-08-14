package telemetry

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

// TestTelemetry_ConcurrentMetricsRecording tests thread safety of all metric operations.
func TestTelemetry_ConcurrentMetricsRecording(t *testing.T) {
	defer goleak.VerifyNone(t)

	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)
	require.NotNil(t, m)

	numGoroutines := 20
	iterations := 50
	var wg sync.WaitGroup

	for g := 0; g < numGoroutines; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				m.RecordHTTPRequest("POST", "/api/v1/orders", 201, 10*time.Millisecond)
				m.DBConnectionsActive.Set(float64(id))
				m.RecordKafkaPublished("shopflow.orders")
				m.RecordKafkaConsumed("shopflow.orders", "inbox-group")
				m.RecordOrderCreated(100)
				m.RecordInventoryReservation("SUCCESS")
				m.RecordSagaTransition("INIT", "CONFIRMED", "SUCCESS")
			}
		}(g)
	}

	wg.Wait()

	// Scrape metrics handler
	handler := Handler(reg)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	totalCalls := numGoroutines * iterations
	assert.Contains(t, body, fmt.Sprintf("orders_created_total %d", totalCalls))
}

// TestTelemetry_ConcurrentTracePropagation tests thread-safe trace generation and middleware handling.
func TestTelemetry_ConcurrentTracePropagation(t *testing.T) {
	defer goleak.VerifyNone(t)

	handler := TracingMiddleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tc, ok := TraceContextFromContext(r.Context())
		if !ok || tc == nil {
			http.Error(w, "missing trace context", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))

	numGoroutines := 30
	var wg sync.WaitGroup

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/health/live", nil)
			if idx%2 == 0 {
				req.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
			}

			handler.ServeHTTP(rec, req)

			assert.Equal(t, http.StatusOK, rec.Code)
			traceparent := rec.Header().Get("traceparent")
			assert.NotEmpty(t, traceparent)
			traceID := rec.Header().Get("X-Trace-ID")
			assert.NotEmpty(t, traceID)
			assert.Contains(t, traceparent, traceID)
		}(i)
	}

	wg.Wait()
}

// TestHealthProbes_Readiness_AllCombinations tests all combinations of healthy and unhealthy states.
func TestHealthProbes_Readiness_AllCombinations(t *testing.T) {
	defer goleak.VerifyNone(t)

	cases := []struct {
		name         string
		dbErr        error
		redisErr     error
		kafkaErr     error
		expectStatus int
		expectHealth string
	}{
		{"all healthy", nil, nil, nil, http.StatusOK, "UP"},
		{"db down", errors.New("db down"), nil, nil, http.StatusServiceUnavailable, "DOWN"},
		{"redis down", nil, errors.New("redis down"), nil, http.StatusServiceUnavailable, "DOWN"},
		{"kafka down", nil, nil, errors.New("kafka down"), http.StatusServiceUnavailable, "DOWN"},
		{"all down", errors.New("db"), errors.New("redis"), errors.New("kafka"), http.StatusServiceUnavailable, "DOWN"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var dbPinger Pinger
			if tc.dbErr != nil {
				dbPinger = PingerFunc(func(ctx context.Context) error { return tc.dbErr })
			} else {
				dbPinger = PingerFunc(func(ctx context.Context) error { return nil })
			}

			var redisPinger Pinger
			if tc.redisErr != nil {
				redisPinger = PingerFunc(func(ctx context.Context) error { return tc.redisErr })
			} else {
				redisPinger = PingerFunc(func(ctx context.Context) error { return nil })
			}

			var kafkaPinger Pinger
			if tc.kafkaErr != nil {
				kafkaPinger = PingerFunc(func(ctx context.Context) error { return tc.kafkaErr })
			} else {
				kafkaPinger = PingerFunc(func(ctx context.Context) error { return nil })
			}

			handler := ReadinessHandler(dbPinger, redisPinger, kafkaPinger)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/health/ready", nil)
			handler.ServeHTTP(rec, req)

			assert.Equal(t, tc.expectStatus, rec.Code)
			assert.Contains(t, rec.Body.String(), tc.expectHealth)
		})
	}
}
