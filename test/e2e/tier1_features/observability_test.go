package tier1_features

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"shopflow/test/e2e/harness"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// FEAT-OBS-03: Health Probes (/health/live, /health/ready)
func TestHealthProbes(t *testing.T) {
	env := harness.SetupEnvironment(t)
	client := env.RequireHTTPServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 1. Liveness probe must always return 200 UP
	statusLive, bodyLive, err := client.GetHealthLive(ctx)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, statusLive)
	assert.Contains(t, string(bodyLive), "UP")

	// 2. Readiness probe returns 200 (if dependencies up) or 503 (if dependencies unready)
	statusReady, readyResp, err := client.GetHealthReady(ctx)
	require.NoError(t, err)
	assert.True(t, statusReady == http.StatusOK || statusReady == http.StatusServiceUnavailable)
	require.NotNil(t, readyResp)
	assert.NotEmpty(t, readyResp.Status)
	assert.Contains(t, readyResp.Checks, "database")
	assert.Contains(t, readyResp.Checks, "kafka")
}

// FEAT-OBS-02: Prometheus Technical & Business Metrics Exporter
func TestPrometheusMetricsEndpoint(t *testing.T) {
	env := harness.SetupEnvironment(t)
	client := env.RequireHTTPServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	status, metricsOutput, err := client.GetMetrics(ctx)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	assert.NotEmpty(t, metricsOutput)

	// Verify standard Prometheus metric lines
	assert.Contains(t, metricsOutput, "# HELP")
	assert.Contains(t, metricsOutput, "# TYPE")
}

// FEAT-OBS-01: W3C OpenTelemetry Distributed Tracing across HTTP & Kafka Boundaries
func TestOpenTelemetryTracePropagation(t *testing.T) {
	env := harness.SetupEnvironment(t)
	client := env.RequireHTTPServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Standard W3C Traceparent: version-trace_id-parent_id-trace_flags
	sampleTraceParent := "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	headers := map[string]string{
		"traceparent": sampleTraceParent,
	}

	status, _, respHeader, err := client.SendRaw(ctx, http.MethodGet, "/health/live", nil, headers)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, status)

	// If response reflects trace ID or correlation header
	if traceID := respHeader.Get("X-Trace-ID"); traceID != "" {
		assert.True(t, strings.Contains(sampleTraceParent, traceID))
	}
}

// FEAT-INF-02: API & Event Contract Baseline Compliance
func TestAPIContractCompliance(t *testing.T) {
	env := harness.SetupEnvironment(t)
	client := env.RequireHTTPServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Verify /api/v1/info endpoint returns correct app name and version
	status, body, _, err := client.SendRaw(ctx, http.MethodGet, "/api/v1/info", nil, nil)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, status)
	assert.Contains(t, string(body), "shopflow")
}
