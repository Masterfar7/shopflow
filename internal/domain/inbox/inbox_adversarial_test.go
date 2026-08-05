package inbox

import (
	"context"
	"errors"
	"fmt"
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

// TestAdversarial_Inbox_RetryAfterTransientFailure checks behavior when a transient error is retried.
func TestAdversarial_Inbox_RetryAfterTransientFailure(t *testing.T) {
	defer goleak.VerifyNone(t)

	repo := NewMockRepository()
	ctx := context.Background()

	var attempts atomic.Int64
	transientErr := errors.New("temporary connection error")

	// Handler fails on attempt 1, should succeed on attempt 2
	handler := func(ctx context.Context, msg kafka.Message) error {
		attempt := attempts.Add(1)
		if attempt == 1 {
			return transientErr
		}
		return nil
	}

	processor := NewProcessor(repo, nil, handler, ProcessorConfig{ConsumerGroup: "test-consumer-group"}, nil)

	msg := kafka.Message{
		Topic: "shopflow.orders",
		Key:   []byte("ord-retry"),
		Value: []byte(`{"order_id":"123"}`),
		Headers: map[string]string{
			"message_id": "msg-retry-test-1",
		},
	}

	// Attempt 1: Fails with transientErr
	err1 := processor.ProcessMessage(ctx, msg)
	require.ErrorIs(t, err1, transientErr)
	assert.Equal(t, int64(1), attempts.Load())

	stored, err := repo.GetMessage(ctx, "msg-retry-test-1", "test-consumer-group")
	require.NoError(t, err)
	assert.Equal(t, StatusFailed, stored.Status)

	// Attempt 2: Redelivery after transient failure
	err2 := processor.ProcessMessage(ctx, msg)
	require.NoError(t, err2, "Attempt 2 redelivery should succeed")
	assert.Equal(t, int64(2), attempts.Load(), "Handler attempts should increment to 2 on redelivery")

	stored2, err := repo.GetMessage(ctx, "msg-retry-test-1", "test-consumer-group")
	require.NoError(t, err)
	assert.Equal(t, StatusCompleted, stored2.Status, "Message should transition to StatusCompleted")

	// Attempt 3: Further redelivery should be skipped idempotently
	err3 := processor.ProcessMessage(ctx, msg)
	require.NoError(t, err3, "Attempt 3 duplicate after completion should be skipped idempotently")
	assert.Equal(t, int64(2), attempts.Load(), "Handler attempts should remain 2 after completion")
}

// TestAdversarial_Inbox_MultiMessageConcurrentContention tests 500 concurrent goroutines
// sending duplicates across 5 distinct message IDs (100 duplicates each).
// Invariant: Exactly 5 handler calls (1 per unique message ID), exactly 495 deduplications, 0 leaks.
func TestAdversarial_Inbox_MultiMessageConcurrentContention(t *testing.T) {
	defer goleak.VerifyNone(t)

	repo := NewMockRepository()
	ctx := context.Background()

	var executedMap sync.Map
	var totalExecutions atomic.Int64

	handler := func(ctx context.Context, msg kafka.Message) error {
		msgID := msg.Headers["message_id"]
		if _, loaded := executedMap.LoadOrStore(msgID, true); loaded {
			t.Errorf("Violation: Message %s was executed more than once!", msgID)
		}
		totalExecutions.Add(1)
		time.Sleep(2 * time.Millisecond)
		return nil
	}

	processor := NewProcessor(repo, nil, handler, ProcessorConfig{ConsumerGroup: "stress-group"}, nil)

	numUniqueMessages := 5
	duplicatesPerMessage := 100
	totalRequests := numUniqueMessages * duplicatesPerMessage

	var wg sync.WaitGroup
	wg.Add(totalRequests)

	for m := 0; m < numUniqueMessages; m++ {
		msgID := fmt.Sprintf("stress-msg-%d", m)
		payload := []byte(fmt.Sprintf(`{"msg_idx":%d}`, m))

		for d := 0; d < duplicatesPerMessage; d++ {
			go func(id string, val []byte) {
				defer wg.Done()
				msg := kafka.Message{
					Topic: "shopflow.orders",
					Key:   []byte(id),
					Value: val,
					Headers: map[string]string{
						"message_id": id,
					},
				}
				_ = processor.ProcessMessage(ctx, msg)
			}(msgID, payload)
		}
	}

	wg.Wait()

	assert.Equal(t, int64(numUniqueMessages), totalExecutions.Load(),
		"Exactly %d unique messages must be processed by handler across %d deliveries",
		numUniqueMessages, totalRequests)

	for m := 0; m < numUniqueMessages; m++ {
		msgID := fmt.Sprintf("stress-msg-%d", m)
		stored, err := repo.GetMessage(ctx, msgID, "stress-group")
		require.NoError(t, err)
		assert.Equal(t, StatusCompleted, stored.Status)
	}
}

// TestAdversarial_Inbox_CorruptedAndEdgeInputs tests handling of nil, missing, or extreme inputs.
func TestAdversarial_Inbox_CorruptedAndEdgeInputs(t *testing.T) {
	defer goleak.VerifyNone(t)

	repo := NewMockRepository()
	ctx := context.Background()

	handlerCalled := false
	handler := func(ctx context.Context, msg kafka.Message) error {
		handlerCalled = true
		return nil
	}

	processor := NewProcessor(repo, nil, handler, ProcessorConfig{ConsumerGroup: "edge-group"}, nil)

	// Case 1: Empty message with no headers and no key
	emptyMsg := kafka.Message{
		Topic: "shopflow.orders",
	}
	err := processor.ProcessMessage(ctx, emptyMsg)
	assert.NoError(t, err, "Should route to DLQ without crashing")
	assert.False(t, handlerCalled)

	// Case 2: Message with key but no message_id header (should use key as fallback)
	handlerCalled = false
	keyMsg := kafka.Message{
		Topic: "shopflow.orders",
		Key:   []byte("fallback-key-id"),
		Value: []byte(`{"data":"ok"}`),
	}
	err = processor.ProcessMessage(ctx, keyMsg)
	assert.NoError(t, err)
	assert.True(t, handlerCalled)

	stored, err := repo.GetMessage(ctx, "fallback-key-id", "edge-group")
	require.NoError(t, err)
	assert.Equal(t, StatusCompleted, stored.Status)

	// Case 3: Message with truncated/invalid JSON payload
	handlerCalled = false
	badJSONMsg := kafka.Message{
		Topic: "shopflow.orders",
		Key:   []byte("bad-json-key"),
		Value: []byte(`{"open": [1, 2, `),
		Headers: map[string]string{
			"message_id": "msg-bad-json",
		},
	}
	err = processor.ProcessMessage(ctx, badJSONMsg)
	assert.NoError(t, err, "Malformed JSON should be routed to DLQ")
	assert.False(t, handlerCalled)

	// Verify DLQs
	dlqs, err := repo.GetDeadLetterMessages(ctx, 10)
	require.NoError(t, err)
	assert.Len(t, dlqs, 2) // emptyMsg and badJSONMsg
}

// TestAdversarial_Inbox_ContextCancellationMidFlight ensures canceled contexts terminate cleanly
func TestAdversarial_Inbox_ContextCancellationMidFlight(t *testing.T) {
	defer goleak.VerifyNone(t)

	repo := NewMockRepository()
	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel context

	handler := func(ctx context.Context, msg kafka.Message) error {
		return ctx.Err()
	}

	processor := NewProcessor(repo, nil, handler, ProcessorConfig{ConsumerGroup: "cancel-group"}, nil)

	msg := kafka.Message{
		Topic: "shopflow.orders",
		Key:   []byte("ord-canceled"),
		Value: []byte(`{}`),
		Headers: map[string]string{
			"message_id": uuid.New().String(),
		},
	}

	err := processor.ProcessMessage(cancelCtx, msg)
	assert.ErrorIs(t, err, context.Canceled)
}
