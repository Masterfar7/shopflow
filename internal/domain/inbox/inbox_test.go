package inbox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"shopflow/internal/platform/kafka"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

func TestInbox_Deduplication_100ConcurrentDuplicateDeliveries(t *testing.T) {
	repo := NewMockRepository()
	ctx := context.Background()

	var handlerCalls atomic.Int64
	handler := func(ctx context.Context, msg kafka.Message) error {
		handlerCalls.Add(1)
		// Simulate small work
		time.Sleep(5 * time.Millisecond)
		return nil
	}

	cfg := ProcessorConfig{
		ConsumerGroup: "order-service-group",
	}
	processor := NewProcessor(repo, nil, handler, cfg, slog.Default())

	targetMessageID := "msg-unique-12345"
	targetPayload := []byte(`{"order_id":"` + uuid.New().String() + `","status":"PAID"}`)

	// Dispatch 100 concurrent duplicate deliveries
	numDeliveries := 100
	var wg sync.WaitGroup
	errs := make([]error, numDeliveries)

	for i := 0; i < numDeliveries; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			msg := kafka.Message{
				Topic: "shopflow.payments",
				Key:   []byte("key-1"),
				Value: targetPayload,
				Headers: map[string]string{
					"message_id": targetMessageID,
					"event_type": "PaymentCompleted",
				},
			}
			errs[idx] = processor.ProcessMessage(ctx, msg)
		}(i)
	}

	wg.Wait()

	// Verify that all 100 calls returned nil (no crash or unhandled error)
	for i, err := range errs {
		assert.NoError(t, err, "Delivery %d should complete without error", i)
	}

	// CRITICAL INVARIANT: The handler MUST be executed EXACTLY ONCE!
	assert.Equal(t, int64(1), handlerCalls.Load(), "Handler must be invoked exactly once for 100 duplicate deliveries")

	// Verify inbox status is COMPLETED
	stored, err := repo.GetMessage(ctx, targetMessageID, "order-service-group")
	require.NoError(t, err)
	assert.Equal(t, StatusCompleted, stored.Status)
}

func TestInbox_TerminalStateProtection(t *testing.T) {
	repo := NewMockRepository()
	ctx := context.Background()

	// Domain handler recognizes aggregate is already CANCELLED and returns ErrTerminalStateIgnored
	var handlerCalled bool
	handler := func(ctx context.Context, msg kafka.Message) error {
		handlerCalled = true
		return ErrTerminalStateIgnored
	}

	processor := NewProcessor(repo, nil, handler, ProcessorConfig{ConsumerGroup: "test-group"}, slog.Default())

	msg := kafka.Message{
		Topic: "shopflow.orders",
		Key:   []byte("order-cancelled"),
		Value: []byte(`{"order_id":"123","action":"late_payment"}`),
		Headers: map[string]string{
			"message_id": "msg-late-999",
			"event_type": "PaymentReceived",
		},
	}

	err := processor.ProcessMessage(ctx, msg)
	assert.NoError(t, err, "Terminal state ignored message must succeed as idempotent no-op")
	assert.True(t, handlerCalled)

	// Status in inbox must be marked COMPLETED so it won't be reprocessed
	stored, err := repo.GetMessage(ctx, "msg-late-999", "test-group")
	require.NoError(t, err)
	assert.Equal(t, StatusCompleted, stored.Status)

	// Zero dead letters recorded
	dlqs, err := repo.GetDeadLetterMessages(ctx, 10)
	require.NoError(t, err)
	assert.Empty(t, dlqs, "Terminal state ignore should not trigger DLQ")
}

func TestInbox_PoisonPill_MalformedJSON(t *testing.T) {
	repo := NewMockRepository()
	ctx := context.Background()

	handler := func(ctx context.Context, msg kafka.Message) error {
		t.Fatal("Handler should not be invoked for malformed JSON")
		return nil
	}

	processor := NewProcessor(repo, nil, handler, ProcessorConfig{ConsumerGroup: "test-group"}, slog.Default())

	msg := kafka.Message{
		Topic: "shopflow.orders",
		Key:   []byte("ord-malformed"),
		Value: []byte(`{"bad_json": not valid json...`),
		Headers: map[string]string{
			"message_id": "msg-malformed-01",
		},
	}

	err := processor.ProcessMessage(ctx, msg)
	assert.NoError(t, err, "Malformed poison pill should be routed to DLQ without crashing")

	// Verify DLQ entry
	dlqs, err := repo.GetDeadLetterMessages(ctx, 10)
	require.NoError(t, err)
	require.Len(t, dlqs, 1)
	assert.Equal(t, "INBOX", dlqs[0].SourceType)
	assert.Equal(t, "msg-malformed-01", dlqs[0].SourceID)
	assert.Equal(t, "shopflow.orders", dlqs[0].Topic)
	assert.Contains(t, dlqs[0].ErrorReason, "malformed JSON")
}

