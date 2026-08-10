package payment

import (
	"context"
	"errors"
	"fmt"
	"time"

	"shopflow/internal/platform/database"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Repository defines database operations for the Payment bounded context.
type Repository interface {
	CreatePayment(ctx context.Context, p *Payment) error
	GetPaymentByID(ctx context.Context, id uuid.UUID) (*Payment, error)
	GetPaymentByIdempotency(ctx context.Context, userID uuid.UUID, key string) (*Payment, error)
	UpdatePaymentStatus(ctx context.Context, id uuid.UUID, status PaymentStatus, errorCode, failureReason *string) error
	TransitionPaymentStatus(ctx context.Context, id uuid.UUID, fromStatus, toStatus PaymentStatus) error
	CreateRefund(ctx context.Context, refund *Refund) error
	GetRefundsByPaymentID(ctx context.Context, paymentID uuid.UUID) ([]Refund, error)
}

// PostgresRepository implements Repository using PostgreSQL.
type PostgresRepository struct {
	pool database.DBTX
}

// NewPostgresRepository creates a PostgresRepository with a connection pool.
func NewPostgresRepository(pool *pgxpool.Pool) *PostgresRepository {
	return &PostgresRepository{pool: pool}
}

// NewPostgresRepositoryWithDBTX creates a PostgresRepository with abstract DBTX.
func NewPostgresRepositoryWithDBTX(dbtx database.DBTX) *PostgresRepository {
	return &PostgresRepository{pool: dbtx}
}

// CreatePayment inserts a new payment record into the payments table.
func (r *PostgresRepository) CreatePayment(ctx context.Context, p *Payment) error {
	if err := p.Validate(); err != nil {
		return err
	}

	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now().UTC()
	}
	if p.UpdatedAt.IsZero() {
		p.UpdatedAt = p.CreatedAt
	}
	if p.Provider == "" {
		p.Provider = "SIMULATED"
	}

	query := `
		INSERT INTO payments (
			id, order_id, user_id, idempotency_key, amount_minor, currency,
			provider, status, error_code, failure_reason, created_at, updated_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12
		);
	`

	_, err := r.pool.Exec(
		ctx, query,
		p.ID, p.OrderID, p.UserID, p.IdempotencyKey, p.AmountMinor, p.Currency,
		p.Provider, string(p.Status), p.ErrorCode, p.FailureReason, p.CreatedAt, p.UpdatedAt,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return ErrIdempotencyConflict
		}
		return fmt.Errorf("failed to insert payment: %w", err)
	}

	return nil
}

// GetPaymentByID retrieves a payment by its primary key UUID.
func (r *PostgresRepository) GetPaymentByID(ctx context.Context, id uuid.UUID) (*Payment, error) {
	query := `
		SELECT id, order_id, user_id, idempotency_key, amount_minor, currency,
		       provider, status, error_code, failure_reason, created_at, updated_at
		FROM payments
		WHERE id = $1;
	`

	row := r.pool.QueryRow(ctx, query, id)
	return r.scanPayment(row)
}

// GetPaymentByIdempotency retrieves a payment by user ID and idempotency key.
func (r *PostgresRepository) GetPaymentByIdempotency(ctx context.Context, userID uuid.UUID, key string) (*Payment, error) {
	query := `
		SELECT id, order_id, user_id, idempotency_key, amount_minor, currency,
		       provider, status, error_code, failure_reason, created_at, updated_at
		FROM payments
		WHERE user_id = $1 AND idempotency_key = $2;
	`

	row := r.pool.QueryRow(ctx, query, userID, key)
	return r.scanPayment(row)
}

// UpdatePaymentStatus transitions a payment status and updates error details.
func (r *PostgresRepository) UpdatePaymentStatus(ctx context.Context, id uuid.UUID, status PaymentStatus, errorCode, failureReason *string) error {
	query := `
		UPDATE payments
		SET status = $2, error_code = $3, failure_reason = $4, updated_at = NOW()
		WHERE id = $1;
	`

	cmd, err := r.pool.Exec(ctx, query, id, string(status), errorCode, failureReason)
	if err != nil {
		return fmt.Errorf("failed to update payment status: %w", err)
	}
	if cmd.RowsAffected() == 0 {
		return ErrPaymentNotFound
	}
	return nil
}

