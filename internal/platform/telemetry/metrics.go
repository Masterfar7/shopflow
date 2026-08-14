package telemetry

import (
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics encapsulates all Prometheus technical and business metrics for ShopFlow.
type Metrics struct {
	// Technical metrics
	HTTPRequestsTotal           *prometheus.CounterVec
	HTTPRequestDurationSeconds  *prometheus.HistogramVec
	DBConnectionsActive         prometheus.Gauge
	KafkaMessagesPublishedTotal *prometheus.CounterVec
	KafkaMessagesConsumedTotal  *prometheus.CounterVec

	// Business metrics
	OrdersCreatedTotal         prometheus.Counter
	OrderAmountMinorTotal      prometheus.Counter
	InventoryReservationsTotal *prometheus.CounterVec
	SagaTransitionsTotal       *prometheus.CounterVec

	registry prometheus.Registerer
}

var (
	defaultMetricsOnce sync.Once
	defaultMetricsInst *Metrics
)

// NewMetrics initializes and registers Prometheus metrics to the provided Registerer.
// If reg is nil, prometheus.DefaultRegisterer is used.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	if reg == nil {
		reg = prometheus.DefaultRegisterer
	}

	m := &Metrics{
		registry: reg,

		// Technical metrics
		HTTPRequestsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "http_requests_total",
				Help: "Total number of HTTP requests processed by ShopFlow.",
			},
			[]string{"method", "path", "status"},
		),

		HTTPRequestDurationSeconds: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "http_request_duration_seconds",
				Help:    "HTTP request latency distributions in seconds.",
				Buckets: prometheus.DefBuckets,
			},
			[]string{"method", "path", "status"},
		),

		DBConnectionsActive: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Name: "db_connections_active",
				Help: "Current count of active database connections in the pool.",
			},
		),

		KafkaMessagesPublishedTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "kafka_messages_published_total",
				Help: "Total number of Kafka messages published to brokers.",
			},
			[]string{"topic"},
		),

		KafkaMessagesConsumedTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "kafka_messages_consumed_total",
				Help: "Total number of Kafka messages processed by consumers.",
			},
			[]string{"topic", "consumer_group"},
		),

		// Business metrics
		OrdersCreatedTotal: prometheus.NewCounter(
			prometheus.CounterOpts{
				Name: "orders_created_total",
				Help: "Total count of orders created across the platform.",
			},
		),

		OrderAmountMinorTotal: prometheus.NewCounter(
			prometheus.CounterOpts{
				Name: "order_amount_minor_total",
				Help: "Total cumulative monetary value of created orders in minor currency units (cents).",
			},
		),

		InventoryReservationsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "inventory_reservations_total",
				Help: "Total inventory reservation attempts partitioned by outcome status.",
			},
			[]string{"status"},
		),

		SagaTransitionsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "saga_transitions_total",
				Help: "Total saga FSM state transitions executed.",
			},
			[]string{"from_state", "to_state", "status"},
		),
	}

	// Register collectors safely
	reg.MustRegister(
		m.HTTPRequestsTotal,
		m.HTTPRequestDurationSeconds,
		m.DBConnectionsActive,
		m.KafkaMessagesPublishedTotal,
		m.KafkaMessagesConsumedTotal,
		m.OrdersCreatedTotal,
		m.OrderAmountMinorTotal,
		m.InventoryReservationsTotal,
		m.SagaTransitionsTotal,
	)

	return m
}

// DefaultMetrics returns the singleton metrics instance using prometheus.DefaultRegisterer.
func DefaultMetrics() *Metrics {
	defaultMetricsOnce.Do(func() {
		defaultMetricsInst = NewMetrics(prometheus.DefaultRegisterer)
	})
	return defaultMetricsInst
}

// RecordHTTPRequest updates HTTP request count and duration histograms.
func (m *Metrics) RecordHTTPRequest(method, path string, status int, duration time.Duration) {
	statusStr := strconv.Itoa(status)
	m.HTTPRequestsTotal.WithLabelValues(method, path, statusStr).Inc()
	m.HTTPRequestDurationSeconds.WithLabelValues(method, path, statusStr).Observe(duration.Seconds())
}

// RecordKafkaPublished increments kafka_messages_published_total for a topic.
func (m *Metrics) RecordKafkaPublished(topic string) {
	m.KafkaMessagesPublishedTotal.WithLabelValues(topic).Inc()
}

// RecordKafkaConsumed increments kafka_messages_consumed_total for a topic and consumer group.
func (m *Metrics) RecordKafkaConsumed(topic, consumerGroup string) {
	m.KafkaMessagesConsumedTotal.WithLabelValues(topic, consumerGroup).Inc()
}

// RecordOrderCreated records business indicators for an order creation.
func (m *Metrics) RecordOrderCreated(amountMinor int64) {
	m.OrdersCreatedTotal.Inc()
	if amountMinor > 0 {
		m.OrderAmountMinorTotal.Add(float64(amountMinor))
	}
}

// RecordInventoryReservation records an inventory reservation result.
func (m *Metrics) RecordInventoryReservation(status string) {
	m.InventoryReservationsTotal.WithLabelValues(status).Inc()
}

// RecordSagaTransition records an FSM state transition.
func (m *Metrics) RecordSagaTransition(fromState, toState, status string) {
	m.SagaTransitionsTotal.WithLabelValues(fromState, toState, status).Inc()
}

// MetricsMiddleware creates a Chi router HTTP middleware to observe method, path, status, and duration.
func MetricsMiddleware(m *Metrics) func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)

			next.ServeHTTP(ww, r)

			duration := time.Since(start)
			path := r.URL.Path
			if rctx := chi.RouteContext(r.Context()); rctx != nil {
				pattern := rctx.RoutePattern()
				if pattern != "" {
					path = pattern
				}
			}

			status := ww.Status()
			if status == 0 {
				status = http.StatusOK
			}

			if m != nil {
				m.RecordHTTPRequest(r.Method, path, status, duration)
			}
		})
	}
}

// Handler returns an http.Handler that serves Prometheus metrics.
// If gatherer is nil, prometheus.DefaultGatherer is used.
func Handler(gatherers ...prometheus.Gatherer) http.Handler {
	if len(gatherers) > 0 && gatherers[0] != nil {
		return promhttp.HandlerFor(gatherers[0], promhttp.HandlerOpts{})
	}
	return promhttp.Handler()
}
