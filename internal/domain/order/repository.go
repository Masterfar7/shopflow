package order

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

// Repository defines all persistence operations for the Order domain.
type Repository interface {
	// Idempotency methods
	LockIdempotencyKey(ctx context.Context, tx pgx.Tx, key string, userID uuid.UUID, reqHash string, ttl time.Duration) (*IdempotencyKeyRecord, bool, error)
	CompleteIdempotencyKey(ctx context.Context, tx pgx.Tx, key string, userID uuid.UUID, statusCode int, responseBody []byte, orderID uuid.UUID) error
	GetIdempotencyKey(ctx context.Context, key string, userID uuid.UUID) (*IdempotencyKeyRecord, error)
	SaveIdempotencyKey(ctx context.Context, tx pgx.Tx, rec *IdempotencyKeyRecord) error
	UpdateIdempotencyKey(ctx context.Context, tx pgx.Tx, rec *IdempotencyKeyRecord) error

	// Order methods
	CreateOrder(ctx context.Context, tx pgx.Tx, o *Order) error
	CreateOrderItems(ctx context.Context, tx pgx.Tx, items []OrderItem) error
	CreateOrderWithItemsAndOutbox(ctx context.Context, tx pgx.Tx, o *Order, outboxPayload any) error
	GetOrderByID(ctx context.Context, id uuid.UUID) (*Order, error)
	GetOrderByUserAndIdempotencyKey(ctx context.Context, userID uuid.UUID, key string) (*Order, error)
	GetOrderItems(ctx context.Context, orderID uuid.UUID) ([]OrderItem, error)
	ListOrders(ctx context.Context, params ListOrdersParams) ([]Order, string, bool, error)
	UpdateOrderStatus(ctx context.Context, tx pgx.Tx, orderID uuid.UUID, fromStatus, toStatus OrderStatus, expectedVersion int64) (int64, error)

	// Outbox integration
	InsertOutboxMessage(ctx context.Context, tx pgx.Tx, aggregateID uuid.UUID, eventType string, payload any) error
}

type PostgresRepository struct {
	pool database.DBTX
}

func NewPostgresRepository(pool *pgxpool.Pool) *PostgresRepository {
	return &PostgresRepository{pool: pool}
}

func NewPostgresRepositoryWithDBTX(dbtx database.DBTX) *PostgresRepository {
	return &PostgresRepository{pool: dbtx}
}

// LockIdempotencyKey attempts to acquire or verify an idempotency key.
// Returns (record, isNew, error). If isNew == true, caller proceeds with execution.
// If isNew == false and record.Status == COMPLETED, caller returns cached response.
func (r *PostgresRepository) LockIdempotencyKey(ctx context.Context, tx pgx.Tx, key string, userID uuid.UUID, reqHash string, ttl time.Duration) (*IdempotencyKeyRecord, bool, error) {
	insertQuery := `
		INSERT INTO idempotency_keys (key, user_id, request_hash, status, created_at, expires_at)
		VALUES ($1, $2, $3, 'PROCESSING', NOW(), NOW() + $4::interval)
		ON CONFLICT (user_id, key) DO UPDATE
		SET request_hash = EXCLUDED.request_hash,
		    status = 'PROCESSING',
		    response_code = NULL,
		    response_body = NULL,
		    order_id = NULL,
		    created_at = NOW(),
		    expires_at = EXCLUDED.expires_at
		WHERE idempotency_keys.expires_at < NOW()
		RETURNING key, user_id, request_hash, status, response_code, response_body, order_id, created_at, expires_at;
	`
	rec := &IdempotencyKeyRecord{}
	var statusStr string
	ttlSeconds := int(ttl.Seconds())
	if ttlSeconds <= 0 {
		ttlSeconds = 86400
	}
	ttlStr := fmt.Sprintf("%d seconds", ttlSeconds)

	err := tx.QueryRow(ctx, insertQuery, key, userID, reqHash, ttlStr).Scan(
		&rec.Key, &rec.UserID, &rec.RequestHash, &statusStr, &rec.ResponseCode,
		&rec.ResponseBody, &rec.OrderID, &rec.CreatedAt, &rec.ExpiresAt,
	)
	if err == nil {
		rec.Status = IdempotencyStatus(statusStr)
		return rec, true, nil
	}

	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, false, fmt.Errorf("lock idempotency key insert: %w", err)
	}

	// Key already exists and is NOT expired: lock row with FOR UPDATE
	selectQuery := `
		SELECT key, user_id, request_hash, status, response_code, response_body, order_id, created_at, expires_at
		FROM idempotency_keys
		WHERE user_id = $1 AND key = $2
		FOR UPDATE;
	`
	err = tx.QueryRow(ctx, selectQuery, userID, key).Scan(
		&rec.Key, &rec.UserID, &rec.RequestHash, &statusStr, &rec.ResponseCode,
		&rec.ResponseBody, &rec.OrderID, &rec.CreatedAt, &rec.ExpiresAt,
	)
	if err != nil {
		return nil, false, fmt.Errorf("lock idempotency key select: %w", err)
	}
	rec.Status = IdempotencyStatus(statusStr)

	// Validate request hash
	if rec.RequestHash != reqHash {
		return nil, false, ErrIdempotencyConflict
	}

	if rec.Status == IdempotencyStatusProcessing {
		return rec, false, ErrConcurrentProcessing
	}

	if rec.Status == IdempotencyStatusFailed {
		// Allow retry for failed previous attempts
		updateQuery := `
			UPDATE idempotency_keys
			SET status = 'PROCESSING', created_at = NOW(), expires_at = NOW() + $1::interval
			WHERE user_id = $2 AND key = $3
			RETURNING key, user_id, request_hash, status, response_code, response_body, order_id, created_at, expires_at;
		`
		err = tx.QueryRow(ctx, updateQuery, ttlStr, userID, key).Scan(
			&rec.Key, &rec.UserID, &rec.RequestHash, &statusStr, &rec.ResponseCode,
			&rec.ResponseBody, &rec.OrderID, &rec.CreatedAt, &rec.ExpiresAt,
		)
		if err != nil {
			return nil, false, fmt.Errorf("retry idempotency key update: %w", err)
		}
		rec.Status = IdempotencyStatus(statusStr)
		return rec, true, nil
	}

	return rec, false, nil
}