// TransitionPaymentStatus atomically transitions payment status from fromStatus to toStatus.
func (r *PostgresRepository) TransitionPaymentStatus(ctx context.Context, id uuid.UUID, fromStatus, toStatus PaymentStatus) error {
	query := `
		UPDATE payments
		SET status = $2, updated_at = NOW()
		WHERE id = $1 AND status = $3;
	`

	cmd, err := r.pool.Exec(ctx, query, id, string(toStatus), string(fromStatus))
	if err != nil {
		return fmt.Errorf("failed to transition payment status: %w", err)
	}
	if cmd.RowsAffected() == 0 {
		current, err := r.GetPaymentByID(ctx, id)
		if err != nil {
			return err
		}
		if current.Status == PaymentStatusRefunded {
			return ErrPaymentAlreadyRefunded
		}
		if current.Status != PaymentStatusSuccess {
			return ErrPaymentCannotBeRefunded
		}
		return ErrPaymentCannotBeRefunded
	}
	return nil
}

// CreateRefund inserts a refund record into payment_refunds.
func (r *PostgresRepository) CreateRefund(ctx context.Context, refund *Refund) error {
	if refund.ID == uuid.Nil {
		refund.ID = uuid.New()
	}
	if refund.CreatedAt.IsZero() {
		refund.CreatedAt = time.Now().UTC()
	}
	if refund.UpdatedAt.IsZero() {
		refund.UpdatedAt = refund.CreatedAt
	}
	if refund.Status == "" {
		refund.Status = RefundStatusSuccess
	}

	query := `
		INSERT INTO payment_refunds (
			id, payment_id, order_id, amount_minor, reason, status, error_code, created_at, updated_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9
		);
	`

	_, err := r.pool.Exec(
		ctx, query,
		refund.ID, refund.PaymentID, refund.OrderID, refund.AmountMinor,
		refund.Reason, string(refund.Status), refund.ErrorCode, refund.CreatedAt, refund.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("failed to insert payment refund: %w", err)
	}
	return nil
}

// GetRefundsByPaymentID fetches all refund records for a payment.
func (r *PostgresRepository) GetRefundsByPaymentID(ctx context.Context, paymentID uuid.UUID) ([]Refund, error) {
	query := `
		SELECT id, payment_id, order_id, amount_minor, reason, status, error_code, created_at, updated_at
		FROM payment_refunds
		WHERE payment_id = $1
		ORDER BY created_at ASC;
	`

	rows, err := r.pool.Query(ctx, query, paymentID)
	if err != nil {
		return nil, fmt.Errorf("failed to query payment refunds: %w", err)
	}
	defer rows.Close()

	var refunds []Refund
	for rows.Next() {
		var ref Refund
		var statusStr string
		err := rows.Scan(
			&ref.ID, &ref.PaymentID, &ref.OrderID, &ref.AmountMinor,
			&ref.Reason, &statusStr, &ref.ErrorCode, &ref.CreatedAt, &ref.UpdatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan payment refund: %w", err)
		}
		ref.Status = RefundStatus(statusStr)
		refunds = append(refunds, ref)
	}

	return refunds, rows.Err()
}

func (r *PostgresRepository) scanPayment(row pgx.Row) (*Payment, error) {
	var p Payment
	var statusStr string

	err := row.Scan(
		&p.ID, &p.OrderID, &p.UserID, &p.IdempotencyKey, &p.AmountMinor, &p.Currency,
		&p.Provider, &statusStr, &p.ErrorCode, &p.FailureReason, &p.CreatedAt, &p.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrPaymentNotFound
		}
		return nil, fmt.Errorf("failed to scan payment: %w", err)
	}

	p.Status = PaymentStatus(statusStr)
	return &p, nil
}
