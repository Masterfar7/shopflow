package notification_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"shopflow/internal/domain/inbox"
	"shopflow/internal/domain/notification"
	"shopflow/internal/platform/email"
	"shopflow/internal/platform/kafka"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

// 1. INVARIANT TEST: Zero Floats in Domain Models & Formatter (Architecture Guidelines §4)
func TestAdversarial_ZeroFloats_Invariant(t *testing.T) {
	defer goleak.VerifyNone(t)

	// A. Reflection verification on domain types
	typesToCheck := []any{
		notification.LineItemPayload{},
		notification.OrderConfirmedPayload{},
		notification.OrderCancelledPayload{},
	}

	for _, obj := range typesToCheck {
		typ := reflect.TypeOf(obj)
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			kind := field.Type.Kind()
			assert.False(t, kind == reflect.Float32 || kind == reflect.Float64,
				"Struct %s contains floating-point field: %s (%s)", typ.Name(), field.Name, kind)
		}
	}

	// B. Extreme and boundary values in FormatMinorMoney (pure integer arithmetic)
	testCases := []struct {
		amount   int64
		currency string
		expected string
	}{
		{amount: 0, currency: "USD", expected: "$0.00"},
		{amount: 1, currency: "USD", expected: "$0.01"},
		{amount: 9, currency: "USD", expected: "$0.09"},
		{amount: 10, currency: "USD", expected: "$0.10"},
		{amount: 99, currency: "USD", expected: "$0.99"},
		{amount: 100, currency: "USD", expected: "$1.00"},
		{amount: 105, currency: "USD", expected: "$1.05"},
		{amount: -1, currency: "USD", expected: "-$0.01"},
		{amount: -99, currency: "USD", expected: "-$0.99"},
		{amount: -100, currency: "USD", expected: "-$1.00"},
		{amount: -105, currency: "USD", expected: "-$1.05"},
		{amount: 1234567890123456, currency: "USD", expected: "$12345678901234.56"},
		{amount: 1999, currency: "eur", expected: "19.99 EUR"},
		{amount: 500, currency: "  gbp  ", expected: "5.00 GBP"},
		{amount: 0, currency: "JPY", expected: "0.00 JPY"},
		{amount: 9999, currency: "", expected: "$99.99"},
	}

	for _, tc := range testCases {
		t.Run(fmt.Sprintf("%d_%s", tc.amount, tc.currency), func(t *testing.T) {
			res := notification.FormatMinorMoney(tc.amount, tc.currency)
			assert.Equal(t, tc.expected, res)
			// Assert no float symbols or IEEE-754 precision artifacts (e.g. .0000000000000004)
			assert.False(t, strings.Contains(res, "0000000000000004"), "FormatMinorMoney leaked float precision")
		})
	}
}

// 2. INVARIANT TEST: Transaction Boundary & Architecture Isolation (Architecture Guidelines §6)
func TestAdversarial_TransactionBoundary_NoDBHandle(t *testing.T) {
	defer goleak.VerifyNone(t)

	// Ensure Service struct has no database connection or transaction references
	svcType := reflect.TypeOf(notification.Service{})
	for i := 0; i < svcType.NumField(); i++ {
		field := svcType.Field(i)
		typeName := field.Type.String()
		assert.False(t, strings.Contains(strings.ToLower(typeName), "tx"),
			"notification.Service must NOT hold transaction handles: field %s is %s", field.Name, typeName)
		assert.False(t, strings.Contains(strings.ToLower(typeName), "sql"),
			"notification.Service must NOT hold SQL handles: field %s is %s", field.Name, typeName)
		assert.False(t, strings.Contains(strings.ToLower(typeName), "pool"),
			"notification.Service must NOT hold DB pool handles: field %s is %s", field.Name, typeName)
	}

	// Invariant: Verify email sending works independently with slow sender without transaction leakage
	slowSender := &slowMockSender{delay: 15 * time.Millisecond}
	renderer, err := notification.NewRenderer()
	require.NoError(t, err)

	svc := notification.NewService(slowSender, renderer, slog.Default())

	payload := notification.OrderConfirmedPayload{
		OrderID:          uuid.New(),
		CustomerEmail:    "slow@shopflow.io",
		TotalAmountMinor: 3500,
		Currency:         "USD",
		ConfirmedAt:      time.Now().UTC(),
	}
	bytesPayload, err := json.Marshal(payload)
	require.NoError(t, err)

	msg := kafka.Message{
		Topic:   "shopflow.orders",
		Headers: map[string]string{"event_type": "OrderConfirmed"},
		Value:   bytesPayload,
	}

	ctx := context.Background()
	start := time.Now()
	err = svc.HandleMessage(ctx, msg)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, time.Since(start), 15*time.Millisecond)
	assert.Equal(t, 1, slowSender.Count())
}

