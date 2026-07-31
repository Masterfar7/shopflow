package saga

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"shopflow/internal/platform/database"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Repository defines all database operations for order_sagas and order_saga_logs.
type Repository interface {
	CreateSaga(ctx context.Context, saga *OrderSaga) error
	CreateSagaTx(ctx context.Context, tx pgx.Tx, saga *OrderSaga) error
	GetSagaByID(ctx context.Context, sagaID uuid.UUID) (*OrderSaga, error)
	GetSagaByOrderID(ctx context.Context, orderID uuid.UUID) (*OrderSaga, error)
	GetSagaForUpdate(ctx context.Context, tx pgx.Tx, sagaID uuid.UUID) (*OrderSaga, error)
	GetSagaByOrderIDForUpdate(ctx context.Context, tx pgx.Tx, orderID uuid.UUID) (*OrderSaga, error)
	TransitionState(ctx context.Context, tx pgx.Tx, sagaID uuid.UUID, fromState, toState SagaState, payload SagaPayload, nextTimeout time.Time) error
	AppendLog(ctx context.Context, tx pgx.Tx, log *OrderSagaLog) error
	GetLogs(ctx context.Context, sagaID uuid.UUID) ([]OrderSagaLog, error)
	FindTimedOutSagas(ctx context.Context, limit int) ([]*OrderSaga, error)
	FindTimedOutSagasTx(ctx context.Context, tx pgx.Tx, limit int) ([]*OrderSaga, error)
}

// PostgresRepository implements Repository using pgxpool.
type PostgresRepository struct {
	pool *pgxpool.Pool
}

// NewPostgresRepository instantiates a PostgresRepository.
func NewPostgresRepository(pool *pgxpool.Pool) *PostgresRepository {
	return &PostgresRepository{pool: pool}
}

func (r *PostgresRepository) CreateSaga(ctx context.Context, saga *OrderSaga) error {
	return database.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		return r.CreateSagaTx(ctx, tx, saga)
	})
}

func (r *PostgresRepository) CreateSagaTx(ctx context.Context, tx pgx.Tx, saga *OrderSaga) error {
	if saga.SagaID == uuid.Nil {
		saga.SagaID = uuid.New()
	}
	if saga.CurrentState == "" {
		saga.CurrentState = StateStarted
	}
	if saga.TimeoutAt.IsZero() {
		saga.TimeoutAt = time.Now().Add(60 * time.Second)
	}

	payloadJSON, err := json.Marshal(saga.Payload)
	if err != nil {
		return fmt.Errorf("marshal saga payload: %w", err)
	}

	query := `
		INSERT INTO order_sagas (
			saga_id, order_id, current_state, payload, retry_count, timeout_at, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, NOW(), NOW())
		RETURNING created_at, updated_at;
	`
	err = tx.QueryRow(ctx, query,
		saga.SagaID, saga.OrderID, string(saga.CurrentState), payloadJSON, saga.RetryCount, saga.TimeoutAt,
	).Scan(&saga.CreatedAt, &saga.UpdatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return ErrSagaAlreadyExists
		}
		return fmt.Errorf("insert order_saga: %w", err)
	}

	// Insert initial transition log
	initLog := &OrderSagaLog{
		SagaID:    saga.SagaID,
		FromState: "",
		ToState:   saga.CurrentState,
		EventType: "SagaInitialized",
		EventID:   uuid.New().String(),
	}
	return r.AppendLog(ctx, tx, initLog)
}

func (r *PostgresRepository) GetSagaByID(ctx context.Context, sagaID uuid.UUID) (*OrderSaga, error) {
	query := `
		SELECT saga_id, order_id, current_state, payload, retry_count, timeout_at, created_at, updated_at
		FROM order_sagas
		WHERE saga_id = $1;
	`
	return r.scanSagaRow(r.pool.QueryRow(ctx, query, sagaID))
}

