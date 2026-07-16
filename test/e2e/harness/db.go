package harness

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// DBAssert encapsulates database operations, migrations, and invariant assertions.
type DBAssert struct {
	Pool *pgxpool.Pool
	DSN  string
}

// NewDBAssert connects to the PostgreSQL database with pgxpool.
func NewDBAssert(ctx context.Context, dsn string) (*DBAssert, error) {
	pgxCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to parse database DSN: %w", err)
	}
	pgxCfg.MaxConns = 30
	pgxCfg.MinConns = 2
	pgxCfg.MaxConnIdleTime = 5 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, pgxCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create connection pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	return &DBAssert{
		Pool: pool,
		DSN:  dsn,
	}, nil
}

// Close closes the underlying connection pool.
func (db *DBAssert) Close() {
	if db.Pool != nil {
		db.Pool.Close()
	}
}

// Ping checks database connectivity.
func (db *DBAssert) Ping(ctx context.Context) error {
	return db.Pool.Ping(ctx)
}

// ApplyMigrations executes Goose SQL migrations against the target database.
func (db *DBAssert) ApplyMigrations(ctx context.Context, migrationsDir string) error {
	sqlDB, err := sql.Open("pgx", db.DSN)
	if err != nil {
		return fmt.Errorf("failed to open sql.DB for migrations: %w", err)
	}
	defer sqlDB.Close()

	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("failed to set goose dialect: %w", err)
	}

	if err := goose.UpContext(ctx, sqlDB, migrationsDir); err != nil {
		return fmt.Errorf("goose migration up failed: %w", err)
	}

	return nil
}

// TruncateAll safely clears all domain tables across tests.
func (db *DBAssert) TruncateAll(ctx context.Context) error {
	query := `
		TRUNCATE TABLE
			payment_refunds,
			payments,
			dead_letter_messages,
			inbox_messages,
			outbox_messages,
			order_saga_logs,
			order_sagas,
			stock_reservation_items,
			stock_reservations,
			inventory,
			idempotency_keys,
			order_items,
			orders,
			cart_items,
			carts,
			products,
			categories
		RESTART IDENTITY CASCADE;
	`
	_, err := db.Pool.Exec(ctx, query)
	return err
}

