package kafka

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
)

// HandlerFunc is invoked for each incoming Kafka message.
type HandlerFunc func(ctx context.Context, msg Message) error

// Consumer defines the managed lifecycle for consuming messages from Kafka.
type Consumer interface {
	Start(ctx context.Context, handler HandlerFunc) error
	Stop(ctx context.Context) error
	IsActive() bool
}

// ConsumerConfig configures the FranzConsumer.
type ConsumerConfig struct {
	Brokers []string
	Group   string
	Topics  []string
}

// FranzConsumer implements Consumer using github.com/twmb/franz-go.
type FranzConsumer struct {
	client     *kgo.Client
	topics     []string
	group      string
	logger     *slog.Logger
	ownsClient bool

	mu     sync.Mutex
	active bool
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewFranzConsumer creates a consumer from an existing franz-go client.
func NewFranzConsumer(client *kgo.Client, topics []string, group string, logger *slog.Logger) *FranzConsumer {
	if logger == nil {
		logger = slog.Default()
	}
	return &FranzConsumer{
		client:     client,
		topics:     topics,
		group:      group,
		logger:     logger,
		ownsClient: false,
	}
}

// NewFranzConsumerFromConfig initializes a new franz-go client configured as a consumer.
func NewFranzConsumerFromConfig(cfg ConsumerConfig, logger *slog.Logger, extraOpts ...kgo.Opt) (*FranzConsumer, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if len(cfg.Brokers) == 0 {
		return nil, fmt.Errorf("kafka consumer: at least one broker is required")
	}
	if len(cfg.Topics) == 0 {
		return nil, fmt.Errorf("kafka consumer: at least one topic is required")
	}

	opts := []kgo.Opt{
		kgo.SeedBrokers(cfg.Brokers...),
		kgo.ConsumeTopics(cfg.Topics...),
	}
	if cfg.Group != "" {
		opts = append(opts, kgo.ConsumerGroup(cfg.Group))
	}
	opts = append(opts, extraOpts...)

	client, err := kgo.NewClient(opts...)
	if err != nil {
		return nil, fmt.Errorf("kafka consumer: initialize client: %w", err)
	}

	return &FranzConsumer{
		client:     client,
		topics:     cfg.Topics,
		group:      cfg.Group,
		logger:     logger,
		ownsClient: true,
	}, nil
}

// Start begins consuming messages in a background goroutine.
func (c *FranzConsumer) Start(ctx context.Context, handler HandlerFunc) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.active {
		return fmt.Errorf("kafka consumer is already running")
	}
	if handler == nil {
		return fmt.Errorf("kafka consumer handler cannot be nil")
	}

	runCtx, cancel := context.WithCancel(ctx)
	c.cancel = cancel
	c.active = true

	c.wg.Add(1)
	go c.consumeLoop(runCtx, handler)

	c.logger.InfoContext(ctx, "kafka consumer started",
		"group", c.group,
		"topics", c.topics,
	)
	return nil
}

// Stop signals the consumer loop to terminate and waits cleanly for completion.
func (c *FranzConsumer) Stop(ctx context.Context) error {
	c.mu.Lock()
	if !c.active {
		c.mu.Unlock()
		return nil
	}
	c.cancel()
	c.active = false
	c.mu.Unlock()

	// Wait for background routine to exit
	done := make(chan struct{})
	go func() {
		c.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		if c.ownsClient && c.client != nil {
			c.client.Close()
		}
		c.logger.InfoContext(ctx, "kafka consumer stopped cleanly", "group", c.group)
		return nil
	case <-ctx.Done():
		return fmt.Errorf("kafka consumer stop timed out: %w", ctx.Err())
	}
}

// IsActive returns whether the consumer is currently active.
func (c *FranzConsumer) IsActive() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.active
}

func (c *FranzConsumer) consumeLoop(ctx context.Context, handler HandlerFunc) {
	defer c.wg.Done()

	for {
		if ctx.Err() != nil {
			return
		}

		fetches := c.client.PollFetches(ctx)
		if fetches.IsClientClosed() || ctx.Err() != nil {
			return
		}

		if errs := fetches.Errors(); len(errs) > 0 {
			for _, err := range errs {
				if ctx.Err() != nil {
					return
				}
				c.logger.ErrorContext(ctx, "kafka consumer fetch error",
					"error", err.Err,
					"topic", err.Topic,
				)
			}
		}

		iter := fetches.RecordIter()
		for !iter.Done() {
			if ctx.Err() != nil {
				return
			}
			record := iter.Next()
			msg := FromRecord(record)

			if err := handler(ctx, msg); err != nil {
				c.logger.ErrorContext(ctx, "kafka consumer message handler returned error",
					"topic", msg.Topic,
					"partition", msg.Partition,
					"offset", msg.Offset,
					"error", err,
				)
			}

			commitCtx, commitCancel := context.WithTimeout(ctx, 3*time.Second)
			if err := c.client.CommitRecords(commitCtx, record); err != nil {
				c.logger.WarnContext(commitCtx, "failed to commit kafka record", "error", err)
			}
			commitCancel()
		}
	}
}