type slowMockSender struct {
	mu    sync.Mutex
	delay time.Duration
	count int
}

func (s *slowMockSender) Send(ctx context.Context, msg email.EmailMessage) error {
	select {
	case <-time.After(s.delay):
	case <-ctx.Done():
		return ctx.Err()
	}
	s.mu.Lock()
	s.count++
	s.mu.Unlock()
	return nil
}

func (s *slowMockSender) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.count
}

// 3. ADVERSARIAL TEST: Security & Template HTML Escaping (XSS Prevention)
func TestAdversarial_Template_HTMLEscaping(t *testing.T) {
	defer goleak.VerifyNone(t)

	renderer, err := notification.NewRenderer()
	require.NoError(t, err)

	orderID := uuid.New()
	xssTitle := "<script>alert('xss')</script> & <b>bold</b>"
	xssReason := "\"><img src=x onerror=alert(1)>"

	// Test OrderConfirmed
	confirmedPayload := notification.OrderConfirmedPayload{
		OrderID:          orderID,
		CustomerEmail:    "xss-test@shopflow.io",
		TotalAmountMinor: 1000,
		Currency:         "USD",
		ConfirmedAt:      time.Now().UTC(),
		LineItems: []notification.LineItemPayload{
			{
				SKU:            "SKU-XSS",
				Title:          xssTitle,
				UnitPriceMinor: 1000,
				Quantity:       1,
				SubtotalMinor:  1000,
			},
		},
	}

	textBody, htmlBody, err := renderer.RenderOrderConfirmed(confirmedPayload)
	require.NoError(t, err)

	// HTML must escape <script> to &lt;script&gt;
	assert.NotContains(t, htmlBody, "<script>")
	assert.Contains(t, htmlBody, "&lt;script&gt;")
	assert.Contains(t, htmlBody, "&amp;")
	// Text body should remain human readable
	assert.Contains(t, textBody, xssTitle)

	// Test OrderCancelled
	cancelledPayload := notification.OrderCancelledPayload{
		OrderID:          orderID,
		CustomerEmail:    "xss-test@shopflow.io",
		TotalAmountMinor: 1000,
		Currency:         "USD",
		Reason:           xssReason,
		CancelledAt:      time.Now().UTC(),
	}

	textCanc, htmlCanc, err := renderer.RenderOrderCancelled(cancelledPayload)
	require.NoError(t, err)

	assert.NotContains(t, htmlCanc, "<img src=x")
	assert.Contains(t, htmlCanc, "&lt;img src=x")
	assert.Contains(t, textCanc, xssReason)
}

