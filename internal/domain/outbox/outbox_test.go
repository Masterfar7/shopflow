package outbox

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

func TestOutbox_NewOutboxMessage(t *testing.T) {
	msg, err := NewOutboxMessage("order", "ord-123", "OrderCreated", map[string]any{"total": 5000}, map[string]string{"foo": "bar"})
	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, msg.ID)
	assert.Equal(t, "order", msg.AggregateType)
	assert.Equal(t, "ord-123", msg.AggregateID)
	assert.Equal(t, "OrderCreated", msg.EventType)
	assert.Equal(t, StatusPending, msg.Status)
	assert.Equal(t, 0, msg.RetryCount)
	assert.JSONEq(t, `{"total":5000}`, string(msg.Payload))
	assert.Equal(t, "bar", msg.Headers["foo"])
}

func TestOutbox_BackoffPolicy(t *testing.T) {
	policy := BackoffPolicy{
		InitialInterval: 100 * time.Millisecond,
		MaxInterval:     2 * time.Second,
		MaxRetries:      4,
		EnableJitter:    false,
	}

	assert.Equal(t, 100*time.Millisecond, policy.Calculate(0))
	assert.Equal(t, 200*time.Millisecond, policy.Calculate(1))
	assert.Equal(t, 400*time.Millisecond, policy.Calculate(2))
	assert.Equal(t, 800*time.Millisecond, policy.Calculate(3))
	assert.Equal(t, 1600*time.Millisecond, policy.Calculate(4))
	assert.Equal(t, 2000*time.Millisecond, policy.Calculate(5)) // capped at MaxInterval

	assert.False(t, policy.IsMaxRetriesExceeded(3))
	assert.True(t, policy.IsMaxRetriesExceeded(4))
	assert.True(t, policy.IsMaxRetriesExceeded(5))
}

func TestOutbox_ConcurrentLeasing_NoContentionOrDuplication(t *testing.T) {
	repo := NewMockRepository()
	ctx := context.Background()

	// Seed 100 messages into outbox
	totalMessages := 100
	for i := 0; i < totalMessages; i++ {
		msg, err := NewOutboxMessage("order", fmt.Sprintf("order-%d", i), "OrderCreated", []byte(`{}`), nil)
		require.NoError(t, err)
		// Space created_at slightly to ensure deterministic ordering
		msg.CreatedAt = time.Now().UTC().Add(time.Duration(i) * time.Millisecond)
		require.NoError(t, repo.SaveMessage(ctx, nil, msg))
	}

	// Launch 10 concurrent workers trying to lease messages
	numWorkers := 10
	batchSize := 10
	leaseDuration := 10 * time.Second

	var wg sync.WaitGroup
	var mu sync.Mutex
	leasedIDs := make(map[uuid.UUID]string)

	for w := 0; w < numWorkers; w++ {
		workerID := fmt.Sprintf("worker-%d", w)
		wg.Add(1)
		go func(wid string) {
			defer wg.Done()
			leased, err := repo.LeaseMessages(ctx, wid, batchSize, leaseDuration)
			if err != nil {
				t.Errorf("worker %s failed to lease: %v", wid, err)
				return
			}
			mu.Lock()
			for _, m := range leased {
				if prevWorker, exists := leasedIDs[m.ID]; exists {
					t.Errorf("Duplicate lease detected! Message %s leased by both %s and %s", m.ID, prevWorker, wid)
				}
				leasedIDs[m.ID] = wid
			}
			mu.Unlock()
		}(workerID)
	}

	wg.Wait()

	// Invariant: exactly 100 messages leased, zero duplicates across workers
	assert.Equal(t, totalMessages, len(leasedIDs), "Each message should be leased exactly once across concurrent workers")
}

