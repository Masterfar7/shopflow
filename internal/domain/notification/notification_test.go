package notification_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
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

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// 1. Financial Arithmetic & Template Formatter Tests (Architecture Guidelines §4: Zero Floats)
func TestFormatMinorMoney(t *testing.T) {
	defer goleak.VerifyNone(t)

	tests := []struct {
		amount   int64
		currency string
		expected string
	}{
		{amount: 0, currency: "USD", expected: "$0.00"},
		{amount: 5, currency: "USD", expected: "$0.05"},
		{amount: 50, currency: "USD", expected: "$0.50"},
		{amount: 1999, currency: "USD", expected: "$19.99"},
		{amount: 100000, currency: "USD", expected: "$1000.00"},
		{amount: -1999, currency: "USD", expected: "-$19.99"},
		{amount: -5, currency: "USD", expected: "-$0.05"},
		{amount: 1999, currency: "EUR", expected: "19.99 EUR"},
		{amount: 5000, currency: "GBP", expected: "50.00 GBP"},
		{amount: 100, currency: "JPY", expected: "1.00 JPY"},
		{amount: 123456789, currency: "USD", expected: "$1234567.89"},
		{amount: math.MinInt64, currency: "USD", expected: "$92233720368547758.07"},
	}

	for _, tc := range tests {
		t.Run(fmt.Sprintf("%d_%s", tc.amount, tc.currency), func(t *testing.T) {
			res := notification.FormatMinorMoney(tc.amount, tc.currency)
			assert.Equal(t, tc.expected, res)
		})
	}
}

// 2. Template Rendering Tests
func TestRenderer_OrderConfirmed(t *testing.T) {
	defer goleak.VerifyNone(t)

	renderer, err := notification.NewRenderer()
	require.NoError(t, err)

	orderID := uuid.New()
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)

	payload := notification.OrderConfirmedPayload{
		OrderID:          orderID,
		CustomerID:       "user-123",
		CustomerEmail:    "buyer@example.com",
		TotalAmountMinor: 4998,
		Currency:         "USD",
		ConfirmedAt:      now,
		LineItems: []notification.LineItemPayload{
			{
				SKU:            "SKU-SHIRT-BLK",
				Title:          "Cotton Black T-Shirt",
				UnitPriceMinor: 2499,
				Quantity:       2,
				SubtotalMinor:  4998,
			},
		},
	}

	textBody, htmlBody, err := renderer.RenderOrderConfirmed(payload)
	require.NoError(t, err)

	// Check text body
	assert.Contains(t, textBody, "Thank you for your order!")
	assert.Contains(t, textBody, orderID.String())
	assert.Contains(t, textBody, "Cotton Black T-Shirt")
	assert.Contains(t, textBody, "SKU-SHIRT-BLK")
	assert.Contains(t, textBody, "$24.99")
	assert.Contains(t, textBody, "$49.98")

	// Check HTML body
	assert.Contains(t, htmlBody, "<!DOCTYPE html>")
	assert.Contains(t, htmlBody, "Order Confirmed!")
	assert.Contains(t, htmlBody, orderID.String())
	assert.Contains(t, htmlBody, "Cotton Black T-Shirt")
	assert.Contains(t, htmlBody, "$49.98")
}

func TestRenderer_OrderCancelled(t *testing.T) {
	defer goleak.VerifyNone(t)

	renderer, err := notification.NewRenderer()
	require.NoError(t, err)

	orderID := uuid.New()
	now := time.Date(2026, 9, 11, 12, 30, 0, 0, time.UTC)

	payload := notification.OrderCancelledPayload{
		OrderID:          orderID,
		CustomerID:       "user-456",
		CustomerEmail:    "cancelled@example.com",
		TotalAmountMinor: 1599,
		Currency:         "USD",
		Reason:           "Customer changed mind",
		CancelledAt:      now,
		LineItems: []notification.LineItemPayload{
			{
				SKU:            "SKU-MUG",
				Title:          "Coffee Ceramic Mug",
				UnitPriceMinor: 1599,
				Quantity:       1,
				SubtotalMinor:  1599,
			},
		},
	}

	textBody, htmlBody, err := renderer.RenderOrderCancelled(payload)
	require.NoError(t, err)

	assert.Contains(t, textBody, "Your order has been cancelled.")
	assert.Contains(t, textBody, orderID.String())
	assert.Contains(t, textBody, "Customer changed mind")
	assert.Contains(t, textBody, "$15.99")

	assert.Contains(t, htmlBody, "Order Cancelled")
	assert.Contains(t, htmlBody, orderID.String())
	assert.Contains(t, htmlBody, "Customer changed mind")
	assert.Contains(t, htmlBody, "$15.99")
}