func (r *PostgresRepository) CompleteIdempotencyKey(ctx context.Context, tx pgx.Tx, key string, userID uuid.UUID, statusCode int, responseBody []byte, orderID uuid.UUID) error {
	query := `
		UPDATE idempotency_keys
		SET status = 'COMPLETED',
		    response_code = $1,
		    response_body = $2,
		    order_id = $3
		WHERE user_id = $4 AND key = $5;
	`
	_, err := tx.Exec(ctx, query, statusCode, responseBody, orderID, userID, key)
	if err != nil {
		return fmt.Errorf("complete idempotency key: %w", err)
	}
	return nil
}

func (r *PostgresRepository) GetIdempotencyKey(ctx context.Context, key string, userID uuid.UUID) (*IdempotencyKeyRecord, error) {
	query := `
		SELECT key, user_id, request_hash, status, response_code, response_body, order_id, created_at, expires_at
		FROM idempotency_keys
		WHERE user_id = $1 AND key = $2;
	`
	rec := &IdempotencyKeyRecord{}
	var statusStr string
	err := r.pool.QueryRow(ctx, query, userID, key).Scan(
		&rec.Key, &rec.UserID, &rec.RequestHash, &statusStr, &rec.ResponseCode,
		&rec.ResponseBody, &rec.OrderID, &rec.CreatedAt, &rec.ExpiresAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get idempotency key: %w", err)
	}
	rec.Status = IdempotencyStatus(statusStr)
	return rec, nil
}

func (r *PostgresRepository) SaveIdempotencyKey(ctx context.Context, tx pgx.Tx, rec *IdempotencyKeyRecord) error {
	query := `
		INSERT INTO idempotency_keys (key, user_id, request_hash, status, response_code, response_body, order_id, created_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (user_id, key) DO UPDATE
		SET request_hash = EXCLUDED.request_hash,
		    status = EXCLUDED.status,
		    response_code = EXCLUDED.response_code,
		    response_body = EXCLUDED.response_body,
		    order_id = EXCLUDED.order_id,
		    created_at = EXCLUDED.created_at,
		    expires_at = EXCLUDED.expires_at;
	`
	var execer database.DBTX = tx
	if execer == nil {
		execer = r.pool
	}
	_, err := execer.Exec(ctx, query,
		rec.Key, rec.UserID, rec.RequestHash, string(rec.Status),
		rec.ResponseCode, rec.ResponseBody, rec.OrderID,
		rec.CreatedAt, rec.ExpiresAt,
	)
	if err != nil {
		return fmt.Errorf("save idempotency key: %w", err)
	}
	return nil
}