func TestOutbox_Poller_HappyPathPublish(t *testing.T) {
	repo := NewMockRepository()
	broker := kafka.NewMockBroker()
	pub := kafka.NewMockPublisher(broker)
	ctx := context.Background()

	cfg := DefaultPollerConfig()
	cfg.WorkerID = "test-worker"
	poller := NewPoller(repo, pub, cfg, slog.Default())

	// Create 3 pending messages with distinct timestamps
	for i := 1; i <= 3; i++ {
		msg, err := NewOutboxMessage("order", fmt.Sprintf("ord-%d", i), "OrderCreated", []byte(`{"id":"`+fmt.Sprintf("ord-%d", i)+`"}`), nil)
		require.NoError(t, err)
		msg.CreatedAt = time.Now().UTC().Add(time.Duration(i) * time.Millisecond)
		require.NoError(t, repo.SaveMessage(ctx, nil, msg))
	}

	count, err := poller.PollAndPublish(ctx)
	require.NoError(t, err)
	assert.Equal(t, 3, count)

	// Verify messages on Kafka
	published := broker.MessagesByTopic("shopflow.orders")
	require.Len(t, published, 3)
	assert.Equal(t, "ord-1", string(published[0].Key))
	assert.Equal(t, "OrderCreated", published[0].Headers["event_type"])

	// Verify status in repository updated to PUBLISHED
	for i := 0; i < 3; i++ {
		msgID := uuid.MustParse(published[i].Headers["message_id"])
		msg, err := repo.GetMessageByID(ctx, msgID)
		require.NoError(t, err)
		assert.Equal(t, StatusPublished, msg.Status)
		assert.NotNil(t, msg.PublishedAt)
		assert.Nil(t, msg.LeasedUntil)
	}
}

func TestOutbox_Poller_PublishFailure_RetryAndDLQ(t *testing.T) {
	repo := NewMockRepository()
	broker := kafka.NewMockBroker()
	pub := kafka.NewMockPublisher(broker)
	ctx := context.Background()

	// Simulate Kafka broker failure
	synthErr := errors.New("kafka: connection refused")
	broker.SetPublishError(synthErr)

	cfg := DefaultPollerConfig()
	cfg.Backoff = BackoffPolicy{
		InitialInterval: 50 * time.Millisecond,
		MaxInterval:     200 * time.Millisecond,
		MaxRetries:      3, // 3 retries max before DLQ
		EnableJitter:    false,
	}
	poller := NewPoller(repo, pub, cfg, slog.Default())

	msg, err := NewOutboxMessage("order", "ord-fail", "OrderCreated", []byte(`{"bad":true}`), nil)
	require.NoError(t, err)
	require.NoError(t, repo.SaveMessage(ctx, nil, msg))

	// Attempt 1: Fails -> status FAILED, retry_count = 1
	n, err := poller.PollAndPublish(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, n)

	stored, err := repo.GetMessageByID(ctx, msg.ID)
	require.NoError(t, err)
	assert.Equal(t, StatusFailed, stored.Status)
	assert.Equal(t, 1, stored.RetryCount)
	assert.NotNil(t, stored.LeasedUntil) // next retry scheduled

	// Advance time past leased_until to simulate retry window
	repo.mu.Lock()
	past := time.Now().UTC().Add(-1 * time.Second)
	repo.messages[msg.ID].LeasedUntil = &past
	repo.mu.Unlock()

	// Attempt 2: Fails -> retry_count = 2
	n, err = poller.PollAndPublish(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, n)

	stored, err = repo.GetMessageByID(ctx, msg.ID)
	require.NoError(t, err)
	assert.Equal(t, StatusFailed, stored.Status)
	assert.Equal(t, 2, stored.RetryCount)

	// Advance time again
	repo.mu.Lock()
	repo.messages[msg.ID].LeasedUntil = &past
	repo.mu.Unlock()

	// Attempt 3: Reaches MaxRetries (3) -> status DEAD_LETTER and record in dead_letter_messages
	n, err = poller.PollAndPublish(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, n)

	stored, err = repo.GetMessageByID(ctx, msg.ID)
	require.NoError(t, err)
	assert.Equal(t, StatusDeadLetter, stored.Status)
	assert.Equal(t, 3, stored.RetryCount)

	// Verify DLQ record
	dlqMsgs, err := repo.GetDeadLetterMessages(ctx, 10)
	require.NoError(t, err)
	require.Len(t, dlqMsgs, 1)
	assert.Equal(t, "OUTBOX", dlqMsgs[0].SourceType)
	assert.Equal(t, msg.ID.String(), dlqMsgs[0].SourceID)
	assert.Equal(t, "shopflow.orders", dlqMsgs[0].Topic)
	assert.Equal(t, 3, dlqMsgs[0].RetryCount)
}

