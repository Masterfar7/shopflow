package inbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"shopflow/internal/platform/database"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Repository defines inbox storage and deduplication queries.
type Repository interface {
	// TryStartProcessing attempts atomic insertion with PRIMARY KEY (message_id, consumer_group).
	// Returns true if message was newly inserted and can be processed; false if already completed/duplicate.
	TryStartProcessing(ctx context.Context, dbtx database.DBTX, msg *InboxMessage) (bool, error)

	// MarkCompleted transitions status to COMPLETED.
	MarkCompleted(ctx context.Context, dbtx database.DBTX, messageID, consumerGroup string) error

	// MarkFailed transitions status to FAILED.
	MarkFailed(ctx context.Context, dbtx database.DBTX, messageID, consumerGroup string) error

	// SaveDeadLetter writes to dead_letter_messages.
	SaveDeadLetter(ctx context.Context, dbtx database.DBTX, dlq *DeadLetterMessage) error

	// GetMessage retrieves an inbox record.
	GetMessage(ctx context.Context, messageID, consumerGroup string) (*InboxMessage, error)

	// GetDeadLetterMessages retrieves dead letter records for inspection.
	GetDeadLetterMessages(ctx context.Context, limit int) ([]DeadLetterMessage, error)
}

// PostgresRepository implements Repository using pgxpool.Pool.
type PostgresRepository struct {
	pool *pgxpool.Pool
}

// NewPostgresRepository creates a PostgresRepository.
func NewPostgresRepository(pool *pgxpool.Pool) *PostgresRepository {
	return &PostgresRepository{pool: pool}
}

func (r *PostgresRepository) getDB(dbtx database.DBTX) database.DBTX {
	if dbtx != nil {
		return dbtx
	}
	return r.pool
}

// TryStartProcessing performs atomic insert with ON CONFLICT DO NOTHING.
func (r *PostgresRepository) TryStartProcessing(ctx context.Context, dbtx database.DBTX, msg *InboxMessage) (bool, error) {
	db := r.getDB(dbtx)

	if msg.Status == "" {
		msg.Status = StatusProcessing
	}

	tag, err := db.Exec(ctx, `
		INSERT INTO inbox_messages (message_id, consumer_group, event_type, payload, status, processed_at)
		VALUES ($1, $2, $3, $4, $5, NOW())
		ON CONFLICT (message_id, consumer_group) DO NOTHING;
	`, msg.MessageID, msg.ConsumerGroup, msg.EventType, msg.Payload, msg.Status)
	if err != nil {
		return false, fmt.Errorf("inbox: try start processing insert: %w", err)
	}

	if tag.RowsAffected() > 0 {
		return true, nil
	}

	// Conflict occurred: check current status
	var currentStatus InboxStatus
	err = db.QueryRow(ctx, `
		SELECT status
		FROM inbox_messages
		WHERE message_id = $1 AND consumer_group = $2;
	`, msg.MessageID, msg.ConsumerGroup).Scan(&currentStatus)
	if err != nil {
		return false, fmt.Errorf("inbox: query conflict status: %w", err)
	}

	if currentStatus == StatusCompleted {
		// Already processed to completion: safely skip idempotently
		return false, nil
	}

	if currentStatus == StatusFailed {
		// Previously failed: reset to PROCESSING and allow redelivery
		_, err := db.Exec(ctx, `
			UPDATE inbox_messages
			SET status = $1, processed_at = NOW()
			WHERE message_id = $2 AND consumer_group = $3;
		`, StatusProcessing, msg.MessageID, msg.ConsumerGroup)
		if err != nil {
			return false, fmt.Errorf("inbox: update failed status to processing: %w", err)
		}
		msg.Status = StatusProcessing
		return true, nil
	}

	if currentStatus == StatusProcessing {
		return false, ErrDuplicateMessage
	}

	return false, ErrDuplicateMessage
}