// 3. Notification Service Dispatch Tests
func TestService_HandleOrderConfirmed(t *testing.T) {
	defer goleak.VerifyNone(t)

	sender := email.NewMemorySender()
	renderer, err := notification.NewRenderer()
	require.NoError(t, err)

	svc := notification.NewService(sender, renderer, slog.Default())
	ctx := context.Background()

	orderID := uuid.New()
	payload := notification.OrderConfirmedPayload{
		OrderID:          orderID,
		CustomerEmail:    "test-user@shopflow.io",
		TotalAmountMinor: 2500,
		Currency:         "USD",
		ConfirmedAt:      time.Now().UTC(),
		LineItems: []notification.LineItemPayload{
			{
				SKU:            "SKU-1",
				Title:          "Item 1",
				UnitPriceMinor: 2500,
				Quantity:       1,
				SubtotalMinor:  2500,
			},
		},
	}
	bytesPayload, err := json.Marshal(payload)
	require.NoError(t, err)

	msg := kafka.Message{
		Topic: "shopflow.orders",
		Headers: map[string]string{
			"event_type": "OrderConfirmed",
			"message_id": "msg-conf-1",
		},
		Value: bytesPayload,
	}

	err = svc.HandleMessage(ctx, msg)
	require.NoError(t, err)

	require.Equal(t, 1, sender.Count())
	sent := sender.GetMessages()[0]
	assert.Equal(t, "test-user@shopflow.io", sent.To[0])
	assert.Contains(t, sent.Subject, orderID.String())
	assert.Contains(t, sent.TextBody, "Item 1")
	assert.Contains(t, sent.HTMLBody, "$25.00")
}

func TestService_HandleOrderCancelled(t *testing.T) {
	defer goleak.VerifyNone(t)

	sender := email.NewMemorySender()
	renderer, err := notification.NewRenderer()
	require.NoError(t, err)

	svc := notification.NewService(sender, renderer, slog.Default())
	ctx := context.Background()

	orderID := uuid.New()
	payload := notification.OrderCancelledPayload{
		OrderID:          orderID,
		CustomerEmail:    "cancel-user@shopflow.io",
		TotalAmountMinor: 1000,
		Currency:         "USD",
		Reason:           "Out of stock",
		CancelledAt:      time.Now().UTC(),
	}
	bytesPayload, err := json.Marshal(payload)
	require.NoError(t, err)

	msg := kafka.Message{
		Topic: "shopflow.orders",
		Headers: map[string]string{
			"event_type": "OrderCancelled",
			"message_id": "msg-canc-1",
		},
		Value: bytesPayload,
	}

	err = svc.HandleMessage(ctx, msg)
	require.NoError(t, err)

	require.Equal(t, 1, sender.Count())
	sent := sender.GetMessages()[0]
	assert.Equal(t, "cancel-user@shopflow.io", sent.To[0])
	assert.Contains(t, sent.Subject, "Order Cancelled")
	assert.Contains(t, sent.TextBody, "Out of stock")
}

func TestService_Compatibility_OrderStatusChanged(t *testing.T) {
	defer goleak.VerifyNone(t)

	sender := email.NewMemorySender()
	renderer, err := notification.NewRenderer()
	require.NoError(t, err)

	svc := notification.NewService(sender, renderer, slog.Default())
	ctx := context.Background()

	orderID := uuid.New()
	payload := notification.OrderConfirmedPayload{
		OrderID:          orderID,
		CustomerID:       "user-compat-1",
		CustomerEmail:    "", // missing email tests fallback
		TotalAmountMinor: 3000,
		Currency:         "USD",
		ConfirmedAt:      time.Now().UTC(),
	}
	bytesPayload, err := json.Marshal(payload)
	require.NoError(t, err)

	msg := kafka.Message{
		Topic: "shopflow.orders",
		Headers: map[string]string{
			"event_type": "OrderStatusChanged_PAID_to_CONFIRMED",
		},
		Value: bytesPayload,
	}

	err = svc.HandleMessage(ctx, msg)
	require.NoError(t, err)

	// OrderStatusChanged_PAID_to_CONFIRMED must be ignored (returns nil, sends 0 emails)
	// to avoid duplicate customer emails on order confirmation.
	require.Equal(t, 0, sender.Count())
}

