package kafka

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/twmb/franz-go/pkg/kgo"
)

// Publisher defines an abstraction for publishing messages to Kafka.
type Publisher interface {
	Publish(ctx context.Context, msg Message) error
	PublishBatch(ctx context.Context, msgs []Message) error
	Close() error
}

// FranzPublisher implements Publisher using github.com/twmb/franz-go.
type FranzPublisher struct {
	client     *kgo.Client
	logger     *slog.Logger
	ownsClient bool
}

// PublisherConfig configures the FranzPublisher.
type PublisherConfig struct {
	Brokers []string
}

// NewFranzPublisher creates a publisher from an existing franz-go client.
func NewFranzPublisher(client *kgo.Client, logger *slog.Logger) *FranzPublisher {
	if logger == nil {
		logger = slog.Default()
	}
	return &FranzPublisher{
		client:     client,
		logger:     logger,
		ownsClient: false,
	}
}

// NewFranzPublisherFromConfig creates a publisher by initializing a new franz-go client.
func NewFranzPublisherFromConfig(cfg PublisherConfig, logger *slog.Logger, extraOpts ...kgo.Opt) (*FranzPublisher, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if len(cfg.Brokers) == 0 {
		return nil, fmt.Errorf("kafka publisher: at least one broker is required")
	}

	opts := []kgo.Opt{
		kgo.SeedBrokers(cfg.Brokers...),
		kgo.ProducerBatchCompression(kgo.SnappyCompression()),
		kgo.RequiredAcks(kgo.AllISRAcks()),
	}
	opts = append(opts, extraOpts...)

	client, err := kgo.NewClient(opts...)
	if err != nil {
		return nil, fmt.Errorf("kafka publisher: initialize client: %w", err)
	}

	return &FranzPublisher{
		client:     client,
		logger:     logger,
		ownsClient: true,
	}, nil
}

// Publish publishes a single message synchronously.
func (p *FranzPublisher) Publish(ctx context.Context, msg Message) error {
	record := msg.ToRecord()
	results := p.client.ProduceSync(ctx, record)
	if err := results.FirstErr(); err != nil {
		p.logger.ErrorContext(ctx, "failed to publish kafka message",
			"topic", msg.Topic,
			"key", string(msg.Key),
			"error", err,
		)
		return fmt.Errorf("kafka publish to topic %q: %w", msg.Topic, err)
	}
	return nil
}

// PublishBatch publishes multiple messages synchronously.
func (p *FranzPublisher) PublishBatch(ctx context.Context, msgs []Message) error {
	if len(msgs) == 0 {
		return nil
	}

	records := make([]*kgo.Record, len(msgs))
	for i, m := range msgs {
		records[i] = m.ToRecord()
	}

	results := p.client.ProduceSync(ctx, records...)
	if err := results.FirstErr(); err != nil {
		p.logger.ErrorContext(ctx, "failed to publish kafka batch",
			"count", len(msgs),
			"error", err,
		)
		return fmt.Errorf("kafka publish batch: %w", err)
	}
	return nil
}

// Close flushes buffered messages and closes the client if owned.
func (p *FranzPublisher) Close() error {
	if p.client != nil {
		p.client.Flush(context.Background())
		if p.ownsClient {
			p.client.Close()
		}
	}
	return nil
}
