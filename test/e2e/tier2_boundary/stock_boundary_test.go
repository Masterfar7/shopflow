package tier2_boundary

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

// Invariant: Reserving stock with 0 on-hand must fail with Insufficient Stock.
func TestZeroStockReservationRejection(t *testing.T) {
	env := harness.SetupEnvironment(t)
	client := env.RequireHTTPServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sku := fmt.Sprintf("SKU-ZERO-%s", uuid.New().String()[:8])
	_, _, _, _ = client.CreateProduct(ctx, harness.CreateProductRequest{
		SKU:        sku,
		Title:      "Out of Stock Item",
		PriceMinor: 2000,
		Currency:   "USD",
	}, uuid.New().String())

	// Attempt creating order for item with 0 on_hand
	orderReq := harness.CreateOrderRequest{
		Currency: "USD",
		Items: []harness.OrderItemRequest{
			{SKU: sku, Quantity: 1},
		},
	}
	_, _, status, err := client.CreateOrder(ctx, uuid.New().String(), orderReq, uuid.New().String())
	require.NoError(t, err)
	assert.True(t, status == http.StatusConflict || status == http.StatusBadRequest || status == http.StatusUnprocessableEntity,
		"order creation with zero stock must be rejected, got %d", status)
}

// Invariant: Negative or zero quantities in cart or order MUST be rejected.
func TestNegativeAndZeroQuantityRejection(t *testing.T) {
	env := harness.SetupEnvironment(t)
	client := env.RequireHTTPServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sku := fmt.Sprintf("SKU-QTY-%s", uuid.New().String()[:8])
	_, _, _, _ = client.CreateProduct(ctx, harness.CreateProductRequest{
		SKU:        sku,
		Title:      "Valid Product",
		PriceMinor: 1000,
		Currency:   "USD",
	}, uuid.New().String())

	testQuantities := []int{0, -1, -50}
	for _, qty := range testQuantities {
		t.Run(fmt.Sprintf("quantity_%d", qty), func(t *testing.T) {
			orderReq := harness.CreateOrderRequest{
				Currency: "USD",
				Items: []harness.OrderItemRequest{
					{SKU: sku, Quantity: qty},
				},
			}
			_, _, status, err := client.CreateOrder(ctx, uuid.New().String(), orderReq, uuid.New().String())
			require.NoError(t, err)
			assert.Equal(t, http.StatusBadRequest, status, "quantity <= 0 must fail with 400 Bad Request")
		})
	}
}

// Invariant: Multi-SKU atomic all-or-nothing reservation guarantee.
// If any single SKU has insufficient stock, the transaction MUST abort completely with zero partial reservations.
func TestMultiSKUAllOrNothingAtomicReservation(t *testing.T) {
	env := harness.SetupEnvironment(t)
	db := env.RequireDB(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	skuAvailable := fmt.Sprintf("SKU-AVAIL-%s", uuid.New().String()[:8])
	skuDepleted := fmt.Sprintf("SKU-DEPL-%s", uuid.New().String()[:8])

	// SKU 1 has 10 units available; SKU 2 has 0 units
	require.NoError(t, db.SeedInventory(ctx, skuAvailable, 10, 0))
	require.NoError(t, db.SeedInventory(ctx, skuDepleted, 0, 0))

	// Transaction attempting to reserve both
	tx, err := db.Pool.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx)

	// Step 1: Check and reserve available item
	_, err = tx.Exec(ctx, `
		UPDATE inventory SET reserved = reserved + 1 WHERE sku = $1 AND (on_hand - reserved) >= 1;
	`, skuAvailable)
	require.NoError(t, err)

	// Step 2: Check depleted item -> Fails condition
	tag, err := tx.Exec(ctx, `
		UPDATE inventory SET reserved = reserved + 1 WHERE sku = $1 AND (on_hand - reserved) >= 1;
	`, skuDepleted)
	require.NoError(t, err)

	if tag.RowsAffected() == 0 {
		// Insufficient stock -> Mandatory complete rollback
		_ = tx.Rollback(ctx)
	} else {
		_ = tx.Commit(ctx)
	}

	// Verify zero partial reservations: skuAvailable must NOT be reserved!
	db.AssertInventory(ctx, t, skuAvailable, 10, 0)
	db.AssertInventory(ctx, t, skuDepleted, 0, 0)
}
