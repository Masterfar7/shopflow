package outbox

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

// Repository defines persistence operations for outbox and dead letter tables.
type Repository interface {
	// SaveMessage persists an outbox record inside the provided database transaction.
	SaveMessage(ctx context.Context, dbtx database.DBTX, msg *OutboxMessage) error

	// LeaseMessages locks and leases a batch of pending/failed messages using SKIP LOCKED.
	LeaseMessages(ctx context.Context, workerID string, batchSize int, leaseDuration time.Duration) ([]*OutboxMessage, error)

	// ExtendLease extends the lease expiration time for currently held messages.
	ExtendLease(ctx context.Context, workerID string, ids []uuid.UUID, leaseDuration time.Duration) error

	// MarkPublished transitions a message to PUBLISHED and clears lease fields.
	MarkPublished(ctx context.Context, id uuid.UUID) error

	// MarkFailed increments retry count, updates status/error, and resets or schedules lease.
	MarkFailed(ctx context.Context, id uuid.UUID, errMsg string, nextRetryAt time.Time, deadLetter bool) error

	// SaveDeadLetter persists an entry into dead_letter_messages table.
	SaveDeadLetter(ctx context.Context, dlq *DeadLetterMessage) error

	// GetMessageByID retrieves a single outbox message.
	GetMessageByID(ctx context.Context, id uuid.UUID) (*OutboxMessage, error)

	// GetDeadLetterMessages retrieves recently recorded dead letter messages.
	GetDeadLetterMessages(ctx context.Context, limit int) ([]DeadLetterMessage, error)

	// GetPendingCount counts pending or failed messages waiting for dispatch.
	GetPendingCount(ctx context.Context) (int64, error)
}

// PostgresRepository implements Repository using pgxpool.Pool.
type PostgresRepository struct {
	pool *pgxpool.Pool
}

// NewPostgresRepository creates a new PostgresRepository.
func NewPostgresRepository(pool *pgxpool.Pool) *PostgresRepository {
	return &PostgresRepository{pool: pool}
}

// SaveMessage inserts a new outbox message using the caller-provided transaction or pool.
func (r *PostgresRepository) SaveMessage(ctx context.Context, dbtx database.DBTX, msg *OutboxMessage) error {
	if dbtx == nil {
		dbtx = r.pool
	}

	if msg.ID == uuid.Nil {
		msg.ID = uuid.New()
	}
	if msg.CreatedAt.IsZero() {
		msg.CreatedAt = time.Now().UTC()
	}
	if msg.Status == "" {
		msg.Status = StatusPending
	}

	headersJSON, err := json.Marshal(msg.Headers)
	if err != nil {
		return fmt.Errorf("outbox: marshal headers: %w", err)
	}

	_, err = dbtx.Exec(ctx, `
		INSERT INTO outbox_messages (
			id, aggregate_type, aggregate_id, event_type, payload,
			headers, status, retry_count, last_error, leased_until,
			leased_by, trace_context, created_at, published_at
		) VALUES (
			$1, $2, $3, $4, $5,
			$6, $7, $8, $9, $10,
			$11, $12, $13, $14
		);
	`,
		msg.ID, msg.AggregateType, msg.AggregateID, msg.EventType, msg.Payload,
		headersJSON, msg.Status, msg.RetryCount, msg.LastError, msg.LeasedUntil,
		msg.LeasedBy, msg.TraceContext, msg.CreatedAt, msg.PublishedAt,
	)
	if err != nil {
		return fmt.Errorf("outbox: insert message: %w", err)
	}

	return nil
}

// LeaseMessages acquires a batch of pending/failed messages using SKIP LOCKED.
func (r *PostgresRepository) LeaseMessages(ctx context.Context, workerID string, batchSize int, leaseDuration time.Duration) ([]*OutboxMessage, error) {
	if batchSize <= 0 {
		batchSize = 10
	}
	if leaseDuration <= 0 {
		leaseDuration = 30 * time.Second
	}

	var leased []*OutboxMessage

	err := database.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id, aggregate_type, aggregate_id, event_type, payload, headers, status, retry_count, leased_until, leased_by, trace_context, created_at
			FROM outbox_messages
			WHERE status IN ('PENDING', 'FAILED') AND (leased_until IS NULL OR leased_until < NOW())
			ORDER BY created_at ASC
			LIMIT $1
			FOR UPDATE SKIP LOCKED;
		`, batchSize)
		if err != nil {
			return fmt.Errorf("query outbox for update: %w", err)
		}
		defer rows.Close()

		var ids []uuid.UUID
		for rows.Next() {
			var msg OutboxMessage
			var headersBytes []byte

			err := rows.Scan(
				&msg.ID, &msg.AggregateType, &msg.AggregateID, &msg.EventType,
				&msg.Payload, &headersBytes, &msg.Status, &msg.RetryCount,
				&msg.LeasedUntil, &msg.LeasedBy, &msg.TraceContext, &msg.CreatedAt,
			)
			if err != nil {
				return fmt.Errorf("scan outbox row: %w", err)
			}

			if len(headersBytes) > 0 {
				_ = json.Unmarshal(headersBytes, &msg.Headers)
			}

			ids = append(ids, msg.ID)
			leased = append(leased, &msg)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate outbox rows: %w", err)
		}

		if len(ids) == 0 {
			return nil
		}

		now := time.Now().UTC()
		expires := now.Add(leaseDuration)

		_, err = tx.Exec(ctx, `
			UPDATE outbox_messages
			SET leased_until = $1, leased_by = $2
			WHERE id = ANY($3);
		`, expires, workerID, ids)
		if err != nil {
			return fmt.Errorf("update leased rows: %w", err)
		}

		for _, m := range leased {
			m.LeasedUntil = &expires
			m.LeasedBy = &workerID
		}

		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("outbox: lease messages: %w", err)
	}

	return leased, nil
}

// ExtendLease extends lease expiration for given message IDs.
func (r *PostgresRepository) ExtendLease(ctx context.Context, workerID string, ids []uuid.UUID, leaseDuration time.Duration) error {
	if len(ids) == 0 {
		return nil
	}
	expires := time.Now().UTC().Add(leaseDuration)

	_, err := r.pool.Exec(ctx, `
		UPDATE outbox_messages
		SET leased_until = $1
		WHERE id = ANY($2) AND leased_by = $3 AND status IN ('PENDING', 'FAILED');
	`, expires, ids, workerID)
	if err != nil {
		return fmt.Errorf("outbox: extend lease: %w", err)
	}
	return nil
}

// MarkPublished marks message as published and clears lease.
func (r *PostgresRepository) MarkPublished(ctx context.Context, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE outbox_messages
		SET status = 'PUBLISHED', published_at = NOW(), leased_until = NULL, leased_by = NULL
		WHERE id = $1;
	`, id)
	if err != nil {
		return fmt.Errorf("outbox: mark published: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrMessageNotFound
	}
	return nil
}

