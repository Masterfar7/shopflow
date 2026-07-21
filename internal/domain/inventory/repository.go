package inventory

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"shopflow/internal/platform/database"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type Repository interface {
	// Read-only stock lookup
	GetStock(ctx context.Context, sku string) (*Item, error)
	GetStockForSKUs(ctx context.Context, skus []string) ([]Item, error)

	// Replenishment / Initializer
	UpsertStock(ctx context.Context, sku string, quantity int) (*Item, error)

	// Deterministic locking and transactional mutations
	LockSKUsForUpdate(ctx context.Context, tx pgx.Tx, sortedSKUs []string) (map[string]Item, error)
	IncrementReserved(ctx context.Context, tx pgx.Tx, sku string, qty int) error
	DecrementReserved(ctx context.Context, tx pgx.Tx, sku string, qty int) error
	CommitStockOnHand(ctx context.Context, tx pgx.Tx, sku string, qty int) error

	// Reservation records
	CreateReservation(ctx context.Context, tx pgx.Tx, res *Reservation) error
	CreateReservationItems(ctx context.Context, tx pgx.Tx, resID uuid.UUID, items []StockItemRequest) error
	GetReservationByID(ctx context.Context, id uuid.UUID) (*Reservation, error)
	GetReservationByOrderID(ctx context.Context, orderID uuid.UUID) (*Reservation, error)
	LockReservationForUpdate(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*Reservation, error)
	LockReservationByOrderForUpdate(ctx context.Context, tx pgx.Tx, orderID uuid.UUID) (*Reservation, error)
	GetReservationItems(ctx context.Context, resID uuid.UUID) ([]ReservationItem, error)
	GetReservationItemsTx(ctx context.Context, tx pgx.Tx, resID uuid.UUID) ([]ReservationItem, error)
	UpdateReservationStatus(ctx context.Context, tx pgx.Tx, id uuid.UUID, status ReservationStatus) error
}

type PostgresRepository struct {
	db database.DBTX
}

func NewPostgresRepository(db database.DBTX) *PostgresRepository {
	return &PostgresRepository{db: db}
}

func (r *PostgresRepository) GetStock(ctx context.Context, sku string) (*Item, error) {
	row := r.db.QueryRow(ctx, `
		SELECT sku, on_hand, reserved, version, updated_at
		FROM inventory
		WHERE sku = $1;
	`, sku)

	var item Item
	if err := row.Scan(&item.SKU, &item.OnHand, &item.Reserved, &item.Version, &item.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrSKUNotFound
		}
		return nil, fmt.Errorf("inventory: get stock: %w", err)
	}
	item.Available = item.OnHand - item.Reserved
	return &item, nil
}

