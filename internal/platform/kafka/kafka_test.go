package kafka

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

func TestKafka_MessageRecordConversion(t *testing.T) {
	orig := Message{
		Topic:     "test-topic",
		Key:       []byte("test-key"),
		Value:     []byte(`{"hello":"world"}`),
		Headers:   map[string]string{"ce_type": "OrderCreated", "traceparent": "00-12345-67890-01"},
		Timestamp: time.Now().Truncate(time.Millisecond),
		Partition: 2,
		Offset:    42,
	}

	record := orig.ToRecord()
	assert.Equal(t, orig.Topic, record.Topic)
	assert.Equal(t, orig.Key, record.Key)
	assert.Equal(t, orig.Value, record.Value)
	assert.Equal(t, orig.Partition, record.Partition)
	assert.Equal(t, orig.Offset, record.Offset)

	converted := FromRecord(record)
	assert.Equal(t, orig.Topic, converted.Topic)
	assert.Equal(t, orig.Key, converted.Key)
	assert.Equal(t, orig.Value, converted.Value)
	assert.Equal(t, orig.Headers["ce_type"], converted.Headers["ce_type"])
	assert.Equal(t, orig.Headers["traceparent"], converted.Headers["traceparent"])
}

func TestKafka_MockPublisherAndConsumer(t *testing.T) {
	defer goleak.VerifyNone(t)

	broker := NewMockBroker()
	pub := NewMockPublisher(broker)
	defer pub.Close()

	consumer := NewMockConsumer(broker, []string{"orders.events"}, "test-group")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var mu sync.Mutex
	var received []Message

	handler := func(ctx context.Context, msg Message) error {
		mu.Lock()
		received = append(received, msg)
		mu.Unlock()
		return nil
	}

	require.NoError(t, consumer.Start(ctx, handler))
	assert.True(t, consumer.IsActive())

	// Publish messages
	msg1 := Message{
		Topic: "orders.events",
		Key:   []byte("ord-1"),
		Value: []byte(`{"order_id":"ord-1"}`),
		Headers: map[string]string{
			"event_type": "OrderCreated",
		},
	}
	require.NoError(t, pub.Publish(ctx, msg1))

	msg2 := Message{
		Topic: "orders.events",
		Key:   []byte("ord-2"),
		Value: []byte(`{"order_id":"ord-2"}`),
		Headers: map[string]string{
			"event_type": "OrderCreated",
		},
	}
	require.NoError(t, pub.Publish(ctx, msg2))

	// Verify delivery
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(received) == 2
	}, 1*time.Second, 10*time.Millisecond)

	mu.Lock()
	assert.Equal(t, "ord-1", string(received[0].Key))
	assert.Equal(t, "ord-2", string(received[1].Key))
	mu.Unlock()

	// Stop consumer cleanly
	require.NoError(t, consumer.Stop(ctx))
	assert.False(t, consumer.IsActive())
}

func TestKafka_MockBroker_FanOutToMultipleConsumerGroups(t *testing.T) {
	defer goleak.VerifyNone(t)

	broker := NewMockBroker()
	pub := NewMockPublisher(broker)
	defer pub.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var g1Msgs, g2Msgs []Message
	var mu1, mu2 sync.Mutex

	c1 := NewMockConsumer(broker, []string{"inventory.events"}, "group-1")
	c2 := NewMockConsumer(broker, []string{"inventory.events"}, "group-2")

	require.NoError(t, c1.Start(ctx, func(ctx context.Context, msg Message) error {
		mu1.Lock()
		g1Msgs = append(g1Msgs, msg)
		mu1.Unlock()
		return nil
	}))
	require.NoError(t, c2.Start(ctx, func(ctx context.Context, msg Message) error {
		mu2.Lock()
		g2Msgs = append(g2Msgs, msg)
		mu2.Unlock()
		return nil
	}))

	msg := Message{Topic: "inventory.events", Key: []byte("k1"), Value: []byte("v1")}
	require.NoError(t, pub.Publish(ctx, msg))

	require.Eventually(t, func() bool {
		mu1.Lock()
		l1 := len(g1Msgs)
		mu1.Unlock()
		mu2.Lock()
		l2 := len(g2Msgs)
		mu2.Unlock()
		return l1 == 1 && l2 == 1
	}, 1*time.Second, 10*time.Millisecond)

	require.NoError(t, c1.Stop(ctx))
	require.NoError(t, c2.Stop(ctx))
}

func TestKafka_MockPublisher_ErrorInjection(t *testing.T) {
	broker := NewMockBroker()
	pub := NewMockPublisher(broker)
	defer pub.Close()

	synthErr := errors.New("simulated kafka broker down")
	broker.SetPublishError(synthErr)

	ctx := context.Background()
	err := pub.Publish(ctx, Message{Topic: "test", Value: []byte("val")})
	assert.ErrorIs(t, err, synthErr)

	broker.SetPublishError(nil)
	err = pub.Publish(ctx, Message{Topic: "test", Value: []byte("val")})
	assert.NoError(t, err)
}

func TestKafka_MockConsumer_DoubleStartAndStop(t *testing.T) {
	defer goleak.VerifyNone(t)

	broker := NewMockBroker()
	c := NewMockConsumer(broker, []string{"topic"}, "group")
	ctx := context.Background()

	require.NoError(t, c.Start(ctx, func(ctx context.Context, msg Message) error { return nil }))
	assert.Error(t, c.Start(ctx, func(ctx context.Context, msg Message) error { return nil }), "double start should error")

	require.NoError(t, c.Stop(ctx))
	assert.NoError(t, c.Stop(ctx), "double stop should be idempotent")
}