// 4. ADVERSARIAL TEST: Poison Pill Variations & Robustness
func TestAdversarial_PoisonPillVariants(t *testing.T) {
	defer goleak.VerifyNone(t)

	sender := email.NewMemorySender()
	renderer, err := notification.NewRenderer()
	require.NoError(t, err)

	svc := notification.NewService(sender, renderer, slog.Default())
	ctx := context.Background()

	poisonPills := []struct {
		name    string
		payload []byte
	}{
		{name: "truncated JSON", payload: []byte(`{"order_id": "123"`)},
		{name: "wrong type for order_id", payload: []byte(`{"order_id": 123456}`)},
		{name: "empty byte slice", payload: []byte{}},
		{name: "non-json plain text", payload: []byte("This is not JSON at all")},
		{name: "JSON array instead of object", payload: []byte(`[1, 2, 3]`)},
		{name: "non-UTF8 binary bytes", payload: []byte{0xFF, 0xFE, 0xFD, 0x00}},
	}

	for _, pp := range poisonPills {
		t.Run(pp.name, func(t *testing.T) {
			msg := kafka.Message{
				Topic:   "shopflow.orders",
				Headers: map[string]string{"event_type": "OrderConfirmed"},
				Value:   pp.payload,
			}
			err := svc.HandleMessage(ctx, msg)
			assert.ErrorIs(t, err, inbox.ErrPoisonPill, "Poison pill %s must return inbox.ErrPoisonPill", pp.name)
		})
	}

	// Invariant: zero emails sent on poison pills
	assert.Equal(t, 0, sender.Count())
}

// 5. ADVERSARIAL TEST: Sequential Duplicate Flood (At-Least-Once Delivery Replay)
func TestAdversarial_SequentialDuplicateFlood(t *testing.T) {
	defer goleak.VerifyNone(t)

	sender := email.NewMemorySender()
	renderer, err := notification.NewRenderer()
	require.NoError(t, err)

	svc := notification.NewService(sender, renderer, slog.Default())
	inboxRepo := inbox.NewMockRepository()

	processor := inbox.NewProcessor(
		inboxRepo,
		nil,
		svc.HandleMessage,
		inbox.ProcessorConfig{ConsumerGroup: "notification-service"},
		slog.Default(),
	)

	payload := notification.OrderConfirmedPayload{
		OrderID:          uuid.New(),
		CustomerEmail:    "flood@shopflow.io",
		TotalAmountMinor: 4990,
		Currency:         "USD",
		ConfirmedAt:      time.Now().UTC(),
	}
	bytesPayload, err := json.Marshal(payload)
	require.NoError(t, err)

	msg := kafka.Message{
		Topic:   "shopflow.orders",
		Headers: map[string]string{"message_id": "flood-msg-1", "event_type": "OrderConfirmed"},
		Value:   bytesPayload,
	}

	ctx := context.Background()

	// Deliver the identical message 50 times sequentially
	const deliveries = 50
	for i := 0; i < deliveries; i++ {
		err := processor.ProcessMessage(ctx, msg)
		assert.NoError(t, err)
	}

	// Invariant: Exactly 1 email sent despite 50 deliveries
	assert.Equal(t, 1, sender.Count(), "Exactly 1 email must be dispatched across 50 deliveries")
}

// 6. ADVERSARIAL TEST: High-Concurrency Redelivery Storm
func TestAdversarial_HighConcurrencyRedeliveryStorm(t *testing.T) {
	defer goleak.VerifyNone(t)

	sender := email.NewMemorySender()
	renderer, err := notification.NewRenderer()
	require.NoError(t, err)

	svc := notification.NewService(sender, renderer, slog.Default())
	inboxRepo := inbox.NewMockRepository()

	processor := inbox.NewProcessor(
		inboxRepo,
		nil,
		svc.HandleMessage,
		inbox.ProcessorConfig{ConsumerGroup: "notification-service"},
		slog.Default(),
	)

	payload := notification.OrderConfirmedPayload{
		OrderID:          uuid.New(),
		CustomerEmail:    "storm@shopflow.io",
		TotalAmountMinor: 9900,
		Currency:         "USD",
		ConfirmedAt:      time.Now().UTC(),
	}
	bytesPayload, err := json.Marshal(payload)
	require.NoError(t, err)

	msg := kafka.Message{
		Topic:   "shopflow.orders",
		Headers: map[string]string{"message_id": "storm-msg-unique", "event_type": "OrderConfirmed"},
		Value:   bytesPayload,
	}

	const workers = 30
	var wg sync.WaitGroup
	wg.Add(workers)

	ctx := context.Background()
	startBarrier := make(chan struct{})

	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			<-startBarrier // Simultaneous launch to maximize race condition probability
			_ = processor.ProcessMessage(ctx, msg)
		}()
	}

	close(startBarrier)
	wg.Wait()

	// Invariant: Exactly 1 email sent, zero race condition duplicates
	assert.Equal(t, 1, sender.Count(), "high concurrency storm must result in exactly 1 dispatched email")
}