func (r *PostgresRepository) UpdateIdempotencyKey(ctx context.Context, tx pgx.Tx, rec *IdempotencyKeyRecord) error {
	query := `
		UPDATE idempotency_keys
		SET request_hash = $1,
		    status = $2,
		    response_code = $3,
		    response_body = $4,
		    order_id = $5,
		    expires_at = $6
		WHERE user_id = $7 AND key = $8;
	`
	var execer database.DBTX = tx
	if execer == nil {
		execer = r.pool
	}
	_, err := execer.Exec(ctx, query,
		rec.RequestHash, string(rec.Status), rec.ResponseCode,
		rec.ResponseBody, rec.OrderID, rec.ExpiresAt,
		rec.UserID, rec.Key,
	)
	if err != nil {
		return fmt.Errorf("update idempotency key: %w", err)
	}
	return nil
}

func (r *PostgresRepository) CreateOrder(ctx context.Context, tx pgx.Tx, o *Order) error {
	if o.ID == uuid.Nil {
		o.ID = uuid.New()
	}
	query := `
		INSERT INTO orders (id, user_id, idempotency_key, status, total_amount_minor, currency, version, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, 1, NOW(), NOW())
		RETURNING version, created_at, updated_at;
	`
	return tx.QueryRow(ctx, query,
		o.ID, o.UserID, o.IdempotencyKey, string(o.Status), o.TotalAmountMinor, o.Currency,
	).Scan(&o.Version, &o.CreatedAt, &o.UpdatedAt)
}

func (r *PostgresRepository) CreateOrderItems(ctx context.Context, tx pgx.Tx, items []OrderItem) error {
	if len(items) == 0 {
		return nil
	}
	query := `
		INSERT INTO order_items (id, order_id, sku, title_snapshot, unit_price_minor, quantity, subtotal_minor, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, NOW());
	`
	batch := &pgx.Batch{}
	for _, item := range items {
		itemID := item.ID
		if itemID == uuid.Nil {
			itemID = uuid.New()
		}
		batch.Queue(query, itemID, item.OrderID, item.SKU, item.TitleSnapshot, item.UnitPriceMinor, item.Quantity, item.SubtotalMinor)
	}

	br := tx.SendBatch(ctx, batch)
	defer br.Close()

	for range items {
		if _, err := br.Exec(); err != nil {
			return fmt.Errorf("insert order item batch: %w", err)
		}
	}
	return nil
}

func (r *PostgresRepository) CreateOrderWithItemsAndOutbox(ctx context.Context, tx pgx.Tx, o *Order, outboxPayload any) error {
	if err := r.CreateOrder(ctx, tx, o); err != nil {
		return err
	}
	if err := r.CreateOrderItems(ctx, tx, o.Items); err != nil {
		return err
	}
	if outboxPayload != nil {
		if err := r.InsertOutboxMessage(ctx, tx, o.ID, "OrderCreated", outboxPayload); err != nil {
			return err
		}
	}
	return nil
}

func (r *PostgresRepository) GetOrderByID(ctx context.Context, id uuid.UUID) (*Order, error) {
	query := `
		SELECT id, user_id, idempotency_key, status, total_amount_minor, currency, version, created_at, updated_at
		FROM orders
		WHERE id = $1;
	`
	o := &Order{}
	var statusStr string
	err := r.pool.QueryRow(ctx, query, id).Scan(
		&o.ID, &o.UserID, &o.IdempotencyKey, &statusStr, &o.TotalAmountMinor,
		&o.Currency, &o.Version, &o.CreatedAt, &o.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrOrderNotFound
		}
		return nil, fmt.Errorf("get order by id: %w", err)
	}
	o.Status = OrderStatus(statusStr)
	return o, nil
}

func (r *PostgresRepository) GetOrderByUserAndIdempotencyKey(ctx context.Context, userID uuid.UUID, key string) (*Order, error) {
	query := `
		SELECT id, user_id, idempotency_key, status, total_amount_minor, currency, version, created_at, updated_at
		FROM orders
		WHERE user_id = $1 AND idempotency_key = $2;
	`
	o := &Order{}
	var statusStr string
	err := r.pool.QueryRow(ctx, query, userID, key).Scan(
		&o.ID, &o.UserID, &o.IdempotencyKey, &statusStr, &o.TotalAmountMinor,
		&o.Currency, &o.Version, &o.CreatedAt, &o.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrOrderNotFound
		}
		return nil, fmt.Errorf("get order by user and idempotency key: %w", err)
	}
	o.Status = OrderStatus(statusStr)

	items, err := r.GetOrderItems(ctx, o.ID)
	if err != nil {
		return nil, err
	}
	o.Items = items
	return o, nil
}