func TestInbox_PoisonPill_DomainHandlerRejection(t *testing.T) {
	repo := NewMockRepository()
	ctx := context.Background()

	// Handler returns ErrPoisonPill on corrupted business payload
	handler := func(ctx context.Context, msg kafka.Message) error {
		return fmt.Errorf("%w: unrecognized schema version 99", ErrPoisonPill)
	}

	processor := NewProcessor(repo, nil, handler, ProcessorConfig{ConsumerGroup: "test-group"}, slog.Default())

	msg := kafka.Message{
		Topic: "shopflow.orders",
		Key:   []byte("ord-bad-schema"),
		Value: []byte(`{"schema_v":99}`),
		Headers: map[string]string{
			"message_id": "msg-bad-schema-02",
		},
	}

	err := processor.ProcessMessage(ctx, msg)
	assert.NoError(t, err, "Poison pill handled cleanly")

	dlqs, err := repo.GetDeadLetterMessages(ctx, 10)
	require.NoError(t, err)
	require.Len(t, dlqs, 1)
	assert.Equal(t, "INBOX", dlqs[0].SourceType)
	assert.Equal(t, "msg-bad-schema-02", dlqs[0].SourceID)
	assert.Contains(t, dlqs[0].ErrorReason, "unrecognized schema version 99")

	// Inbox status marked COMPLETED so it won't be retried indefinitely
	stored, err := repo.GetMessage(ctx, "msg-bad-schema-02", "test-group")
	require.NoError(t, err)
	assert.Equal(t, StatusCompleted, stored.Status)
}

func TestInbox_TransientError_Retriable(t *testing.T) {
	repo := NewMockRepository()
	ctx := context.Background()

	transientErr := errors.New("database connection timeout")
	handler := func(ctx context.Context, msg kafka.Message) error {
		return transientErr
	}

	processor := NewProcessor(repo, nil, handler, ProcessorConfig{ConsumerGroup: "test-group"}, slog.Default())

	msg := kafka.Message{
		Topic: "shopflow.orders",
		Key:   []byte("ord-transient"),
		Value: []byte(`{"valid":true}`),
		Headers: map[string]string{
			"message_id": "msg-transient-03",
		},
	}

	err := processor.ProcessMessage(ctx, msg)
	assert.ErrorIs(t, err, transientErr)

	// Status should be FAILED
	stored, err := repo.GetMessage(ctx, "msg-transient-03", "test-group")
	require.NoError(t, err)
	assert.Equal(t, StatusFailed, stored.Status)

	// DLQ should NOT be written for transient errors
	dlqs, err := repo.GetDeadLetterMessages(ctx, 10)
	require.NoError(t, err)
	assert.Empty(t, dlqs)
}

func TestInbox_IndependentConsumerGroups(t *testing.T) {
	repo := NewMockRepository()
	ctx := context.Background()

	var g1Count, g2Count atomic.Int64
	p1 := NewProcessor(repo, nil, func(ctx context.Context, msg kafka.Message) error {
		g1Count.Add(1)
		return nil
	}, ProcessorConfig{ConsumerGroup: "analytics-service"}, slog.Default())

	p2 := NewProcessor(repo, nil, func(ctx context.Context, msg kafka.Message) error {
		g2Count.Add(1)
		return nil
	}, ProcessorConfig{ConsumerGroup: "notification-service"}, slog.Default())

	msg := kafka.Message{
		Topic: "shopflow.orders",
		Value: []byte(`{"order_id":"101"}`),
		Headers: map[string]string{
			"message_id": "msg-shared-101",
		},
	}

	require.NoError(t, p1.ProcessMessage(ctx, msg))
	require.NoError(t, p2.ProcessMessage(ctx, msg))

	assert.Equal(t, int64(1), g1Count.Load())
	assert.Equal(t, int64(1), g2Count.Load())

	// Replay for p1 -> skipped
	require.NoError(t, p1.ProcessMessage(ctx, msg))
	assert.Equal(t, int64(1), g1Count.Load())
}

func TestInbox_Lifecycle_WithMockConsumer_ZeroLeaks(t *testing.T) {
	defer goleak.VerifyNone(t)

	repo := NewMockRepository()
	broker := kafka.NewMockBroker()
	consumer := kafka.NewMockConsumer(broker, []string{"shopflow.orders"}, "test-group")

	var processedCount atomic.Int64
	handler := func(ctx context.Context, msg kafka.Message) error {
		processedCount.Add(1)
		return nil
	}

	processor := NewProcessor(repo, consumer, handler, ProcessorConfig{ConsumerGroup: "test-group"}, slog.Default())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, processor.Start(ctx))
	assert.True(t, processor.IsActive())

	// Publish via MockPublisher to broker
	pub := kafka.NewMockPublisher(broker)
	defer pub.Close()

	require.NoError(t, pub.Publish(ctx, kafka.Message{
		Topic: "shopflow.orders",
		Value: []byte(`{"id":"order-live"}`),
		Headers: map[string]string{
			"message_id": "live-msg-1",
		},
	}))

	require.Eventually(t, func() bool {
		return processedCount.Load() == 1
	}, 1*time.Second, 10*time.Millisecond)

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer stopCancel()

	require.NoError(t, processor.Stop(stopCtx))
	assert.False(t, processor.IsActive())
}