// 7. ADVERSARIAL TEST: Consumer Rapid Start/Stop Lifecycle & Context Cancellation
func TestAdversarial_Consumer_RapidStartStop(t *testing.T) {
	defer goleak.VerifyNone(t)

	sender := email.NewMemorySender()
	renderer, err := notification.NewRenderer()
	require.NoError(t, err)

	svc := notification.NewService(sender, renderer, slog.Default())
	inboxRepo := inbox.NewMockRepository()
	broker := kafka.NewMockBroker()
	kafkaConsumer := kafka.NewMockConsumer(broker, []string{"shopflow.orders"}, "notification-service")

	consumer := notification.NewConsumer(inboxRepo, kafkaConsumer, svc, slog.Default())

	// Rapidly cycle start/stop 5 times
	for i := 0; i < 5; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		err := consumer.Start(ctx)
		require.NoError(t, err)
		assert.True(t, consumer.IsActive())

		// Stop immediately
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
		err = consumer.Stop(stopCtx)
		stopCancel()
		require.NoError(t, err)
		assert.False(t, consumer.IsActive())

		cancel()
	}
}

// 8. ADVERSARIAL TEST: Customer Email Fallback Determinism
func TestAdversarial_CustomerEmailFallback(t *testing.T) {
	defer goleak.VerifyNone(t)

	sender := email.NewMemorySender()
	renderer, err := notification.NewRenderer()
	require.NoError(t, err)

	svc := notification.NewService(sender, renderer, slog.Default())
	ctx := context.Background()

	orderID := uuid.MustParse("12345678-1234-1234-1234-123456789abc")

	// Case 1: Empty email, has CustomerID -> user-<customer_id>@shopflow.io
	p1 := notification.OrderConfirmedPayload{
		OrderID:          orderID,
		CustomerID:       "cust-999",
		CustomerEmail:    "",
		TotalAmountMinor: 1000,
		Currency:         "USD",
		ConfirmedAt:      time.Now().UTC(),
	}
	b1, _ := json.Marshal(p1)
	err = svc.HandleMessage(ctx, kafka.Message{
		Headers: map[string]string{"event_type": "OrderConfirmed"},
		Value:   b1,
	})
	require.NoError(t, err)
	assert.Equal(t, "user-cust-999@shopflow.io", sender.GetMessages()[0].To[0])

	sender.Reset()

	// Case 2: Empty email, empty CustomerID -> customer-<order_id[:8]>@shopflow.io
	p2 := notification.OrderConfirmedPayload{
		OrderID:          orderID,
		CustomerID:       "",
		CustomerEmail:    "",
		TotalAmountMinor: 1000,
		Currency:         "USD",
		ConfirmedAt:      time.Now().UTC(),
	}
	b2, _ := json.Marshal(p2)
	err = svc.HandleMessage(ctx, kafka.Message{
		Headers: map[string]string{"event_type": "OrderConfirmed"},
		Value:   b2,
	})
	require.NoError(t, err)
	assert.Equal(t, "customer-12345678@shopflow.io", sender.GetMessages()[0].To[0])
}
