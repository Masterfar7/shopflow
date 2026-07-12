// internal/domain/cart/repository.go
package cart

import (
	"context"
	"errors"
	"fmt"

	"shopflow/internal/platform/database"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Repository interface {
	GetCartByID(ctx context.Context, id uuid.UUID) (*Cart, error)
	GetActiveCartByUserID(ctx context.Context, userID uuid.UUID) (*Cart, error)
	CreateCart(ctx context.Context, cart *Cart) error

	// Transactional cart mutation methods
	IncrementCartVersion(ctx context.Context, tx pgx.Tx, cartID uuid.UUID, expectedVersion int64) (int64, error)
	GetCartItems(ctx context.Context, cartID uuid.UUID) ([]CartItem, error)
	UpsertCartItem(ctx context.Context, tx pgx.Tx, cartID uuid.UUID, sku string, quantity int, unitPriceMinor int64) error
	UpdateCartItemQuantity(ctx context.Context, tx pgx.Tx, cartID uuid.UUID, sku string, quantity int) error
	DeleteCartItem(ctx context.Context, tx pgx.Tx, cartID uuid.UUID, sku string) error
	ClearCartItems(ctx context.Context, tx pgx.Tx, cartID uuid.UUID) error
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

func (r *PostgresRepository) GetCartByID(ctx context.Context, id uuid.UUID) (*Cart, error) {
	query := `
		SELECT id, user_id, status, version, created_at, updated_at
		FROM carts
		WHERE id = $1;
	`
	c := &Cart{}
	var statusStr string
	err := r.pool.QueryRow(ctx, query, id).Scan(
		&c.ID, &c.UserID, &statusStr, &c.Version, &c.CreatedAt, &c.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrCartNotFound
		}
		return nil, fmt.Errorf("get cart by id: %w", err)
	}
	c.Status = CartStatus(statusStr)
	return c, nil
}

func (r *PostgresRepository) GetActiveCartByUserID(ctx context.Context, userID uuid.UUID) (*Cart, error) {
	query := `
		SELECT id, user_id, status, version, created_at, updated_at
		FROM carts
		WHERE user_id = $1 AND status = 'ACTIVE';
	`
	c := &Cart{}
	var statusStr string
	err := r.pool.QueryRow(ctx, query, userID).Scan(
		&c.ID, &c.UserID, &statusStr, &c.Version, &c.CreatedAt, &c.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrCartNotFound
		}
		return nil, fmt.Errorf("get active cart by user id: %w", err)
	}
	c.Status = CartStatus(statusStr)
	return c, nil
}

func (r *PostgresRepository) CreateCart(ctx context.Context, cart *Cart) error {
	if cart.ID == uuid.Nil {
		cart.ID = uuid.New()
	}

	query := `
		INSERT INTO carts (id, user_id, status, version, created_at, updated_at)
		VALUES ($1, $2, 'ACTIVE', 1, NOW(), NOW())
		ON CONFLICT (user_id) WHERE status = 'ACTIVE'
		DO UPDATE SET updated_at = NOW()
		RETURNING id, user_id, status, version, created_at, updated_at;
	`
	var statusStr string
	err := r.pool.QueryRow(ctx, query, cart.ID, cart.UserID).Scan(
		&cart.ID, &cart.UserID, &statusStr, &cart.Version, &cart.CreatedAt, &cart.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("create or get active cart: %w", err)
	}
	cart.Status = CartStatus(statusStr)
	return nil
}

func (r *PostgresRepository) IncrementCartVersion(ctx context.Context, tx pgx.Tx, cartID uuid.UUID, expectedVersion int64) (int64, error) {
	query := `
		UPDATE carts
		SET version = version + 1, updated_at = NOW()
		WHERE id = $1 AND version = $2 AND status = 'ACTIVE'
		RETURNING version;
	`
	var newVersion int64
	err := tx.QueryRow(ctx, query, cartID, expectedVersion).Scan(&newVersion)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Check if the cart exists
			var currentVersion int64
			var status string
			checkErr := tx.QueryRow(ctx, `SELECT version, status FROM carts WHERE id = $1`, cartID).Scan(&currentVersion, &status)
			if checkErr != nil {
				if errors.Is(checkErr, pgx.ErrNoRows) {
					return 0, ErrCartNotFound
				}
				return 0, fmt.Errorf("check cart existence: %w", checkErr)
			}
			if status != string(CartStatusActive) {
				return 0, ErrCartNotActive
			}
			return 0, ErrOptimisticLockConflict
		}
		return 0, fmt.Errorf("increment cart version: %w", err)
	}
	return newVersion, nil
}

func (r *PostgresRepository) GetCartItems(ctx context.Context, cartID uuid.UUID) ([]CartItem, error) {
	query := `
		SELECT id, cart_id, sku, quantity, unit_price_minor, created_at, updated_at
		FROM cart_items
		WHERE cart_id = $1
		ORDER BY sku ASC;
	`
	rows, err := r.pool.Query(ctx, query, cartID)
	if err != nil {
		return nil, fmt.Errorf("get cart items: %w", err)
	}
	defer rows.Close()

	var items []CartItem
	for rows.Next() {
		var item CartItem
		if err := rows.Scan(
			&item.ID, &item.CartID, &item.SKU, &item.Quantity, &item.UnitPriceMinor,
			&item.CreatedAt, &item.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan cart item: %w", err)
		}
		items = append(items, item)
	}
	return items, nil
}

func (r *PostgresRepository) UpsertCartItem(ctx context.Context, tx pgx.Tx, cartID uuid.UUID, sku string, quantity int, unitPriceMinor int64) error {
	query := `
		INSERT INTO cart_items (id, cart_id, sku, quantity, unit_price_minor, created_at, updated_at)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, NOW(), NOW())
		ON CONFLICT (cart_id, sku)
		DO UPDATE SET
			quantity = cart_items.quantity + EXCLUDED.quantity,
			unit_price_minor = EXCLUDED.unit_price_minor,
			updated_at = NOW();
	`
	_, err := tx.Exec(ctx, query, cartID, sku, quantity, unitPriceMinor)
	if err != nil {
		return fmt.Errorf("upsert cart item: %w", err)
	}
	return nil
}

func (r *PostgresRepository) UpdateCartItemQuantity(ctx context.Context, tx pgx.Tx, cartID uuid.UUID, sku string, quantity int) error {
	query := `
		UPDATE cart_items
		SET quantity = $1, updated_at = NOW()
		WHERE cart_id = $2 AND sku = $3;
	`
	tag, err := tx.Exec(ctx, query, quantity, cartID, sku)
	if err != nil {
		return fmt.Errorf("update cart item quantity: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrItemNotFound
	}
	return nil
}

func (r *PostgresRepository) DeleteCartItem(ctx context.Context, tx pgx.Tx, cartID uuid.UUID, sku string) error {
	query := `
		DELETE FROM cart_items
		WHERE cart_id = $1 AND sku = $2;
	`
	tag, err := tx.Exec(ctx, query, cartID, sku)
	if err != nil {
		return fmt.Errorf("delete cart item: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrItemNotFound
	}
	return nil
}

func (r *PostgresRepository) ClearCartItems(ctx context.Context, tx pgx.Tx, cartID uuid.UUID) error {
	query := `DELETE FROM cart_items WHERE cart_id = $1;`
	_, err := tx.Exec(ctx, query, cartID)
	if err != nil {
		return fmt.Errorf("clear cart items: %w", err)
	}
	return nil
}