// SeedProduct inserts a product SKU and corresponding inventory.
func (db *DBAssert) SeedProduct(ctx context.Context, sku, title string, priceMinor int64, currency string, onHand int) error {
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	_, err = tx.Exec(ctx, `
		INSERT INTO products (sku, title, price_minor, currency, is_active, version)
		VALUES ($1, $2, $3, $4, TRUE, 1)
		ON CONFLICT (sku) DO UPDATE
		SET title = EXCLUDED.title, price_minor = EXCLUDED.price_minor, updated_at = NOW();
	`, sku, title, priceMinor, currency)
	if err != nil {
		return fmt.Errorf("seed product insert failed: %w", err)
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO inventory (sku, on_hand, reserved, version)
		VALUES ($1, $2, 0, 1)
		ON CONFLICT (sku) DO UPDATE
		SET on_hand = EXCLUDED.on_hand, reserved = 0, updated_at = NOW();
	`, sku, onHand)
	if err != nil {
		return fmt.Errorf("seed inventory insert failed: %w", err)
	}

	return tx.Commit(ctx)
}

// SeedInventory seeds initial inventory levels directly.
func (db *DBAssert) SeedInventory(ctx context.Context, sku string, onHand, reserved int) error {
	_, err := db.Pool.Exec(ctx, `
		INSERT INTO inventory (sku, on_hand, reserved, version)
		VALUES ($1, $2, $3, 1)
		ON CONFLICT (sku) DO UPDATE
		SET on_hand = EXCLUDED.on_hand, reserved = EXCLUDED.reserved, updated_at = NOW();
	`, sku, onHand, reserved)
	return err
}

// AssertInventory asserts on_hand and reserved stock levels.
func (db *DBAssert) AssertInventory(ctx context.Context, t testing.TB, sku string, expectedOnHand, expectedReserved int) {
	t.Helper()
	var onHand, reserved int
	err := db.Pool.QueryRow(ctx, "SELECT on_hand, reserved FROM inventory WHERE sku = $1", sku).Scan(&onHand, &reserved)
	require.NoError(t, err, "failed to query inventory for sku: %s", sku)
	assert.Equal(t, expectedOnHand, onHand, "inventory on_hand mismatch for sku: %s", sku)
	assert.Equal(t, expectedReserved, reserved, "inventory reserved mismatch for sku: %s", sku)
}

// AssertInventoryConservation asserts the physical stock conservation invariants:
// on_hand >= 0, reserved >= 0, reserved <= on_hand.
func (db *DBAssert) AssertInventoryConservation(ctx context.Context, t testing.TB, sku string) {
	t.Helper()
	var onHand, reserved int
	err := db.Pool.QueryRow(ctx, "SELECT on_hand, reserved FROM inventory WHERE sku = $1", sku).Scan(&onHand, &reserved)
	require.NoError(t, err, "failed to query inventory for sku: %s", sku)
	assert.GreaterOrEqual(t, onHand, 0, "invariant violated: on_hand must be >= 0 for sku: %s", sku)
	assert.GreaterOrEqual(t, reserved, 0, "invariant violated: reserved must be >= 0 for sku: %s", sku)
	assert.LessOrEqual(t, reserved, onHand, "invariant violated: reserved must be <= on_hand for sku: %s", sku)
}

// AssertOrder asserts the status and total minor amount for an order.
func (db *DBAssert) AssertOrder(ctx context.Context, t testing.TB, orderID string, expectedStatus string, expectedTotalMinor int64) {
	t.Helper()
	var status string
	var totalMinor int64
	err := db.Pool.QueryRow(ctx, "SELECT status, total_amount_minor FROM orders WHERE id = $1", orderID).Scan(&status, &totalMinor)
	require.NoError(t, err, "failed to query order: %s", orderID)
	assert.Equal(t, expectedStatus, status, "order status mismatch for order: %s", orderID)
	assert.Equal(t, expectedTotalMinor, totalMinor, "order total_amount_minor mismatch for order: %s", orderID)
}

// AssertOrderTotalsInvariant asserts that sum(subtotal_minor) == total_amount_minor and subtotal == unit_price * quantity.
func (db *DBAssert) AssertOrderTotalsInvariant(ctx context.Context, t testing.TB, orderID string) {
	t.Helper()
	var totalAmount int64
	err := db.Pool.QueryRow(ctx, "SELECT total_amount_minor FROM orders WHERE id = $1", orderID).Scan(&totalAmount)
	require.NoError(t, err, "failed to read order: %s", orderID)

	rows, err := db.Pool.Query(ctx, `
		SELECT sku, unit_price_minor, quantity, subtotal_minor
		FROM order_items
		WHERE order_id = $1
	`, orderID)
	require.NoError(t, err, "failed to query order items for order: %s", orderID)
	defer rows.Close()

	var sumSubtotals int64
	for rows.Next() {
		var sku string
		var unitPrice, subtotal int64
		var qty int
		err := rows.Scan(&sku, &unitPrice, &qty, &subtotal)
		require.NoError(t, err)

		// Check line item subtotal invariant
		assert.Equal(t, unitPrice*int64(qty), subtotal, "line item subtotal invariant violated for sku %s in order %s", sku, orderID)
		sumSubtotals += subtotal
	}

	assert.Equal(t, totalAmount, sumSubtotals, "order total invariant violated: sum(subtotals) != total_amount_minor for order %s", orderID)
}

// AssertOutboxMessage asserts that an outbox record exists and was published.
func (db *DBAssert) AssertOutboxMessage(ctx context.Context, t testing.TB, aggregateID, eventType string, expectedStatus string) {
	t.Helper()
	var status string
	err := db.Pool.QueryRow(ctx, `
		SELECT status
		FROM outbox_messages
		WHERE aggregate_id = $1 AND event_type = $2
		ORDER BY created_at DESC
		LIMIT 1
	`, aggregateID, eventType).Scan(&status)
	require.NoError(t, err, "outbox message not found for aggregate %s, event %s", aggregateID, eventType)
	assert.Equal(t, expectedStatus, status)
}

// AssertInboxMessage asserts that an inbox message exists with expected status.
func (db *DBAssert) AssertInboxMessage(ctx context.Context, t testing.TB, messageID, consumerGroup, expectedStatus string) {
	t.Helper()
	var status string
	err := db.Pool.QueryRow(ctx, `
		SELECT status
		FROM inbox_messages
		WHERE message_id = $1 AND consumer_group = $2
	`, messageID, consumerGroup).Scan(&status)
	require.NoError(t, err, "inbox message not found for id %s, group %s", messageID, consumerGroup)
	assert.Equal(t, expectedStatus, status)
}

// AssertSagaState asserts the current state of an order saga.
func (db *DBAssert) AssertSagaState(ctx context.Context, t testing.TB, orderID, expectedState string) {
	t.Helper()
	var currentState string
	err := db.Pool.QueryRow(ctx, "SELECT current_state FROM order_sagas WHERE order_id = $1", orderID).Scan(&currentState)
	require.NoError(t, err, "order saga not found for order: %s", orderID)
	assert.Equal(t, expectedState, currentState, "saga state mismatch for order: %s", orderID)
}

// VerifyDeterministicSKULocking simulates multi-SKU row locking with ascending sort to guarantee zero deadlocks.
func (db *DBAssert) VerifyDeterministicSKULocking(ctx context.Context, skus []string) ([]string, error) {
	// Deterministic sorting invariant
	sorted := make([]string, len(skus))
	copy(sorted, skus)
	sort.Strings(sorted)

	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	rows, err := tx.Query(ctx, `
		SELECT sku
		FROM inventory
		WHERE sku = ANY($1)
		ORDER BY sku ASC
		FOR UPDATE;
	`, sorted)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var lockedSKUs []string
	for rows.Next() {
		var sku string
		if err := rows.Scan(&sku); err != nil {
			return nil, err
		}
		lockedSKUs = append(lockedSKUs, sku)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return lockedSKUs, nil
}