func TestService_PayloadPeek(t *testing.T) {
	defer goleak.VerifyNone(t)

	sender := email.NewMemorySender()
	renderer, err := notification.NewRenderer()
	require.NoError(t, err)

	svc := notification.NewService(sender, renderer, slog.Default())
	ctx := context.Background()

	orderID := uuid.New()
	payload := map[string]any{
		"event_type":         "OrderConfirmed",
		"order_id":           orderID,
		"customer_email":     "peek@example.com",
		"total_amount_minor": 1000,
		"currency":           "USD",
		"confirmed_at":       time.Now().UTC(),
	}
	bytesPayload, err := json.Marshal(payload)
	require.NoError(t, err)

	// Message with empty headers, relies on JSON payload peek
	msg := kafka.Message{
		Topic:   "shopflow.orders",
		Headers: map[string]string{},
		Value:   bytesPayload,
	}

	err = svc.HandleMessage(ctx, msg)
	require.NoError(t, err)

	require.Equal(t, 1, sender.Count())
	assert.Equal(t, "peek@example.com", sender.GetMessages()[0].To[0])
}

func TestService_PoisonPill(t *testing.T) {
	defer goleak.VerifyNone(t)

	sender := email.NewMemorySender()
	renderer, err := notification.NewRenderer()
	require.NoError(t, err)

	svc := notification.NewService(sender, renderer, slog.Default())
	ctx := context.Background()

	msg := kafka.Message{
		Topic: "shopflow.orders",
		Headers: map[string]string{
			"event_type": "OrderConfirmed",
		},
		Value: []byte("{invalid-json"),
	}

	err = svc.HandleMessage(ctx, msg)
	assert.ErrorIs(t, err, inbox.ErrPoisonPill)
	assert.Equal(t, 0, sender.Count())
}

func TestService_IgnoredEvents(t *testing.T) {
	defer goleak.VerifyNone(t)

	sender := email.NewMemorySender()
	renderer, err := notification.NewRenderer()
	require.NoError(t, err)

	svc := notification.NewService(sender, renderer, slog.Default())
	ctx := context.Background()

	msg := kafka.Message{
		Topic: "shopflow.orders",
		Headers: map[string]string{
			"event_type": "InventoryReserved",
		},
		Value: []byte(`{"reservation_id":"123"}`),
	}

	err = svc.HandleMessage(ctx, msg)
	require.NoError(t, err)
	assert.Equal(t, 0, sender.Count())
}

func TestService_SenderFailure(t *testing.T) {
	defer goleak.VerifyNone(t)

	sender := email.NewMemorySender()
	renderer, err := notification.NewRenderer()
	require.NoError(t, err)

	simulatedErr := errors.New("smtp connection refused")
	sender.SetFailNext(simulatedErr)

	svc := notification.NewService(sender, renderer, slog.Default())
	ctx := context.Background()

	orderID := uuid.New()
	payload := notification.OrderConfirmedPayload{
		OrderID:          orderID,
		CustomerEmail:    "fail@shopflow.io",
		TotalAmountMinor: 1000,
		Currency:         "USD",
		ConfirmedAt:      time.Now().UTC(),
	}
	bytesPayload, err := json.Marshal(payload)
	require.NoError(t, err)

	msg := kafka.Message{
		Headers: map[string]string{"event_type": "OrderConfirmed"},
		Value:   bytesPayload,
	}

	err = svc.HandleMessage(ctx, msg)
	assert.ErrorIs(t, err, simulatedErr)
	assert.Equal(t, 0, sender.Count())
}

