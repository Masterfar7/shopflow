package notification

import (
	"context"
	"log/slog"

	"shopflow/internal/domain/inbox"
	"shopflow/internal/platform/kafka"
)

// Consumer manages the consumption of order events from Kafka using the idempotent inbox pattern.
type Consumer struct {
	processor *inbox.Processor
	logger    *slog.Logger
}

// NewConsumer creates a notification consumer with consumer_group="notification-service".
func NewConsumer(
	inboxRepo inbox.Repository,
	kafkaConsumer kafka.Consumer,
	svc *Service,
	logger *slog.Logger,
) *Consumer {
	if logger == nil {
		logger = slog.Default()
	}

	processor := inbox.NewProcessor(
		inboxRepo,
		kafkaConsumer,
		svc.HandleMessage,
		inbox.ProcessorConfig{
			ConsumerGroup: "notification-service",
			DLQTopic:      "shopflow.deadletter",
		},
		logger,
	)

	return &Consumer{
		processor: processor,
		logger:    logger,
	}
}

// Start begins processing incoming events.
func (c *Consumer) Start(ctx context.Context) error {
	return c.processor.Start(ctx)
}

// Stop cleanly terminates the consumer loop and waits for in-flight tasks to complete.
func (c *Consumer) Stop(ctx context.Context) error {
	return c.processor.Stop(ctx)
}

// IsActive returns whether the consumer loop is actively running.
func (c *Consumer) IsActive() bool {
	return c.processor.IsActive()
}