func TestOutbox_Poller_GracefulShutdown_ZeroLeaks(t *testing.T) {
	defer goleak.VerifyNone(t)

	repo := NewMockRepository()
	broker := kafka.NewMockBroker()
	pub := kafka.NewMockPublisher(broker)

	cfg := DefaultPollerConfig()
	cfg.PollInterval = 10 * time.Millisecond
	poller := NewPoller(repo, pub, cfg, slog.Default())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, poller.Start(ctx))
	assert.True(t, poller.IsActive())

	time.Sleep(30 * time.Millisecond)

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer stopCancel()

	require.NoError(t, poller.Stop(stopCtx))
	assert.False(t, poller.IsActive())
}

func TestOutbox_Poller_DoubleStartAndStop(t *testing.T) {
	repo := NewMockRepository()
	broker := kafka.NewMockBroker()
	pub := kafka.NewMockPublisher(broker)

	poller := NewPoller(repo, pub, DefaultPollerConfig(), slog.Default())
	ctx := context.Background()

	require.NoError(t, poller.Start(ctx))
	assert.Error(t, poller.Start(ctx), "double start should return error")

	require.NoError(t, poller.Stop(ctx))
	assert.NoError(t, poller.Stop(ctx), "double stop should be idempotent")
}

func TestOutbox_StrictTransactionBoundaries_NoNetworkInsideDBTx(t *testing.T) {
	// Verify architectural invariant:
	// When poller runs, the DB lease transaction must be closed before Kafka Publish is called.
	repo := NewMockRepository()
	broker := kafka.NewMockBroker()
	pub := kafka.NewMockPublisher(broker)
	ctx := context.Background()

	msg, err := NewOutboxMessage("order", "ord-strict", "OrderCreated", []byte(`{}`), nil)
	require.NoError(t, err)
	require.NoError(t, repo.SaveMessage(ctx, nil, msg))

	var pubCalled atomic.Bool
	// Wrapper publisher that inspects repo state during publish
	inspectingPub := &inspectingPublisher{
		inner: pub,
		onPublish: func() {
			pubCalled.Store(true)
			// At the moment of network publish:
			// The message in repo MUST already be leased (meaning the lease TX has completed and committed!)
			m, getErr := repo.GetMessageByID(ctx, msg.ID)
			if getErr != nil || m.LeasedUntil == nil {
				t.Errorf("Invariant violation: Message not leased prior to publish!")
			}
		},
	}

	cfg := DefaultPollerConfig()
	poller := NewPoller(repo, inspectingPub, cfg, slog.Default())

	count, err := poller.PollAndPublish(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
	assert.True(t, pubCalled.Load())
}

type inspectingPublisher struct {
	inner     kafka.Publisher
	onPublish func()
}

func (p *inspectingPublisher) Publish(ctx context.Context, msg kafka.Message) error {
	if p.onPublish != nil {
		p.onPublish()
	}
	return p.inner.Publish(ctx, msg)
}

func (p *inspectingPublisher) PublishBatch(ctx context.Context, msgs []kafka.Message) error {
	if p.onPublish != nil {
		p.onPublish()
	}
	return p.inner.PublishBatch(ctx, msgs)
}

func (p *inspectingPublisher) Close() error {
	return p.inner.Close()
}