func (r *PostgresRepository) GetSagaByOrderID(ctx context.Context, orderID uuid.UUID) (*OrderSaga, error) {
	query := `
		SELECT saga_id, order_id, current_state, payload, retry_count, timeout_at, created_at, updated_at
		FROM order_sagas
		WHERE order_id = $1;
	`
	return r.scanSagaRow(r.pool.QueryRow(ctx, query, orderID))
}

func (r *PostgresRepository) GetSagaForUpdate(ctx context.Context, tx pgx.Tx, sagaID uuid.UUID) (*OrderSaga, error) {
	query := `
		SELECT saga_id, order_id, current_state, payload, retry_count, timeout_at, created_at, updated_at
		FROM order_sagas
		WHERE saga_id = $1
		FOR UPDATE;
	`
	return r.scanSagaRow(tx.QueryRow(ctx, query, sagaID))
}

func (r *PostgresRepository) GetSagaByOrderIDForUpdate(ctx context.Context, tx pgx.Tx, orderID uuid.UUID) (*OrderSaga, error) {
	query := `
		SELECT saga_id, order_id, current_state, payload, retry_count, timeout_at, created_at, updated_at
		FROM order_sagas
		WHERE order_id = $1
		FOR UPDATE;
	`
	return r.scanSagaRow(tx.QueryRow(ctx, query, orderID))
}

func (r *PostgresRepository) TransitionState(
	ctx context.Context,
	tx pgx.Tx,
	sagaID uuid.UUID,
	fromState, toState SagaState,
	payload SagaPayload,
	nextTimeout time.Time,
) error {
	if !CanTransition(fromState, toState) {
		return fmt.Errorf("%w: cannot transition from %s to %s", ErrInvalidStateTransition, fromState, toState)
	}

	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}

	if nextTimeout.IsZero() {
		if IsTerminal(toState) {
			nextTimeout = time.Now().Add(24 * time.Hour)
		} else {
			nextTimeout = time.Now().Add(60 * time.Second)
		}
	}

	query := `
		UPDATE order_sagas
		SET current_state = $1,
		    payload = $2,
		    timeout_at = $3,
		    updated_at = NOW()
		WHERE saga_id = $4 AND current_state = $5
		RETURNING updated_at;
	`
	var updatedAt time.Time
	err = tx.QueryRow(ctx, query, string(toState), payloadJSON, nextTimeout, sagaID, string(fromState)).Scan(&updatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Determine whether saga doesn't exist or concurrent modification occurred
			var current string
			checkErr := tx.QueryRow(ctx, `SELECT current_state FROM order_sagas WHERE saga_id = $1`, sagaID).Scan(&current)
			if errors.Is(checkErr, pgx.ErrNoRows) {
				return ErrSagaNotFound
			}
			if IsTerminal(SagaState(current)) {
				return ErrSagaAlreadyTerminal
			}
			return fmt.Errorf("%w: state changed from %s to %s", ErrOptimisticLockConflict, fromState, current)
		}
		return fmt.Errorf("update saga state: %w", err)
	}

	return nil
}

func (r *PostgresRepository) AppendLog(ctx context.Context, tx pgx.Tx, log *OrderSagaLog) error {
	query := `
		INSERT INTO order_saga_logs (
			saga_id, from_state, to_state, event_type, event_id, error_detail, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, NOW())
		RETURNING id, created_at;
	`
	return tx.QueryRow(ctx, query,
		log.SagaID, string(log.FromState), string(log.ToState), log.EventType, log.EventID, log.ErrorDetail,
	).Scan(&log.ID, &log.CreatedAt)
}

