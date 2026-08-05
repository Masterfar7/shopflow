package outbox

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"shopflow/internal/platform/kafka"

	"github.com/google/uuid"
)

// TopicResolver maps an OutboxMessage to its destination Kafka topic.
type TopicResolver func(msg *OutboxMessage) string

// DefaultTopicResolver routes messages based on aggregate type.
func DefaultTopicResolver(msg *OutboxMessage) string {
	switch msg.AggregateType {
	case "order":
		return "shopflow.orders"
	case "inventory":
		return "shopflow.inventory"
	case "payment":
		return "shopflow.payments"
	default:
		return "shopflow.events"
	}
}

// PollerConfig holds configuration for the Outbox Poller service.
type PollerConfig struct {
	PollInterval  time.Duration
	BatchSize     int
	LeaseDuration time.Duration
	WorkerID      string
	Backoff       BackoffPolicy
	DLQTopic      string
	TopicResolver TopicResolver
}

// DefaultPollerConfig returns production defaults for the outbox poller.
func DefaultPollerConfig() PollerConfig {
	return PollerConfig{
		PollInterval:  100 * time.Millisecond,
		BatchSize:     50,
		LeaseDuration: 30 * time.Second,
		WorkerID:      "outbox-worker-" + uuid.New().String()[:8],
		Backoff:       DefaultBackoffPolicy(),
		DLQTopic:      "shopflow.deadletter",
		TopicResolver: DefaultTopicResolver,
	}
}

// Poller runs the polling loop that leases outbox records and publishes them to Kafka.
// It guarantees STRICT TRANSACTION BOUNDARIES: zero Kafka network I/O inside DB transactions.
type Poller struct {
	repo      Repository
	publisher kafka.Publisher
	cfg       PollerConfig
	logger    *slog.Logger

	mu     sync.Mutex
	active bool
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewPoller initializes an Outbox Poller.
func NewPoller(repo Repository, publisher kafka.Publisher, cfg PollerConfig, logger *slog.Logger) *Poller {
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 100 * time.Millisecond
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 50
	}
	if cfg.LeaseDuration <= 0 {
		cfg.LeaseDuration = 30 * time.Second
	}
	if cfg.WorkerID == "" {
		cfg.WorkerID = "outbox-worker-" + uuid.New().String()[:8]
	}
	if cfg.TopicResolver == nil {
		cfg.TopicResolver = DefaultTopicResolver
	}

	return &Poller{
		repo:      repo,
		publisher: publisher,
		cfg:       cfg,
		logger:    logger,
	}
}

// Start launches the background polling loop.
func (p *Poller) Start(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.active {
		return fmt.Errorf("outbox poller is already running")
	}

	runCtx, cancel := context.WithCancel(ctx)
	p.cancel = cancel
	p.active = true

	p.wg.Add(1)
	go p.run(runCtx)

	p.logger.InfoContext(ctx, "outbox poller started",
		"worker_id", p.cfg.WorkerID,
		"poll_interval", p.cfg.PollInterval,
		"batch_size", p.cfg.BatchSize,
	)
	return nil
}

// Stop terminates the polling loop cleanly and waits for all in-flight messages to conclude.
func (p *Poller) Stop(ctx context.Context) error {
	p.mu.Lock()
	if !p.active {
		p.mu.Unlock()
		return nil
	}
	p.cancel()
	p.active = false
	p.mu.Unlock()

	done := make(chan struct{})
	go func() {
		p.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		p.logger.InfoContext(ctx, "outbox poller stopped cleanly", "worker_id", p.cfg.WorkerID)
		return nil
	case <-ctx.Done():
		return fmt.Errorf("outbox poller stop timed out: %w", ctx.Err())
	}
}

// IsActive reports whether the poller is currently running.
func (p *Poller) IsActive() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.active
}

func (p *Poller) run(ctx context.Context) {
	defer p.wg.Done()

	ticker := time.NewTicker(p.cfg.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_, _ = p.PollAndPublish(ctx)
		}
	}
}

