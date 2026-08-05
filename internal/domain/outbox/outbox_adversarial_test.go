package outbox

import (
	"context"
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

// TestAdversarial_Outbox_MultiPollerHighConcurrency tests 5 concurrent poller instances
// competing to lease and publish 150 messages simultaneously.
// Invariant: Exactly 150 unique messages published, 0 duplicate publishes, 0 lost messages.
func TestAdversarial_Outbox_MultiPollerHighConcurrency(t *testing.T) {
	defer goleak.VerifyNone(t)

	repo := NewMockRepository()
	broker := kafka.NewMockBroker()
	ctx := context.Background()

	numMessages := 150
	for i := 0; i < numMessages; i++ {
		msg, err := NewOutboxMessage("order", fmt.Sprintf("order-%04d", i), "OrderCreated",
			[]byte(fmt.Sprintf(`{"order_id":"order-%04d"}`, i)), nil)
		require.NoError(t, err)
		msg.CreatedAt = time.Now().UTC().Add(time.Duration(i) * time.Millisecond)
		require.NoError(t, repo.SaveMessage(ctx, nil, msg))
	}

	numPollers := 5
	var pollers []*Poller
	for p := 0; p < numPollers; p++ {
		pub := kafka.NewMockPublisher(broker)
		cfg := DefaultPollerConfig()
		cfg.WorkerID = fmt.Sprintf("poller-worker-%d", p)
		cfg.BatchSize = 15
		cfg.LeaseDuration = 5 * time.Second
		poller := NewPoller(repo, pub, cfg, slog.Default())
		pollers = append(pollers, poller)
	}

	// Run all pollers concurrently until all messages are published
	var totalPublished atomic.Int64
	var wg sync.WaitGroup

	for _, poller := range pollers {
		wg.Add(1)
		go func(p *Poller) {
			defer wg.Done()
			for {
				count, err := p.PollAndPublish(ctx)
				if err != nil {
					t.Errorf("PollAndPublish error: %v", err)
					return
				}
				if count == 0 {
					// Check if any pending remain
					pending, _ := repo.GetPendingCount(ctx)
					if pending == 0 {
						return
					}
					time.Sleep(5 * time.Millisecond)
				}
				totalPublished.Add(int64(count))
			}
		}(poller)
	}

	wg.Wait()

	assert.Equal(t, int64(numMessages), totalPublished.Load(),
		"Total published across concurrent pollers must equal total messages")

	// Verify Kafka broker received exactly numMessages
	published := broker.MessagesByTopic("shopflow.orders")
	assert.Equal(t, numMessages, len(published), "Kafka broker must have received exactly 150 messages")

	// Verify all messages are in PUBLISHED status in repository
	for _, m := range published {
		msgID := uuid.MustParse(m.Headers["message_id"])
		stored, err := repo.GetMessageByID(ctx, msgID)
		require.NoError(t, err)
		assert.Equal(t, StatusPublished, stored.Status)
		assert.Nil(t, stored.LeasedUntil)
	}
}

// TestAdversarial_Outbox_BackoffExtremesAndBoundaries checks extreme inputs to BackoffPolicy
func TestAdversarial_Outbox_BackoffExtremesAndBoundaries(t *testing.T) {
	policy := BackoffPolicy{
		InitialInterval: 50 * time.Millisecond,
		MaxInterval:     10 * time.Second,
		MaxRetries:      5,
		EnableJitter:    true,
	}

	// Negative retry count
	dNeg := policy.Calculate(-10)
	assert.GreaterOrEqual(t, dNeg, 50*time.Millisecond)

	// Very large retry count (should not panic or overflow)
	dBig := policy.Calculate(1000)
	assert.LessOrEqual(t, dBig, 10*time.Second+3*time.Second) // capped at MaxInterval + jitter

	// Zero MaxRetries
	zeroRetriesPolicy := BackoffPolicy{MaxRetries: 0}
	assert.True(t, zeroRetriesPolicy.IsMaxRetriesExceeded(0))
	assert.True(t, zeroRetriesPolicy.IsMaxRetriesExceeded(1))
}

// TestAdversarial_Outbox_CustomTopicResolverFallback checks topic routing resilience
func TestAdversarial_Outbox_CustomTopicResolverFallback(t *testing.T) {
	repo := NewMockRepository()
	broker := kafka.NewMockBroker()
	pub := kafka.NewMockPublisher(broker)
	ctx := context.Background()

	cfg := DefaultPollerConfig()
	poller := NewPoller(repo, pub, cfg, slog.Default())

	// Test fallback default topic for unexpected aggregate type
	msg, err := NewOutboxMessage("notification", "notif-001", "NotificationSent", []byte(`{}`), nil)
	require.NoError(t, err)
	require.NoError(t, repo.SaveMessage(ctx, nil, msg))

	n, err := poller.PollAndPublish(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n)

	notifs := broker.MessagesByTopic("shopflow.events")
	require.Len(t, notifs, 1)
	assert.Equal(t, "NotificationSent", notifs[0].Headers["event_type"])
}
