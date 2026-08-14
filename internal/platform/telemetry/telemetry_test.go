package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

func TestMetrics_RegistrationAndRecording(t *testing.T) {
	defer goleak.VerifyNone(t)

	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)
	require.NotNil(t, m)

	// Record technical metrics
	m.RecordHTTPRequest("POST", "/api/v1/orders", http.StatusCreated, 150*time.Millisecond)
	m.DBConnectionsActive.Set(12)
	m.RecordKafkaPublished("shopflow.orders")
	m.RecordKafkaConsumed("shopflow.orders", "shopflow-inbox-group")

	// Record business metrics
	m.RecordOrderCreated(12500)
	m.RecordInventoryReservation("SUCCESS")
	m.RecordSagaTransition("PENDING", "CONFIRMED", "SUCCESS")

	// Scrape via Handler
	handler := Handler(reg)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()

	// Verify Prometheus output contains expected metrics
	assert.Contains(t, body, "http_requests_total{method=\"POST\",path=\"/api/v1/orders\",status=\"201\"} 1")
	assert.Contains(t, body, "http_request_duration_seconds_bucket")
	assert.Contains(t, body, "db_connections_active 12")
	assert.Contains(t, body, "kafka_messages_published_total{topic=\"shopflow.orders\"} 1")
	assert.Contains(t, body, "kafka_messages_consumed_total{consumer_group=\"shopflow-inbox-group\",topic=\"shopflow.orders\"} 1")
	assert.Contains(t, body, "orders_created_total 1")
	assert.Contains(t, body, "order_amount_minor_total 12500")
	assert.Contains(t, body, "inventory_reservations_total{status=\"SUCCESS\"} 1")
	assert.Contains(t, body, "saga_transitions_total{from_state=\"PENDING\",status=\"SUCCESS\",to_state=\"CONFIRMED\"} 1")
}

func TestMetricsMiddleware_ChiIntegration(t *testing.T) {
	defer goleak.VerifyNone(t)

	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)

	r := chi.NewRouter()
	r.Use(MetricsMiddleware(m))
	r.Get("/api/v1/items/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("item"))
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/items/42", nil)
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	// Scrape to verify route pattern is captured rather than dynamic parameter
	metricsHandler := Handler(reg)
	recMetrics := httptest.NewRecorder()
	reqMetrics := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	metricsHandler.ServeHTTP(recMetrics, reqMetrics)

	body := recMetrics.Body.String()
	assert.Contains(t, body, `http_requests_total{method="GET",path="/api/v1/items/{id}",status="200"} 1`)
}

func TestW3CTraceContext_ParseAndFormat(t *testing.T) {
	defer goleak.VerifyNone(t)

	validHeader := "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	tc, err := ParseTraceparent(validHeader)
	require.NoError(t, err)
	require.NotNil(t, tc)

	assert.Equal(t, "00", tc.Version)
	assert.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", tc.TraceID)
	assert.Equal(t, "00f067aa0ba902b7", tc.SpanID)
	assert.Equal(t, "01", tc.TraceFlags)
	assert.Equal(t, validHeader, tc.String())
}

func TestW3CTraceContext_InvalidParsing(t *testing.T) {
	defer goleak.VerifyNone(t)

	testCases := []struct {
		name   string
		header string
		err    error
	}{
		{"empty", "", ErrInvalidTraceparentLength},
		{"too few parts", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7", ErrInvalidTraceparentLength},
		{"unsupported version ff", "ff-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01", ErrUnsupportedVersion},
		{"all zeros trace ID", "00-00000000000000000000000000000000-00f067aa0ba902b7-01", ErrInvalidTraceID},
		{"short trace ID", "00-4bf92f3577b34da6a3ce929d0e0e473-00f067aa0ba902b7-01", ErrInvalidTraceID},
		{"invalid hex trace ID", "00-4bf92f3577b34da6a3ce929d0e0e473z-00f067aa0ba902b7-01", ErrInvalidTraceID},
		{"all zeros span ID", "00-4bf92f3577b34da6a3ce929d0e0e4736-0000000000000000-01", ErrInvalidSpanID},
		{"short span ID", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b-01", ErrInvalidSpanID},
		{"invalid hex flags", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-0z", ErrInvalidTraceFlags},
		{"too long flags", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-001", ErrInvalidTraceFlags},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			parsed, err := ParseTraceparent(tc.header)
			assert.Error(t, err)
			assert.Nil(t, parsed)
		})
	}
}

func TestW3CTraceContext_GenerateTraceContext(t *testing.T) {
	defer goleak.VerifyNone(t)

	tc1 := GenerateTraceContext()
	require.NotNil(t, tc1)
	assert.Equal(t, "00", tc1.Version)
	assert.Len(t, tc1.TraceID, 32)
	assert.Len(t, tc1.SpanID, 16)
	assert.Equal(t, "01", tc1.TraceFlags)
	assert.NotEqual(t, strings.Repeat("0", 32), tc1.TraceID)
	assert.NotEqual(t, strings.Repeat("0", 16), tc1.SpanID)

	tc2 := GenerateTraceContext()
	require.NotNil(t, tc2)
	assert.NotEqual(t, tc1.TraceID, tc2.TraceID, "subsequent generated traces must be unique")
}

func TestTracingMiddleware_ValidAndFallback(t *testing.T) {
	defer goleak.VerifyNone(t)

	handler := TracingMiddleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tc, ok := TraceContextFromContext(r.Context())
		require.True(t, ok)
		require.NotNil(t, tc)

		traceID := TraceIDFromContext(r.Context())
		assert.Equal(t, tc.TraceID, traceID)

		w.WriteHeader(http.StatusOK)
	}))

	// 1. With incoming valid traceparent & tracestate
	incoming := "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("traceparent", incoming)
	req.Header.Set("tracestate", "vendor=data")

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, incoming, rec.Header().Get("traceparent"))
	assert.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", rec.Header().Get("X-Trace-ID"))
	assert.Equal(t, "vendor=data", rec.Header().Get("tracestate"))

	// 2. With invalid traceparent -> fallback to new generated traceparent
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/test", nil)
	req2.Header.Set("traceparent", "invalid-trace")

	handler.ServeHTTP(rec2, req2)

	assert.Equal(t, http.StatusOK, rec2.Code)
	generated := rec2.Header().Get("traceparent")
	require.NotEmpty(t, generated)
	assert.NotEqual(t, "invalid-trace", generated)
	parsedGenerated, err := ParseTraceparent(generated)
	require.NoError(t, err)
	assert.Equal(t, parsedGenerated.TraceID, rec2.Header().Get("X-Trace-ID"))
}