// 4. Idempotency & Inbox Deduplication Integration Test
func TestConsumer_IdempotentDeduplication(t *testing.T) {
	defer goleak.VerifyNone(t)

	sender := email.NewMemorySender()
	renderer, err := notification.NewRenderer()
	require.NoError(t, err)

	svc := notification.NewService(sender, renderer, slog.Default())
	inboxRepo := inbox.NewMockRepository()
	broker := kafka.NewMockBroker()
	kafkaConsumer := kafka.NewMockConsumer(broker, []string{"shopflow.orders"}, "notification-service")

	consumer := notification.NewConsumer(inboxRepo, kafkaConsumer, svc, slog.Default())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = consumer.Start(ctx)
	require.NoError(t, err)
	assert.True(t, consumer.IsActive())

	orderID := uuid.New()
	payload := notification.OrderConfirmedPayload{
		OrderID:          orderID,
		CustomerEmail:    "idempotent@shopflow.io",
		TotalAmountMinor: 5000,
		Currency:         "USD",
		ConfirmedAt:      time.Now().UTC(),
	}
	bytesPayload, err := json.Marshal(payload)
	require.NoError(t, err)

	msg := kafka.Message{
		Topic: "shopflow.orders",
		Headers: map[string]string{
			"message_id": "msg-unique-id-100",
			"event_type": "OrderConfirmed",
		},
		Value: bytesPayload,
	}

	// 1. Publish the message the first time
	err = broker.Publish(ctx, msg)
	require.NoError(t, err)

	// Wait for email to be processed and dispatched
	require.Eventually(t, func() bool {
		return sender.Count() == 1
	}, 2*time.Second, 20*time.Millisecond)

	// 2. Publish the exact same message AGAIN (simulating network redelivery or rebalance)
	err = broker.Publish(ctx, msg)
	require.NoError(t, err)

	// Sleep briefly to give consumer opportunity to process duplicate
	time.Sleep(100 * time.Millisecond)

	// Invariant: Count MUST STILL BE 1. Zero duplicate emails dispatched!
	assert.Equal(t, 1, sender.Count(), "idempotent deduplication must prevent duplicate emails")

	// Verify inbox message record transitioned to COMPLETED
	rec, err := inboxRepo.GetMessage(ctx, "msg-unique-id-100", "notification-service")
	require.NoError(t, err)
	assert.Equal(t, inbox.StatusCompleted, rec.Status)

	// Stop consumer cleanly
	err = consumer.Stop(ctx)
	require.NoError(t, err)
	assert.False(t, consumer.IsActive())
}

// 5. Concurrency Invariant: Concurrent Redeliveries
func TestConsumer_ConcurrentRedelivery(t *testing.T) {
	defer goleak.VerifyNone(t)

	sender := email.NewMemorySender()
	renderer, err := notification.NewRenderer()
	require.NoError(t, err)

	svc := notification.NewService(sender, renderer, slog.Default())
	inboxRepo := inbox.NewMockRepository()

	// Direct processor testing under concurrent attempts
	processor := inbox.NewProcessor(
		inboxRepo,
		nil,
		svc.HandleMessage,
		inbox.ProcessorConfig{ConsumerGroup: "notification-service"},
		slog.Default(),
	)

	orderID := uuid.New()
	payload := notification.OrderConfirmedPayload{
		OrderID:          orderID,
		CustomerEmail:    "concurrent@shopflow.io",
		TotalAmountMinor: 9900,
		Currency:         "USD",
		ConfirmedAt:      time.Now().UTC(),
	}
	bytesPayload, err := json.Marshal(payload)
	require.NoError(t, err)

	msg := kafka.Message{
		Topic: "shopflow.orders",
		Headers: map[string]string{
			"message_id": "msg-concurrent-test",
			"event_type": "OrderConfirmed",
		},
		Value: bytesPayload,
	}

	const concurrentGoroutines = 10
	var wg sync.WaitGroup
	wg.Add(concurrentGoroutines)

	ctx := context.Background()
	for i := 0; i < concurrentGoroutines; i++ {
		go func() {
			defer wg.Done()
			_ = processor.ProcessMessage(ctx, msg)
		}()
	}

	wg.Wait()

	// Invariant: exactly 1 email sent despite 10 concurrent message deliveries
	assert.Equal(t, 1, sender.Count(), "exactly 1 email must be sent despite concurrent redeliveries")
}