func (r *PostgresRepository) GetLogs(ctx context.Context, sagaID uuid.UUID) ([]OrderSagaLog, error) {
	query := `
		SELECT id, saga_id, from_state, to_state, event_type, event_id, error_detail, created_at
		FROM order_saga_logs
		WHERE saga_id = $1
		ORDER BY created_at ASC, id ASC;
	`
	rows, err := r.pool.Query(ctx, query, sagaID)
	if err != nil {
		return nil, fmt.Errorf("query saga logs: %w", err)
	}
	defer rows.Close()

	var logs []OrderSagaLog
	for rows.Next() {
		var l OrderSagaLog
		var fromStr, toStr string
		if err := rows.Scan(
			&l.ID, &l.SagaID, &fromStr, &toStr, &l.EventType, &l.EventID, &l.ErrorDetail, &l.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan saga log: %w", err)
		}
		l.FromState = SagaState(fromStr)
		l.ToState = SagaState(toStr)
		logs = append(logs, l)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate saga logs: %w", err)
	}
	return logs, nil
}

func (r *PostgresRepository) FindTimedOutSagas(ctx context.Context, limit int) ([]*OrderSaga, error) {
	if limit <= 0 {
		limit = 20
	}
	query := `
		SELECT saga_id, order_id, current_state, payload, retry_count, timeout_at, created_at, updated_at
		FROM order_sagas
		WHERE timeout_at <= NOW()
		  AND current_state NOT IN ('CONFIRMED', 'FAILED', 'FAILED_COMPENSATED')
		ORDER BY timeout_at ASC
		LIMIT $1;
	`
	rows, err := r.pool.Query(ctx, query, limit)
	if err != nil {
		return nil, fmt.Errorf("query timed out sagas: %w", err)
	}
	defer rows.Close()

	var sagas []*OrderSaga
	for rows.Next() {
		s, err := r.scanSagaFromRows(rows)
		if err != nil {
			return nil, err
		}
		sagas = append(sagas, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate timed out sagas: %w", err)
	}
	return sagas, nil
}

func (r *PostgresRepository) FindTimedOutSagasTx(ctx context.Context, tx pgx.Tx, limit int) ([]*OrderSaga, error) {
	if limit <= 0 {
		limit = 20
	}
	query := `
		SELECT saga_id, order_id, current_state, payload, retry_count, timeout_at, created_at, updated_at
		FROM order_sagas
		WHERE timeout_at <= NOW()
		  AND current_state NOT IN ('CONFIRMED', 'FAILED', 'FAILED_COMPENSATED')
		ORDER BY timeout_at ASC
		LIMIT $1
		FOR UPDATE SKIP LOCKED;
	`
	rows, err := tx.Query(ctx, query, limit)
	if err != nil {
		return nil, fmt.Errorf("query timed out sagas tx: %w", err)
	}
	defer rows.Close()

	var sagas []*OrderSaga
	for rows.Next() {
		s, err := r.scanSagaFromRows(rows)
		if err != nil {
			return nil, err
		}
		sagas = append(sagas, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate timed out sagas tx: %w", err)
	}
	return sagas, nil
}

func (r *PostgresRepository) scanSagaRow(row pgx.Row) (*OrderSaga, error) {
	s := &OrderSaga{}
	var stateStr string
	var payloadData []byte
	err := row.Scan(
		&s.SagaID, &s.OrderID, &stateStr, &payloadData, &s.RetryCount, &s.TimeoutAt, &s.CreatedAt, &s.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrSagaNotFound
		}
		return nil, fmt.Errorf("scan saga row: %w", err)
	}
	s.CurrentState = SagaState(stateStr)
	if err := json.Unmarshal(payloadData, &s.Payload); err != nil {
		return nil, fmt.Errorf("unmarshal saga payload: %w", err)
	}
	return s, nil
}

func (r *PostgresRepository) scanSagaFromRows(rows pgx.Rows) (*OrderSaga, error) {
	s := &OrderSaga{}
	var stateStr string
	var payloadData []byte
	err := rows.Scan(
		&s.SagaID, &s.OrderID, &stateStr, &payloadData, &s.RetryCount, &s.TimeoutAt, &s.CreatedAt, &s.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("scan saga rows: %w", err)
	}
	s.CurrentState = SagaState(stateStr)
	if err := json.Unmarshal(payloadData, &s.Payload); err != nil {
		return nil, fmt.Errorf("unmarshal saga payload: %w", err)
	}
	return s, nil
}