func (r *PostgresRepository) GetOrderItems(ctx context.Context, orderID uuid.UUID) ([]OrderItem, error) {
	query := `
		SELECT id, order_id, sku, title_snapshot, unit_price_minor, quantity, subtotal_minor, created_at
		FROM order_items
		WHERE order_id = $1
		ORDER BY created_at ASC, id ASC;
	`
	rows, err := r.pool.Query(ctx, query, orderID)
	if err != nil {
		return nil, fmt.Errorf("get order items: %w", err)
	}
	defer rows.Close()

	var items []OrderItem
	for rows.Next() {
		var item OrderItem
		if err := rows.Scan(
			&item.ID, &item.OrderID, &item.SKU, &item.TitleSnapshot,
			&item.UnitPriceMinor, &item.Quantity, &item.SubtotalMinor, &item.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan order item: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate order items: %w", err)
	}
	return items, nil
}

func (r *PostgresRepository) ListOrders(ctx context.Context, params ListOrdersParams) ([]Order, string, bool, error) {
	limit := params.Limit
	if limit <= 0 || limit > 100 {
		limit = 20
	}

	var statusVal *string
	if params.Status != nil {
		s := string(*params.Status)
		statusVal = &s
	}

	query := `
		SELECT id, user_id, idempotency_key, status, total_amount_minor, currency, version, created_at, updated_at
		FROM orders
		WHERE user_id = $1
		  AND ($2::text IS NULL OR status = $2)
		ORDER BY created_at DESC, id DESC
		LIMIT $3;
	`
	rows, err := r.pool.Query(ctx, query, params.UserID, statusVal, limit+1)
	if err != nil {
		return nil, "", false, fmt.Errorf("list orders: %w", err)
	}
	defer rows.Close()

	var orders []Order
	for rows.Next() {
		var o Order
		var statusStr string
		if err := rows.Scan(
			&o.ID, &o.UserID, &o.IdempotencyKey, &statusStr, &o.TotalAmountMinor,
			&o.Currency, &o.Version, &o.CreatedAt, &o.UpdatedAt,
		); err != nil {
			return nil, "", false, fmt.Errorf("scan order: %w", err)
		}
		o.Status = OrderStatus(statusStr)
		orders = append(orders, o)
	}
	if err := rows.Err(); err != nil {
		return nil, "", false, fmt.Errorf("iterate orders: %w", err)
	}

	hasMore := len(orders) > limit
	if hasMore {
		orders = orders[:limit]
	}

	return orders, "", hasMore, nil
}

func (r *PostgresRepository) UpdateOrderStatus(ctx context.Context, tx pgx.Tx, orderID uuid.UUID, fromStatus, toStatus OrderStatus, expectedVersion int64) (int64, error) {
	query := `
		UPDATE orders
		SET status = $1, version = version + 1, updated_at = NOW()
		WHERE id = $2 AND status = $3 AND version = $4
		RETURNING version;
	`
	var newVersion int64
	err := tx.QueryRow(ctx, query, string(toStatus), orderID, string(fromStatus), expectedVersion).Scan(&newVersion)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Invariant check: is order already in target state?
			var currentStatus string
			var currentVersion int64
			checkErr := tx.QueryRow(ctx, "SELECT status, version FROM orders WHERE id = $1", orderID).Scan(&currentStatus, &currentVersion)
			if checkErr != nil {
				if errors.Is(checkErr, pgx.ErrNoRows) {
					return 0, ErrOrderNotFound
				}
				return 0, checkErr
			}
			if currentStatus == string(toStatus) {
				return currentVersion, nil // Idempotent success
			}
			if OrderStatus(currentStatus).IsTerminal() {
				return 0, ErrOrderTerminal
			}
			return 0, ErrOptimisticLockConflict
		}
		return 0, fmt.Errorf("update order status: %w", err)
	}
	return newVersion, nil
}

func (r *PostgresRepository) InsertOutboxMessage(ctx context.Context, tx pgx.Tx, aggregateID uuid.UUID, eventType string, payload any) error {
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal outbox payload: %w", err)
	}

	query := `
		INSERT INTO outbox_messages (id, aggregate_type, aggregate_id, event_type, payload, headers, status, created_at)
		VALUES (gen_random_uuid(), 'order', $1, $2, $3, '{}'::jsonb, 'PENDING', NOW());
	`
	_, err = tx.Exec(ctx, query, aggregateID.String(), eventType, payloadBytes)
	if err != nil {
		return fmt.Errorf("insert outbox message: %w", err)
	}
	return nil
}
