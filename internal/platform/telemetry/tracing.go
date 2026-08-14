package telemetry

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

const (
	// HeaderTraceparent is the standard W3C trace context header name.
	HeaderTraceparent = "traceparent"
	// HeaderTracestate is the standard W3C tracestate header name.
	HeaderTracestate = "tracestate"
	// HeaderCorrelationTraceID is standard correlation header for quick client lookups.
	HeaderCorrelationTraceID = "X-Trace-ID"

	// SupportedVersion is the current W3C Trace Context version.
	SupportedVersion = "00"
)

var (
	ErrInvalidTraceparentLength  = errors.New("traceparent header must have at least 4 hyphen-separated fields")
	ErrUnsupportedVersion        = errors.New("unsupported traceparent version")
	ErrInvalidTraceID            = errors.New("trace-id must be a 32-hex character non-zero string")
	ErrInvalidSpanID             = errors.New("span-id must be a 16-hex character non-zero string")
	ErrInvalidTraceFlags         = errors.New("trace-flags must be a 2-hex character string")
	allZerosTraceID              = strings.Repeat("0", 32)
	allZerosSpanID               = strings.Repeat("0", 16)
)

// TraceContext represents a parsed W3C Trace Context structure.
type TraceContext struct {
	Version    string `json:"version"`
	TraceID    string `json:"trace_id"`
	SpanID     string `json:"span_id"`
	TraceFlags string `json:"trace_flags"`
	TraceState string `json:"trace_state,omitempty"`
}

// String formats the TraceContext into the standard W3C traceparent string:
// 00-<32hex trace_id>-<16hex parent_id>-<02hex flags>
func (tc *TraceContext) String() string {
	if tc == nil {
		return ""
	}
	ver := tc.Version
	if ver == "" {
		ver = SupportedVersion
	}
	flags := tc.TraceFlags
	if flags == "" {
		flags = "01"
	}
	return fmt.Sprintf("%s-%s-%s-%s", ver, tc.TraceID, tc.SpanID, flags)
}

// ParseTraceparent parses a W3C traceparent header string.
func ParseTraceparent(raw string) (*TraceContext, error) {
	trimmed := strings.TrimSpace(raw)
	parts := strings.Split(trimmed, "-")
	if len(parts) < 4 {
		return nil, ErrInvalidTraceparentLength
	}

	version := parts[0]
	traceID := parts[1]
	spanID := parts[2]
	flags := parts[3]

	// Version validation
	if len(version) != 2 || version == "ff" {
		return nil, ErrUnsupportedVersion
	}
	if version == "00" && len(parts) != 4 {
		return nil, ErrInvalidTraceparentLength
	}

	// TraceID validation: 32 hex chars, not all zeros
	if len(traceID) != 32 || traceID == allZerosTraceID {
		return nil, ErrInvalidTraceID
	}
	if _, err := hex.DecodeString(traceID); err != nil {
		return nil, ErrInvalidTraceID
	}

	// SpanID validation: 16 hex chars, not all zeros
	if len(spanID) != 16 || spanID == allZerosSpanID {
		return nil, ErrInvalidSpanID
	}
	if _, err := hex.DecodeString(spanID); err != nil {
		return nil, ErrInvalidSpanID
	}

	// TraceFlags validation: 2 hex chars
	if len(flags) != 2 {
		return nil, ErrInvalidTraceFlags
	}
	if _, err := hex.DecodeString(flags); err != nil {
		return nil, ErrInvalidTraceFlags
	}

	return &TraceContext{
		Version:    version,
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: flags,
	}, nil
}

// GenerateTraceContext creates a new valid W3C TraceContext with cryptographically random IDs.
func GenerateTraceContext() *TraceContext {
	var traceBytes [16]byte
	var spanBytes [8]byte

	_, _ = rand.Read(traceBytes[:])
	_, _ = rand.Read(spanBytes[:])

	// Ensure non-zero
	if traceBytes == [16]byte{} {
		traceBytes[15] = 1
	}
	if spanBytes == [8]byte{} {
		spanBytes[7] = 1
	}

	return &TraceContext{
		Version:    SupportedVersion,
		TraceID:    hex.EncodeToString(traceBytes[:]),
		SpanID:     hex.EncodeToString(spanBytes[:]),
		TraceFlags: "01", // Sampled / recorded
	}
}

type traceContextKey struct{}

// WithTraceContext returns a copy of parent context carrying the TraceContext.
func WithTraceContext(ctx context.Context, tc *TraceContext) context.Context {
	return context.WithValue(ctx, traceContextKey{}, tc)
}

// TraceContextFromContext retrieves the TraceContext stored in ctx, if any.
func TraceContextFromContext(ctx context.Context) (*TraceContext, bool) {
	if ctx == nil {
		return nil, false
	}
	tc, ok := ctx.Value(traceContextKey{}).(*TraceContext)
	return tc, ok && tc != nil
}

// TraceIDFromContext extracts the 32-hex trace ID from context, or empty string if not present.
func TraceIDFromContext(ctx context.Context) string {
	if tc, ok := TraceContextFromContext(ctx); ok && tc != nil {
		return tc.TraceID
	}
	return ""
}

// TracingMiddleware extracts incoming W3C traceparent (and tracestate), or generates a new one.
// It stores the TraceContext in request context and reflects traceparent & X-Trace-ID on responses.
func TracingMiddleware() func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rawTraceparent := r.Header.Get(HeaderTraceparent)
			tc, err := ParseTraceparent(rawTraceparent)
			if err != nil || tc == nil {
				// Fallback to generating a valid fresh TraceContext
				tc = GenerateTraceContext()
			}

			// Capture optional tracestate header
			if state := r.Header.Get(HeaderTracestate); state != "" {
				tc.TraceState = strings.TrimSpace(state)
			}

			// Set headers on response
			w.Header().Set(HeaderTraceparent, tc.String())
			w.Header().Set(HeaderCorrelationTraceID, tc.TraceID)
			if tc.TraceState != "" {
				w.Header().Set(HeaderTracestate, tc.TraceState)
			}

			// Inject into context
			ctx := WithTraceContext(r.Context(), tc)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// InjectKafkaTrace writes the TraceContext from context into Kafka message headers.
func InjectKafkaTrace(ctx context.Context, headers map[string]string) {
	if headers == nil {
		return
	}
	tc, ok := TraceContextFromContext(ctx)
	if !ok || tc == nil {
		tc = GenerateTraceContext()
	}
	headers[HeaderTraceparent] = tc.String()
	if tc.TraceState != "" {
		headers[HeaderTracestate] = tc.TraceState
	}
}

// ExtractKafkaTrace extracts TraceContext from Kafka message headers.
func ExtractKafkaTrace(headers map[string]string) (*TraceContext, bool) {
	if headers == nil {
		return nil, false
	}
	raw, ok := headers[HeaderTraceparent]
	if !ok || raw == "" {
		return nil, false
	}
	tc, err := ParseTraceparent(raw)
	if err != nil {
		return nil, false
	}
	if state, hasState := headers[HeaderTracestate]; hasState {
		tc.TraceState = state
	}
	return tc, true
}

// ContextWithKafkaTrace extracts trace from headers or generates a new one, returning an enriched context.
func ContextWithKafkaTrace(ctx context.Context, headers map[string]string) context.Context {
	tc, ok := ExtractKafkaTrace(headers)
	if !ok || tc == nil {
		tc = GenerateTraceContext()
	}
	return WithTraceContext(ctx, tc)
}
