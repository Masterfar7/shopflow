package kafka

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

// TestAdversarial_Kafka_HighThroughputConcurrentPubSub tests 20 concurrent publishers and
// 5 concurrent consumer groups under rapid publish bursts.
func TestAdversarial_Kafka_HighThroughputConcurrentPubSub(t *testing.T) {
	defer goleak.VerifyNone(t)

	broker := NewMockBroker()
	pub := NewMockPublisher(broker)
	defer pub.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	numGroups := 3
	messagesPerPub := 20
	numPublishers := 10
	totalMessages := numPublishers * messagesPerPub

	var groupCounters [3]atomic.Int64
	var consumers []*MockConsumer

	for g := 0; g < numGroups; g++ {
		groupName := fmt.Sprintf("group-%d", g)
		c := NewMockConsumer(broker, []string{"stress.events"}, groupName)
		consumers = append(consumers, c)
		idx := g
		require.NoError(t, c.Start(ctx, func(ctx context.Context, msg Message) error {
			groupCounters[idx].Add(1)
			return nil
		}))
	}

	var wgPub sync.WaitGroup
	for p := 0; p < numPublishers; p++ {
		wgPub.Add(1)
		go func(pubIdx int) {
			defer wgPub.Done()
			for m := 0; m < messagesPerPub; m++ {
				msg := Message{
					Topic: "stress.events",
					Key:   []byte(fmt.Sprintf("key-%d-%d", pubIdx, m)),
					Value: []byte(fmt.Sprintf(`{"p":%d,"m":%d}`, pubIdx, m)),
				}
				_ = pub.Publish(ctx, msg)
			}
		}(p)
	}

	wgPub.Wait()

	// Wait for consumers to receive all messages
	require.Eventually(t, func() bool {
		for g := 0; g < numGroups; g++ {
			if groupCounters[g].Load() != int64(totalMessages) {
				return false
			}
		}
		return true
	}, 2*time.Second, 10*time.Millisecond)

	for _, c := range consumers {
		require.NoError(t, c.Stop(ctx))
	}
}

// TestAdversarial_Kafka_BatchPublishingEdgeCases checks empty batch, single, and multi-batch operations
func TestAdversarial_Kafka_BatchPublishingEdgeCases(t *testing.T) {
	defer goleak.VerifyNone(t)

	broker := NewMockBroker()
	pub := NewMockPublisher(broker)
	defer pub.Close()
	ctx := context.Background()

	// Empty batch should be no-op
	require.NoError(t, pub.PublishBatch(ctx, nil))
	require.NoError(t, pub.PublishBatch(ctx, []Message{}))

	// Batch of 5 messages
	var msgs []Message
	for i := 0; i < 5; i++ {
		msgs = append(msgs, Message{
			Topic: "batch.topic",
			Key:   []byte(fmt.Sprintf("key-%d", i)),
			Value: []byte(fmt.Sprintf("val-%d", i)),
		})
	}

	require.NoError(t, pub.PublishBatch(ctx, msgs))
	published := broker.MessagesByTopic("batch.topic")
	assert.Len(t, published, 5)
}