// MarkCompleted updates status to COMPLETED.
func (r *PostgresRepository) MarkCompleted(ctx context.Context, dbtx database.DBTX, messageID, consumerGroup string) error {
	db := r.getDB(dbtx)

	_, err := db.Exec(ctx, `
		UPDATE inbox_messages
		SET status = 'COMPLETED', processed_at = NOW()
		WHERE message_id = $1 AND consumer_group = $2;
	`, messageID, consumerGroup)
	if err != nil {
		return fmt.Errorf("inbox: mark completed: %w", err)
	}
	return nil
}

// MarkFailed updates status to FAILED.
func (r *PostgresRepository) MarkFailed(ctx context.Context, dbtx database.DBTX, messageID, consumerGroup string) error {
	db := r.getDB(dbtx)

	_, err := db.Exec(ctx, `
		UPDATE inbox_messages
		SET status = 'FAILED', processed_at = NOW()
		WHERE message_id = $1 AND consumer_group = $2;
	`, messageID, consumerGroup)
	if err != nil {
		return fmt.Errorf("inbox: mark failed: %w", err)
	}
	return nil
}

// SaveDeadLetter writes to dead_letter_messages.
func (r *PostgresRepository) SaveDeadLetter(ctx context.Context, dbtx database.DBTX, dlq *DeadLetterMessage) error {
	db := r.getDB(dbtx)

	if dlq.ID == uuid.Nil {
		dlq.ID = uuid.New()
	}
	if dlq.CreatedAt.IsZero() {
		dlq.CreatedAt = time.Now().UTC()
	}

	headersJSON, err := json.Marshal(dlq.Headers)
	if err != nil {
		return fmt.Errorf("inbox: marshal dlq headers: %w", err)
	}

	_, err = db.Exec(ctx, `
		INSERT INTO dead_letter_messages (
			id, source_type, source_id, topic, partition,
			offset_val, error_reason, payload, headers, retry_count, created_at
		) VALUES (
			$1, $2, $3, $4, $5,
			$6, $7, $8, $9, $10, $11
		);
	`,
		dlq.ID, dlq.SourceType, dlq.SourceID, dlq.Topic, dlq.Partition,
		dlq.OffsetVal, dlq.ErrorReason, dlq.Payload, headersJSON, dlq.RetryCount, dlq.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("inbox: save dead letter: %w", err)
	}
	return nil
}

// GetMessage retrieves an inbox record.
func (r *PostgresRepository) GetMessage(ctx context.Context, messageID, consumerGroup string) (*InboxMessage, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT message_id, consumer_group, event_type, payload, status, processed_at
		FROM inbox_messages
		WHERE message_id = $1 AND consumer_group = $2;
	`, messageID, consumerGroup)

	var msg InboxMessage
	if err := row.Scan(&msg.MessageID, &msg.ConsumerGroup, &msg.EventType, &msg.Payload, &msg.Status, &msg.ProcessedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrMessageNotFound
		}
		return nil, fmt.Errorf("inbox: get message: %w", err)
	}
	return &msg, nil
}

// GetDeadLetterMessages retrieves recent dead letters.
func (r *PostgresRepository) GetDeadLetterMessages(ctx context.Context, limit int) ([]DeadLetterMessage, error) {
	if limit <= 0 {
		limit = 50
	}

	rows, err := r.pool.Query(ctx, `
		SELECT id, source_type, source_id, topic, partition, offset_val, error_reason, payload, headers, retry_count, created_at
		FROM dead_letter_messages
		WHERE source_type = 'INBOX'
		ORDER BY created_at DESC
		LIMIT $1;
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("inbox: get dlq messages: %w", err)
	}
	defer rows.Close()

	var result []DeadLetterMessage
	for rows.Next() {
		var dlq DeadLetterMessage
		var headersBytes []byte

		err := rows.Scan(
			&dlq.ID, &dlq.SourceType, &dlq.SourceID, &dlq.Topic,
			&dlq.Partition, &dlq.OffsetVal, &dlq.ErrorReason,
			&dlq.Payload, &headersBytes, &dlq.RetryCount, &dlq.CreatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("scan dlq row: %w", err)
		}
		if len(headersBytes) > 0 {
			_ = json.Unmarshal(headersBytes, &dlq.Headers)
		}
		result = append(result, dlq)
	}

	return result, rows.Err()
}
