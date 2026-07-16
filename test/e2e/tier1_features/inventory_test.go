package tier1_features

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"shopflow/test/e2e/harness"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// FEAT-INV-01: SKU Stock Initialization & Inventory Tracking
func TestInventoryStockInitialization(t *testing.T) {
	env := harness.SetupEnvironment(t)
	client := env.RequireHTTPServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	sku := fmt.Sprintf("SKU-INV-INIT-%s", uuid.New().String()[:8])
	_, _, _, err := client.CreateProduct(ctx, harness.CreateProductRequest{
		SKU:        sku,
		Title:      "Initial Stock Product",
		PriceMinor: 1999,
		Currency:   "USD",
	}, uuid.New().String())
	require.NoError(t, err)

	// Replenish stock
	inv, prob, status, err := client.ReplenishStock(ctx, sku, 100)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status, "expected 200 OK on replenishment, prob: %+v", prob)
	require.NotNil(t, inv)
	assert.Equal(t, 100, inv.OnHand)
	assert.Equal(t, 0, inv.Reserved)

	// Query stock
	fetched, prob, status, err := client.GetInventory(ctx, sku)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status, "prob: %+v", prob)
	require.NotNil(t, fetched)
	assert.Equal(t, 100, fetched.OnHand)
}

// FEAT-INV-02: Multi-SKU Atomic Stock Reservation with Deterministic Ascending Locking
func TestInventoryDeterministicLocking(t *testing.T) {
	env := harness.SetupEnvironment(t)
	db := env.RequireDB(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Seed multiple SKUs in unsorted order
	skus := []string{
		fmt.Sprintf("SKU-Z-%s", uuid.New().String()[:6]),
		fmt.Sprintf("SKU-A-%s", uuid.New().String()[:6]),
		fmt.Sprintf("SKU-M-%s", uuid.New().String()[:6]),
	}

	for _, s := range skus {
		err := db.SeedInventory(ctx, s, 50, 0)
		require.NoError(t, err)
	}

	// Verify locking enforces ascending order: SKU-A... < SKU-M... < SKU-Z...
	locked, err := db.VerifyDeterministicSKULocking(ctx, skus)
	require.NoError(t, err)
	require.Len(t, locked, 3)
	assert.True(t, locked[0] < locked[1] && locked[1] < locked[2], "SKU locks MUST be acquired in ascending order")
}

// FEAT-INV-03: Physical Stock Conservation (Zero Overselling, on_hand >= 0, reserved <= on_hand)
func TestInventoryPhysicalStockConservation(t *testing.T) {
	env := harness.SetupEnvironment(t)
	db := env.RequireDB(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sku := fmt.Sprintf("SKU-CONSERVE-%s", uuid.New().String()[:8])
	err := db.SeedInventory(ctx, sku, 10, 5) // on_hand = 10, reserved = 5 -> valid
	require.NoError(t, err)

	db.AssertInventoryConservation(ctx, t, sku)

	// Invariant violation attempt: CHECK (reserved <= on_hand) MUST reject reserved > on_hand
	_, err = db.Pool.Exec(ctx, `
		UPDATE inventory SET reserved = 20 WHERE sku = $1;
	`, sku)
	assert.Error(t, err, "database CHECK constraint MUST reject reserved > on_hand")
}

// FEAT-INV-04: Stock Reservation Release & Stock Reconciliation (Compensation)
func TestInventoryReservationRelease(t *testing.T) {
	env := harness.SetupEnvironment(t)
	db := env.RequireDB(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sku := fmt.Sprintf("SKU-REL-%s", uuid.New().String()[:8])
	err := db.SeedInventory(ctx, sku, 20, 10) // 10 reserved
	require.NoError(t, err)

	orderID := uuid.New().String()
	resID := uuid.New().String()

	tx, err := db.Pool.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx)

	// Create reservation record
	_, err = tx.Exec(ctx, `
		INSERT INTO stock_reservations (id, order_id, status, expires_at)
		VALUES ($1, $2, 'PENDING', NOW() + INTERVAL '10 minutes');
	`, resID, orderID)
	require.NoError(t, err)

	_, err = tx.Exec(ctx, `
		INSERT INTO stock_reservation_items (reservation_id, sku, quantity)
		VALUES ($1, $2, 10);
	`, resID, sku)
	require.NoError(t, err)

	// Compensation release: decrement reserved by 10
	_, err = tx.Exec(ctx, `
		UPDATE inventory SET reserved = reserved - 10, updated_at = NOW() WHERE sku = $1;
	`, sku)
	require.NoError(t, err)

	_, err = tx.Exec(ctx, `
		UPDATE stock_reservations SET status = 'RELEASED', updated_at = NOW() WHERE id = $1;
	`, resID)
	require.NoError(t, err)

	err = tx.Commit(ctx)
	require.NoError(t, err)

	// Verify inventory after release: on_hand = 20, reserved = 0
	db.AssertInventory(ctx, t, sku, 20, 0)
	db.AssertInventoryConservation(ctx, t, sku)
}
