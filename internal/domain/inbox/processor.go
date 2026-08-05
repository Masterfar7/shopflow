package inbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"shopflow/internal/platform/kafka"

	"github.com/google/uuid"
)

// MessageHandler defines the domain business logic invoked on each valid non-duplicate event.
type MessageHandler func(ctx context.Context, msg kafka.Message) error

// ProcessorConfig configures the idempotent inbox message processor.
type ProcessorConfig struct {
	ConsumerGroup string
	DLQTopic      string
}

// Processor manages idempotent message ingestion, deduplication, terminal state protection,
// and poison-pill DLQ routing.
type Processor struct {
	repo     Repository
	consumer kafka.Consumer
	handler  MessageHandler
	cfg      ProcessorConfig
	logger   *slog.Logger

	mu     sync.Mutex
	active bool
}

// NewProcessor creates an inbox Processor.
func NewProcessor(repo Repository, consumer kafka.Consumer, handler MessageHandler, cfg ProcessorConfig, logger *slog.Logger) *Processor {
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.ConsumerGroup == "" {
		cfg.ConsumerGroup = "default-inbox-group"
	}

	return &Processor{
		repo:     repo,
		consumer: consumer,
		handler:  handler,
		cfg:      cfg,
		logger:   logger,
	}
}

// Start initiates the consumer loop using the processor's handle pipeline.
func (p *Processor) Start(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.active {
		return fmt.Errorf("inbox processor is already running")
	}

	if p.consumer != nil {
		if err := p.consumer.Start(ctx, p.ProcessMessage); err != nil {
			return fmt.Errorf("inbox processor: start consumer: %w", err)
		}
	}

	p.active = true
	p.logger.InfoContext(ctx, "inbox processor started", "consumer_group", p.cfg.ConsumerGroup)
	return nil
}

// Stop cleanly terminates the consumer loop.
func (p *Processor) Stop(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.active {
		return nil
	}

	if p.consumer != nil {
		if err := p.consumer.Stop(ctx); err != nil {
			return fmt.Errorf("inbox processor: stop consumer: %w", err)
		}
	}

	p.active = false
	p.logger.InfoContext(ctx, "inbox processor stopped cleanly", "consumer_group", p.cfg.ConsumerGroup)
	return nil
}

// IsActive returns whether the processor is actively running.
func (p *Processor) IsActive() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.active
}

// ProcessMessage processes an incoming Kafka message with deduplication, terminal state protection,
// and poison pill routing.
func (p *Processor) ProcessMessage(ctx context.Context, msg kafka.Message) error {
	// Extract message ID
	messageID := msg.Headers["message_id"]
	if messageID == "" {
		messageID = string(msg.Key)
	}

	// Poison pill check: messages without ID or with invalid payload
	if messageID == "" {
		p.logger.WarnContext(ctx, "received message with missing message_id/key, routing to DLQ",
			"topic", msg.Topic,
			"offset", msg.Offset,
		)
		return p.routeToDLQ(ctx, msg, "missing message_id or key")
	}

	// Validate JSON payload
	if len(msg.Value) > 0 && !json.Valid(msg.Value) {
		p.logger.WarnContext(ctx, "received malformed JSON payload, routing to DLQ",
			"message_id", messageID,
			"topic", msg.Topic,
		)
		_ = p.routeToDLQ(ctx, msg, "malformed JSON payload")
		return nil
	}

	inboxMsg := &InboxMessage{
		MessageID:     messageID,
		ConsumerGroup: p.cfg.ConsumerGroup,
		EventType:     msg.Headers["event_type"],
		Payload:       msg.Value,
		Status:        StatusProcessing,
		ProcessedAt:   time.Now().UTC(),
	}

	// Step 1: Deduplication check via PRIMARY KEY (message_id, consumer_group)
	started, err := p.repo.TryStartProcessing(ctx, nil, inboxMsg)
	if err != nil {
		if errors.Is(err, ErrDuplicateMessage) {
			p.logger.InfoContext(ctx, "duplicate message currently in flight, skipping",
				"message_id", messageID,
				"consumer_group", p.cfg.ConsumerGroup,
			)
			return nil
		}
		return fmt.Errorf("inbox: try start processing: %w", err)
	}

	if !started {
		p.logger.InfoContext(ctx, "duplicate message already completed, skipping idempotently",
			"message_id", messageID,
			"consumer_group", p.cfg.ConsumerGroup,
		)
		return nil
	}

	// Step 2: Invoke domain message handler
	handlerErr := p.handler(ctx, msg)
	if handlerErr != nil {
		// Case A: Terminal state protection — aggregate is already in final state, event is safely no-oped
		if errors.Is(handlerErr, ErrTerminalStateIgnored) {
			p.logger.InfoContext(ctx, "event arrived for terminal aggregate, safely ignored",
				"message_id", messageID,
				"topic", msg.Topic,
			)
			if markErr := p.repo.MarkCompleted(ctx, nil, messageID, p.cfg.ConsumerGroup); markErr != nil {
				p.logger.ErrorContext(ctx, "failed to mark terminal ignored message completed", "error", markErr)
			}
			return nil
		}

		// Case B: Poison pill — unrecoverable domain parsing or validation error
		if errors.Is(handlerErr, ErrPoisonPill) {
			p.logger.WarnContext(ctx, "domain handler rejected message as poison pill, routing to DLQ",
				"message_id", messageID,
				"error", handlerErr,
			)
			_ = p.routeToDLQ(ctx, msg, handlerErr.Error())
			// Mark completed in inbox so consumer does not retry the poisoned message
			_ = p.repo.MarkCompleted(ctx, nil, messageID, p.cfg.ConsumerGroup)
			return nil
		}

		// Case C: Transient error — mark FAILED so it can be retried
		p.logger.ErrorContext(ctx, "message handler failed with transient error",
			"message_id", messageID,
			"error", handlerErr,
		)
		_ = p.repo.MarkFailed(ctx, nil, messageID, p.cfg.ConsumerGroup)
		return handlerErr
	}

	// Step 3: Successfully handled -> Mark COMPLETED
	if err := p.repo.MarkCompleted(ctx, nil, messageID, p.cfg.ConsumerGroup); err != nil {
		p.logger.ErrorContext(ctx, "failed to mark inbox message completed",
			"message_id", messageID,
			"error", err,
		)
		return fmt.Errorf("inbox: mark completed: %w", err)
	}

	return nil
}

func (p *Processor) routeToDLQ(ctx context.Context, msg kafka.Message, reason string) error {
	sourceID := msg.Headers["message_id"]
	if sourceID == "" {
		sourceID = string(msg.Key)
	}
	if sourceID == "" {
		sourceID = "unknown-" + uuid.New().String()
	}

	dlq := &DeadLetterMessage{
		ID:          uuid.New(),
		SourceType:  "INBOX",
		SourceID:    sourceID,
		Topic:       msg.Topic,
		Partition:   int(msg.Partition),
		OffsetVal:   msg.Offset,
		ErrorReason: reason,
		Payload:     msg.Value,
		Headers:     msg.Headers,
		RetryCount:  1,
		CreatedAt:   time.Now().UTC(),
	}

	if err := p.repo.SaveDeadLetter(ctx, nil, dlq); err != nil {
		p.logger.ErrorContext(ctx, "failed to record dead letter in inbox", "error", err)
		return err
	}
	return nil
}