func TestKafkaTraceHelpers(t *testing.T) {
	defer goleak.VerifyNone(t)

	tc := &TraceContext{
		Version:    "00",
		TraceID:    "4bf92f3577b34da6a3ce929d0e0e4736",
		SpanID:     "00f067aa0ba902b7",
		TraceFlags: "01",
		TraceState: "congo=t61rcWkgMzE",
	}

	ctx := WithTraceContext(context.Background(), tc)
	headers := make(map[string]string)
	InjectKafkaTrace(ctx, headers)

	assert.Equal(t, tc.String(), headers["traceparent"])
	assert.Equal(t, "congo=t61rcWkgMzE", headers["tracestate"])

	extracted, ok := ExtractKafkaTrace(headers)
	require.True(t, ok)
	require.NotNil(t, extracted)
	assert.Equal(t, tc.TraceID, extracted.TraceID)
	assert.Equal(t, tc.SpanID, extracted.SpanID)
	assert.Equal(t, tc.TraceFlags, extracted.TraceFlags)
	assert.Equal(t, tc.TraceState, extracted.TraceState)

	// Context with Kafka Trace
	enrichedCtx := ContextWithKafkaTrace(context.Background(), headers)
	traceID := TraceIDFromContext(enrichedCtx)
	assert.Equal(t, tc.TraceID, traceID)
}

func TestHealthProbes_Liveness(t *testing.T) {
	defer goleak.VerifyNone(t)

	startTime := time.Now().Add(-5 * time.Minute)
	handler := LivenessHandler(startTime)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health/live", nil)
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "application/json", rec.Header().Get("Content-Type"))

	var resp LivenessResponse
	err := json.NewDecoder(rec.Body).Decode(&resp)
	require.NoError(t, err)
	assert.Equal(t, "UP", resp.Status)
	assert.NotEmpty(t, resp.Uptime)
}

func TestHealthProbes_Readiness(t *testing.T) {
	defer goleak.VerifyNone(t)

	// 1. All healthy dependencies
	mockDB := PingerFunc(func(ctx context.Context) error { return nil })
	mockRedis := PingerFunc(func(ctx context.Context) error { return nil })
	mockKafka := PingerFunc(func(ctx context.Context) error { return nil })

	handler := ReadinessHandler(mockDB, mockRedis, mockKafka)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health/ready", nil)
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var resp ReadinessResponse
	err := json.NewDecoder(rec.Body).Decode(&resp)
	require.NoError(t, err)
	assert.Equal(t, "UP", resp.Status)
	assert.Equal(t, "UP", resp.Checks["database"])
	assert.Equal(t, "UP", resp.Checks["redis"])
	assert.Equal(t, "UP", resp.Checks["kafka"])

	// 2. Degraded dependency (PostgreSQL error)
	failingDB := PingerFunc(func(ctx context.Context) error { return errors.New("connection refused") })
	degradedHandler := ReadinessHandler(failingDB, mockRedis, mockKafka)

	recDegraded := httptest.NewRecorder()
	reqDegraded := httptest.NewRequest(http.MethodGet, "/health/ready", nil)
	degradedHandler.ServeHTTP(recDegraded, reqDegraded)

	require.Equal(t, http.StatusServiceUnavailable, recDegraded.Code)
	var degradedResp ReadinessResponse
	err = json.NewDecoder(recDegraded.Body).Decode(&degradedResp)
	require.NoError(t, err)
	assert.Equal(t, "DOWN", degradedResp.Status)
	assert.Contains(t, degradedResp.Checks["database"], "DOWN: connection refused")
	assert.Equal(t, "UP", degradedResp.Checks["redis"])
	assert.Equal(t, "UP", degradedResp.Checks["kafka"])

	// 3. Degraded dependency (Kafka error)
	failingKafka := PingerFunc(func(ctx context.Context) error { return errors.New("broker unreachable") })
	kafkaDegradedHandler := ReadinessHandler(mockDB, mockRedis, failingKafka)

	recKafka := httptest.NewRecorder()
	reqKafka := httptest.NewRequest(http.MethodGet, "/health/ready", nil)
	kafkaDegradedHandler.ServeHTTP(recKafka, reqKafka)

	require.Equal(t, http.StatusServiceUnavailable, recKafka.Code)
	var kafkaResp ReadinessResponse
	err = json.NewDecoder(recKafka.Body).Decode(&kafkaResp)
	require.NoError(t, err)
	assert.Equal(t, "DOWN", kafkaResp.Status)
	assert.Contains(t, kafkaResp.Checks["kafka"], "DOWN: broker unreachable")
}

// Unused dummy to silence linter
var _ = io.Discard