func (r *PostgresRepository) GetStockForSKUs(ctx context.Context, skus []string) ([]Item, error) {
	if len(skus) == 0 {
		return []Item{}, nil
	}

	rows, err := r.db.Query(ctx, `
		SELECT sku, on_hand, reserved, version, updated_at
		FROM inventory
		WHERE sku = ANY($1)
		ORDER BY sku ASC;
	`, skus)
	if err != nil {
		return nil, fmt.Errorf("inventory: get stock for skus: %w", err)
	}
	defer rows.Close()

	var items []Item
	for rows.Next() {
		var item Item
		if err := rows.Scan(&item.SKU, &item.OnHand, &item.Reserved, &item.Version, &item.UpdatedAt); err != nil {
			return nil, fmt.Errorf("inventory: scan stock item: %w", err)
		}
		item.Available = item.OnHand - item.Reserved
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *PostgresRepository) UpsertStock(ctx context.Context, sku string, quantity int) (*Item, error) {
	row := r.db.QueryRow(ctx, `
		INSERT INTO inventory (sku, on_hand, reserved, version, updated_at)
		VALUES ($1, $2, 0, 1, NOW())
		ON CONFLICT (sku) DO UPDATE
		SET on_hand = inventory.on_hand + EXCLUDED.on_hand,
		    version = inventory.version + 1,
		    updated_at = NOW()
		RETURNING sku, on_hand, reserved, version, updated_at;
	`, sku, quantity)

	var item Item
	if err := row.Scan(&item.SKU, &item.OnHand, &item.Reserved, &item.Version, &item.UpdatedAt); err != nil {
		return nil, fmt.Errorf("inventory: upsert stock: %w", err)
	}
	item.Available = item.OnHand - item.Reserved
	return &item, nil
}

func (r *PostgresRepository) LockSKUsForUpdate(ctx context.Context, tx pgx.Tx, sortedSKUs []string) (map[string]Item, error) {
	if len(sortedSKUs) == 0 {
		return make(map[string]Item), nil
	}
	if !sort.StringsAreSorted(sortedSKUs) {
		return nil, ErrDeadlockAvoidanceViolation
	}

	rows, err := tx.Query(ctx, `
		SELECT sku, on_hand, reserved, version, updated_at
		FROM inventory
		WHERE sku = ANY($1)
		ORDER BY sku ASC
		FOR UPDATE;
	`, sortedSKUs)
	if err != nil {
		return nil, fmt.Errorf("inventory: lock skus for update: %w", err)
	}
	defer rows.Close()

	result := make(map[string]Item, len(sortedSKUs))
	for rows.Next() {
		var item Item
		if err := rows.Scan(&item.SKU, &item.OnHand, &item.Reserved, &item.Version, &item.UpdatedAt); err != nil {
			return nil, fmt.Errorf("inventory: scan locked item: %w", err)
		}
		item.Available = item.OnHand - item.Reserved
		result[item.SKU] = item
	}
	return result, rows.Err()
}

func (r *PostgresRepository) IncrementReserved(ctx context.Context, tx pgx.Tx, sku string, qty int) error {
	tag, err := tx.Exec(ctx, `
		UPDATE inventory
		SET reserved = reserved + $2,
		    version = version + 1,
		    updated_at = NOW()
		WHERE sku = $1;
	`, sku, qty)
	if err != nil {
		return fmt.Errorf("inventory: increment reserved: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrSKUNotFound
	}
	return nil
}

func (r *PostgresRepository) DecrementReserved(ctx context.Context, tx pgx.Tx, sku string, qty int) error {
	tag, err := tx.Exec(ctx, `
		UPDATE inventory
		SET reserved = reserved - $2,
		    version = version + 1,
		    updated_at = NOW()
		WHERE sku = $1;
	`, sku, qty)
	if err != nil {
		return fmt.Errorf("inventory: decrement reserved: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrSKUNotFound
	}
	return nil
}

func (r *PostgresRepository) CommitStockOnHand(ctx context.Context, tx pgx.Tx, sku string, qty int) error {
	tag, err := tx.Exec(ctx, `
		UPDATE inventory
		SET on_hand = on_hand - $2,
		    reserved = reserved - $2,
		    version = version + 1,
		    updated_at = NOW()
		WHERE sku = $1;
	`, sku, qty)
	if err != nil {
		return fmt.Errorf("inventory: commit stock on hand: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrSKUNotFound
	}
	return nil
}

func (r *PostgresRepository) CreateReservation(ctx context.Context, tx pgx.Tx, res *Reservation) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO stock_reservations (id, order_id, status, expires_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, NOW(), NOW());
	`, res.ID, res.OrderID, res.Status, res.ExpiresAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return ErrDuplicateReservationID
		}
		return fmt.Errorf("inventory: create reservation: %w", err)
	}
	return nil
}

func (r *PostgresRepository) CreateReservationItems(ctx context.Context, tx pgx.Tx, resID uuid.UUID, items []StockItemRequest) error {
	for _, item := range items {
		_, err := tx.Exec(ctx, `
			INSERT INTO stock_reservation_items (id, reservation_id, sku, quantity, created_at)
			VALUES (gen_random_uuid(), $1, $2, $3, NOW());
		`, resID, item.SKU, item.Quantity)
		if err != nil {
			return fmt.Errorf("inventory: create reservation item: %w", err)
		}
	}
	return nil
}

func (r *PostgresRepository) GetReservationByID(ctx context.Context, id uuid.UUID) (*Reservation, error) {
	row := r.db.QueryRow(ctx, `
		SELECT id, order_id, status, expires_at, created_at, updated_at
		FROM stock_reservations
		WHERE id = $1;
	`, id)

	var res Reservation
	if err := row.Scan(&res.ID, &res.OrderID, &res.Status, &res.ExpiresAt, &res.CreatedAt, &res.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrReservationNotFound
		}
		return nil, fmt.Errorf("inventory: get reservation by id: %w", err)
	}
	return &res, nil
}

func (r *PostgresRepository) GetReservationByOrderID(ctx context.Context, orderID uuid.UUID) (*Reservation, error) {
	row := r.db.QueryRow(ctx, `
		SELECT id, order_id, status, expires_at, created_at, updated_at
		FROM stock_reservations
		WHERE order_id = $1;
	`, orderID)

	var res Reservation
	if err := row.Scan(&res.ID, &res.OrderID, &res.Status, &res.ExpiresAt, &res.CreatedAt, &res.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrReservationNotFound
		}
		return nil, fmt.Errorf("inventory: get reservation by order id: %w", err)
	}
	return &res, nil
}

func (r *PostgresRepository) LockReservationForUpdate(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*Reservation, error) {
	row := tx.QueryRow(ctx, `
		SELECT id, order_id, status, expires_at, created_at, updated_at
		FROM stock_reservations
		WHERE id = $1
		FOR UPDATE;
	`, id)

	var res Reservation
	if err := row.Scan(&res.ID, &res.OrderID, &res.Status, &res.ExpiresAt, &res.CreatedAt, &res.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrReservationNotFound
		}
		return nil, fmt.Errorf("inventory: lock reservation for update: %w", err)
	}
	return &res, nil
}

func (r *PostgresRepository) LockReservationByOrderForUpdate(ctx context.Context, tx pgx.Tx, orderID uuid.UUID) (*Reservation, error) {
	row := tx.QueryRow(ctx, `
		SELECT id, order_id, status, expires_at, created_at, updated_at
		FROM stock_reservations
		WHERE order_id = $1
		FOR UPDATE;
	`, orderID)

	var res Reservation
	if err := row.Scan(&res.ID, &res.OrderID, &res.Status, &res.ExpiresAt, &res.CreatedAt, &res.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrReservationNotFound
		}
		return nil, fmt.Errorf("inventory: lock reservation by order for update: %w", err)
	}
	return &res, nil
}

func (r *PostgresRepository) GetReservationItems(ctx context.Context, resID uuid.UUID) ([]ReservationItem, error) {
	return r.GetReservationItemsTx(ctx, nil, resID)
}

func (r *PostgresRepository) GetReservationItemsTx(ctx context.Context, tx pgx.Tx, resID uuid.UUID) ([]ReservationItem, error) {
	query := `
		SELECT id, reservation_id, sku, quantity, created_at
		FROM stock_reservation_items
		WHERE reservation_id = $1
		ORDER BY sku ASC;
	`
	var rows pgx.Rows
	var err error
	if tx != nil {
		rows, err = tx.Query(ctx, query, resID)
	} else {
		rows, err = r.db.Query(ctx, query, resID)
	}
	if err != nil {
		return nil, fmt.Errorf("inventory: get reservation items: %w", err)
	}
	defer rows.Close()

	var items []ReservationItem
	for rows.Next() {
		var item ReservationItem
		if err := rows.Scan(&item.ID, &item.ReservationID, &item.SKU, &item.Quantity, &item.CreatedAt); err != nil {
			return nil, fmt.Errorf("inventory: scan reservation item: %w", err)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *PostgresRepository) UpdateReservationStatus(ctx context.Context, tx pgx.Tx, id uuid.UUID, status ReservationStatus) error {
	tag, err := tx.Exec(ctx, `
		UPDATE stock_reservations
		SET status = $2, updated_at = NOW()
		WHERE id = $1;
	`, id, status)
	if err != nil {
		return fmt.Errorf("inventory: update reservation status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrReservationNotFound
	}
	return nil
}