// MarkFailed records an attempt failure, incrementing retry count and scheduling next retry or dead letter.
func (r *PostgresRepository) MarkFailed(ctx context.Context, id uuid.UUID, errMsg string, nextRetryAt time.Time, deadLetter bool) error {
	var err error
	if deadLetter {
		_, err = r.pool.Exec(ctx, `
			UPDATE outbox_messages
			SET status = 'DEAD_LETTER', last_error = $2, leased_until = NULL, leased_by = NULL, retry_count = retry_count + 1
			WHERE id = $1;
		`, id, errMsg)
	} else {
		_, err = r.pool.Exec(ctx, `
			UPDATE outbox_messages
			SET status = 'FAILED', last_error = $2, leased_until = $3, leased_by = NULL, retry_count = retry_count + 1
			WHERE id = $1;
		`, id, errMsg, nextRetryAt)
	}
	if err != nil {
		return fmt.Errorf("outbox: mark failed: %w", err)
	}
	return nil
}

// SaveDeadLetter writes a poisoned/exhausted event to the dead_letter_messages table.
func (r *PostgresRepository) SaveDeadLetter(ctx context.Context, dlq *DeadLetterMessage) error {
	if dlq.ID == uuid.Nil {
		dlq.ID = uuid.New()
	}
	if dlq.CreatedAt.IsZero() {
		dlq.CreatedAt = time.Now().UTC()
	}

	headersJSON, err := json.Marshal(dlq.Headers)
	if err != nil {
		return fmt.Errorf("outbox: marshal dlq headers: %w", err)
	}

	_, err = r.pool.Exec(ctx, `
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
		return fmt.Errorf("outbox: save dead letter: %w", err)
	}
	return nil
}

// GetMessageByID retrieves an outbox message by ID.
func (r *PostgresRepository) GetMessageByID(ctx context.Context, id uuid.UUID) (*OutboxMessage, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, aggregate_type, aggregate_id, event_type, payload, headers,
		       status, retry_count, last_error, leased_until, leased_by,
		       trace_context, created_at, published_at
		FROM outbox_messages
		WHERE id = $1;
	`, id)

	var msg OutboxMessage
	var headersBytes []byte

	err := row.Scan(
		&msg.ID, &msg.AggregateType, &msg.AggregateID, &msg.EventType,
		&msg.Payload, &headersBytes, &msg.Status, &msg.RetryCount,
		&msg.LastError, &msg.LeasedUntil, &msg.LeasedBy, &msg.TraceContext,
		&msg.CreatedAt, &msg.PublishedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrMessageNotFound
		}
		return nil, fmt.Errorf("outbox: get message: %w", err)
	}

	if len(headersBytes) > 0 {
		_ = json.Unmarshal(headersBytes, &msg.Headers)
	}

	return &msg, nil
}

// GetDeadLetterMessages retrieves dead letter messages ordered by created_at desc.
func (r *PostgresRepository) GetDeadLetterMessages(ctx context.Context, limit int) ([]DeadLetterMessage, error) {
	if limit <= 0 {
		limit = 50
	}

	rows, err := r.pool.Query(ctx, `
		SELECT id, source_type, source_id, topic, partition, offset_val, error_reason, payload, headers, retry_count, created_at
		FROM dead_letter_messages
		ORDER BY created_at DESC
		LIMIT $1;
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("outbox: get dlq messages: %w", err)
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

// GetPendingCount returns the count of messages in PENDING or FAILED status.
func (r *PostgresRepository) GetPendingCount(ctx context.Context) (int64, error) {
	var count int64
	err := r.pool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM outbox_messages
		WHERE status IN ('PENDING', 'FAILED');
	`).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("outbox: get pending count: %w", err)
	}
	return count, nil
}
