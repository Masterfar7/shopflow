package kafka

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// MockBroker is an in-memory thread-safe Kafka broker simulator.
type MockBroker struct {
	mu           sync.RWMutex
	messages     map[string][]Message
	subscribers  map[string]map[string][]chan Message // topic -> group -> list of channels
	offsets      map[string]int64
	publishError error
	consumeDelay time.Duration
}

// NewMockBroker creates an initialized MockBroker.
func NewMockBroker() *MockBroker {
	return &MockBroker{
		messages:    make(map[string][]Message),
		subscribers: make(map[string]map[string][]chan Message),
		offsets:     make(map[string]int64),
	}
}

// SetPublishError configures a synthetic error to be returned on publish calls.
func (b *MockBroker) SetPublishError(err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.publishError = err
}

// SetConsumeDelay sets an artificial delay before delivering messages.
func (b *MockBroker) SetConsumeDelay(d time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.consumeDelay = d
}

// Publish writes a message to the broker and fans out to consumer groups.
func (b *MockBroker) Publish(ctx context.Context, msg Message) error {
	b.mu.Lock()
	if b.publishError != nil {
		err := b.publishError
		b.mu.Unlock()
		return err
	}

	if msg.Timestamp.IsZero() {
		msg.Timestamp = time.Now()
	}

	b.offsets[msg.Topic]++
	msg.Offset = b.offsets[msg.Topic]

	// Deep copy headers
	if msg.Headers != nil {
		hCopy := make(map[string]string, len(msg.Headers))
		for k, v := range msg.Headers {
			hCopy[k] = v
		}
		msg.Headers = hCopy
	}

	// Copy value
	if msg.Value != nil {
		vCopy := make([]byte, len(msg.Value))
		copy(vCopy, msg.Value)
		msg.Value = vCopy
	}

	b.messages[msg.Topic] = append(b.messages[msg.Topic], msg)

	// Deliver to subscribers: each group gets one copy (round-robin among channels in group)
	var targets []chan Message
	if groups, ok := b.subscribers[msg.Topic]; ok {
		for _, chans := range groups {
			if len(chans) > 0 {
				// Pick first available channel in group
				targets = append(targets, chans[0])
			}
		}
	}
	b.mu.Unlock()

	for _, ch := range targets {
		select {
		case ch <- msg:
		case <-ctx.Done():
			return ctx.Err()
		default:
			// Buffer full or drop in non-blocking mock
		}
	}

	return nil
}

func (b *MockBroker) subscribe(topic, group string) chan Message {
	b.mu.Lock()
	defer b.mu.Unlock()

	ch := make(chan Message, 1000)
	if _, ok := b.subscribers[topic]; !ok {
		b.subscribers[topic] = make(map[string][]chan Message)
	}
	b.subscribers[topic][group] = append(b.subscribers[topic][group], ch)
	return ch
}

func (b *MockBroker) unsubscribe(topic, group string, ch chan Message) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if groups, ok := b.subscribers[topic]; ok {
		if chans, ok := groups[group]; ok {
			var updated []chan Message
			for _, c := range chans {
				if c != ch {
					updated = append(updated, c)
				}
			}
			groups[group] = updated
		}
	}
}

// MessagesByTopic returns all messages published to a specific topic.
func (b *MockBroker) MessagesByTopic(topic string) []Message {
	b.mu.RLock()
	defer b.mu.RUnlock()

	msgs := b.messages[topic]
	result := make([]Message, len(msgs))
	copy(result, msgs)
	return result
}

// PublishedMessages returns all messages across all topics.
func (b *MockBroker) PublishedMessages() []Message {
	b.mu.RLock()
	defer b.mu.RUnlock()

	var result []Message
	for _, msgs := range b.messages {
		result = append(result, msgs...)
	}
	return result
}

// Clear removes all stored messages and offsets.
func (b *MockBroker) Clear() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.messages = make(map[string][]Message)
	b.offsets = make(map[string]int64)
	b.publishError = nil
}

// MockPublisher is a thread-safe mock implementation of Publisher.
type MockPublisher struct {
	broker    *MockBroker
	mu        sync.RWMutex
	published []Message
	closed    bool
}

// NewMockPublisher creates a new MockPublisher backed by a MockBroker.
func NewMockPublisher(broker *MockBroker) *MockPublisher {
	if broker == nil {
		broker = NewMockBroker()
	}
	return &MockPublisher{
		broker: broker,
	}
}

// Publish publishes a message to the in-memory broker.
func (p *MockPublisher) Publish(ctx context.Context, msg Message) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return fmt.Errorf("mock publisher is closed")
	}
	p.published = append(p.published, msg)
	p.mu.Unlock()

	return p.broker.Publish(ctx, msg)
}

// PublishBatch publishes multiple messages to the in-memory broker.
func (p *MockPublisher) PublishBatch(ctx context.Context, msgs []Message) error {
	for _, msg := range msgs {
		if err := p.Publish(ctx, msg); err != nil {
			return err
		}
	}
	return nil
}

// PublishedMessages returns a slice of all messages published through this publisher.
func (p *MockPublisher) PublishedMessages() []Message {
	p.mu.RLock()
	defer p.mu.RUnlock()
	result := make([]Message, len(p.published))
	copy(result, p.published)
	return result
}

// Close closes the mock publisher.
func (p *MockPublisher) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	return nil
}

// MockConsumer is a thread-safe mock implementation of Consumer.
type MockConsumer struct {
	broker *MockBroker
	topics []string
	group  string

	mu       sync.Mutex
	active   bool
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	channels []chan Message
}

// NewMockConsumer creates a new MockConsumer.
func NewMockConsumer(broker *MockBroker, topics []string, group string) *MockConsumer {
	return &MockConsumer{
		broker: broker,
		topics: topics,
		group:  group,
	}
}

// Start begins consuming messages from the mock broker.
func (c *MockConsumer) Start(ctx context.Context, handler HandlerFunc) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.active {
		return fmt.Errorf("mock consumer is already running")
	}
	if handler == nil {
		return fmt.Errorf("mock consumer handler cannot be nil")
	}

	runCtx, cancel := context.WithCancel(ctx)
	c.cancel = cancel
	c.active = true

	c.channels = make([]chan Message, len(c.topics))
	for i, topic := range c.topics {
		ch := c.broker.subscribe(topic, c.group)
		c.channels[i] = ch

		c.wg.Add(1)
		go c.consumeChannel(runCtx, topic, ch, handler)
	}

	return nil
}

// Stop terminates consumption and cleans up subscribers without leaking routines.
func (c *MockConsumer) Stop(ctx context.Context) error {
	c.mu.Lock()
	if !c.active {
		c.mu.Unlock()
		return nil
	}
	c.cancel()
	c.active = false

	for i, topic := range c.topics {
		if i < len(c.channels) {
			c.broker.unsubscribe(topic, c.group, c.channels[i])
		}
	}
	c.mu.Unlock()

	c.wg.Wait()
	return nil
}

// IsActive returns whether the mock consumer is running.
func (c *MockConsumer) IsActive() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.active
}

func (c *MockConsumer) consumeChannel(ctx context.Context, topic string, ch chan Message, handler HandlerFunc) {
	defer c.wg.Done()

	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			_ = handler(ctx, msg)
		}
	}
}