// PollAndPublish executes a single lease-publish-ack cycle.
// STRICT INVARIANT:
// 1. Lease transaction runs and COMMITS immediately.
// 2. Kafka publish is executed over the network (NO DB TX).
// 3. MarkPublished is executed in a subsequent independent operation.
func (p *Poller) PollAndPublish(ctx context.Context) (int, error) {
	// Step 1: Lease batch with SKIP LOCKED (DB TX opens and COMMITS inside LeaseMessages)
	leased, err := p.repo.LeaseMessages(ctx, p.cfg.WorkerID, p.cfg.BatchSize, p.cfg.LeaseDuration)
	if err != nil {
		p.logger.ErrorContext(ctx, "failed to lease outbox messages", "error", err)
		return 0, err
	}
	if len(leased) == 0 {
		return 0, nil
	}

	publishedCount := 0

	// Step 2 & 3: Iterate through leased messages, publish to Kafka with zero DB tx held
	for _, msg := range leased {
		if ctx.Err() != nil {
			return publishedCount, ctx.Err()
		}

		topic := p.cfg.TopicResolver(msg)
		headers := make(map[string]string, len(msg.Headers)+4)
		for k, v := range msg.Headers {
			headers[k] = v
		}
		headers["event_type"] = msg.EventType
		headers["aggregate_type"] = msg.AggregateType
		headers["aggregate_id"] = msg.AggregateID
		headers["message_id"] = msg.ID.String()
		if msg.TraceContext != nil && *msg.TraceContext != "" {
			headers["traceparent"] = *msg.TraceContext
		}

		kafkaMsg := kafka.Message{
			Topic:     topic,
			Key:       []byte(msg.AggregateID),
			Value:     msg.Payload,
			Headers:   headers,
			Timestamp: msg.CreatedAt,
		}

		// Network call to Kafka
		publishErr := p.publisher.Publish(ctx, kafkaMsg)
		if publishErr == nil {
			// Publish succeeded -> Mark published in subsequent DB call
			if markErr := p.repo.MarkPublished(ctx, msg.ID); markErr != nil {
				p.logger.ErrorContext(ctx, "failed to mark message published",
					"id", msg.ID,
					"error", markErr,
				)
			} else {
				publishedCount++
			}
		} else {
			// Publish failed -> Process backoff and dead letter routing
			p.handlePublishFailure(ctx, msg, topic, publishErr)
		}
	}

	return publishedCount, nil
}

func (p *Poller) handlePublishFailure(ctx context.Context, msg *OutboxMessage, topic string, pubErr error) {
	nextRetryCount := msg.RetryCount + 1
	errMsg := pubErr.Error()

	p.logger.WarnContext(ctx, "outbox message publish failed",
		"id", msg.ID,
		"retry_count", nextRetryCount,
		"error", pubErr,
	)

	if p.cfg.Backoff.IsMaxRetriesExceeded(nextRetryCount) {
		// Route to Dead Letter
		p.logger.ErrorContext(ctx, "outbox message exceeded max retries, transitioning to DEAD_LETTER",
			"id", msg.ID,
			"retries", nextRetryCount,
		)

		if err := p.repo.MarkFailed(ctx, msg.ID, errMsg, time.Time{}, true); err != nil {
			p.logger.ErrorContext(ctx, "failed to mark message dead letter", "id", msg.ID, "error", err)
		}

		dlq := &DeadLetterMessage{
			ID:          uuid.New(),
			SourceType:  "OUTBOX",
			SourceID:    msg.ID.String(),
			Topic:       topic,
			ErrorReason: fmt.Sprintf("Exceeded max retries (%d): %v", nextRetryCount, pubErr),
			Payload:     msg.Payload,
			Headers:     msg.Headers,
			RetryCount:  nextRetryCount,
			CreatedAt:   time.Now().UTC(),
		}

		if err := p.repo.SaveDeadLetter(ctx, dlq); err != nil {
			p.logger.ErrorContext(ctx, "failed to save dead letter message", "id", msg.ID, "error", err)
		}

		// Optionally emit to DLQ topic on Kafka
		if p.cfg.DLQTopic != "" {
			dlqKafkaMsg := kafka.Message{
				Topic: p.cfg.DLQTopic,
				Key:   []byte(msg.ID.String()),
				Value: msg.Payload,
				Headers: map[string]string{
					"source_type":  "OUTBOX",
					"source_id":    msg.ID.String(),
					"original_top": topic,
					"error_reason": dlq.ErrorReason,
				},
			}
			_ = p.publisher.Publish(ctx, dlqKafkaMsg)
		}
	} else {
		// Schedule next retry with exponential backoff and jitter
		backoffDuration := p.cfg.Backoff.Calculate(nextRetryCount)
		nextRetryAt := time.Now().UTC().Add(backoffDuration)

		if err := p.repo.MarkFailed(ctx, msg.ID, errMsg, nextRetryAt, false); err != nil {
			p.logger.ErrorContext(ctx, "failed to mark message failed with backoff",
				"id", msg.ID,
				"error", err,
			)
		}
	}
}
