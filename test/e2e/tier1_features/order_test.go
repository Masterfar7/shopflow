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

// FEAT-ORD-01: Atomic Multi-Item Order Creation with Server-Side Totals
func TestOrderCreationAndTotalsCalculation(t *testing.T) {
	env := harness.SetupEnvironment(t)
	client := env.RequireHTTPServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	userID := uuid.New().String()
	sku1 := fmt.Sprintf("SKU-ORD-1-%s", uuid.New().String()[:8])
	sku2 := fmt.Sprintf("SKU-ORD-2-%s", uuid.New().String()[:8])

	// Seed products
	_, _, _, _ = client.CreateProduct(ctx, harness.CreateProductRequest{
		SKU:        sku1,
		Title:      "Product 1",
		PriceMinor: 2500, // $25.00
		Currency:   "USD",
	}, uuid.New().String())
	_, _, _, _ = client.CreateProduct(ctx, harness.CreateProductRequest{
		SKU:        sku2,
		Title:      "Product 2",
		PriceMinor: 1000, // $10.00
		Currency:   "USD",
	}, uuid.New().String())

	// Create order with 2 items: 2x sku1 ($50.00) + 3x sku2 ($30.00) = $80.00 (8000 minor units)
	orderReq := harness.CreateOrderRequest{
		Currency: "USD",
		Items: []harness.OrderItemRequest{
			{SKU: sku1, Quantity: 2},
			{SKU: sku2, Quantity: 3},
		},
	}

	idemKey := uuid.New().String()
	order, prob, status, err := client.CreateOrder(ctx, userID, orderReq, idemKey)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, status, "expected 201 Created, prob: %+v", prob)
	require.NotNil(t, order)
	assert.Equal(t, userID, order.UserID)
	assert.Equal(t, "PENDING", order.Status)
	assert.Equal(t, int64(8000), order.TotalAmountMinor, "server-side totals calculation mismatch: expected 8000")
	assert.Len(t, order.Items, 2)
}

// FEAT-ORD-02: Immutable Price & Title Snapshotting on Line Items
func TestOrderImmutableSnapshots(t *testing.T) {
	env := harness.SetupEnvironment(t)
	client := env.RequireHTTPServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	sku := fmt.Sprintf("SKU-SNAP-%s", uuid.New().String()[:8])
	prodReq := harness.CreateProductRequest{
		SKU:        sku,
		Title:      "Snapshot Master Item",
		PriceMinor: 4500,
		Currency:   "USD",
	}
	_, _, _, _ = client.CreateProduct(ctx, prodReq, uuid.New().String())

	userID := uuid.New().String()
	orderReq := harness.CreateOrderRequest{
		Currency: "USD",
		Items: []harness.OrderItemRequest{
			{SKU: sku, Quantity: 1},
		},
	}
	order, prob, status, err := client.CreateOrder(ctx, userID, orderReq, uuid.New().String())
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, status, "prob: %+v", prob)
	require.NotNil(t, order)
	require.Len(t, order.Items, 1)

	assert.Equal(t, "Snapshot Master Item", order.Items[0].TitleSnapshot)
	assert.Equal(t, int64(4500), order.Items[0].UnitPriceMinor)
	assert.Equal(t, int64(4500), order.Items[0].SubtotalMinor)
}

// FEAT-ORD-04: REST Idempotency Key Handling with DB Locking & Response Caching
func TestOrderIdempotencyKeyCaching(t *testing.T) {
	env := harness.SetupEnvironment(t)
	client := env.RequireHTTPServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	userID := uuid.New().String()
	sku := fmt.Sprintf("SKU-IDEM-%s", uuid.New().String()[:8])
	_, _, _, _ = client.CreateProduct(ctx, harness.CreateProductRequest{
		SKU:        sku,
		Title:      "Idempotent Product",
		PriceMinor: 1200,
		Currency:   "USD",
	}, uuid.New().String())

	orderReq := harness.CreateOrderRequest{
		Currency: "USD",
		Items: []harness.OrderItemRequest{
			{SKU: sku, Quantity: 1},
		},
	}
	idemKey := "idem-order-" + uuid.New().String()

	// 1. Initial Request
	order1, _, status1, err := client.CreateOrder(ctx, userID, orderReq, idemKey)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, status1)
	require.NotNil(t, order1)

	// 2. Exact Duplicate Request with Same Idempotency-Key
	order2, _, status2, err := client.CreateOrder(ctx, userID, orderReq, idemKey)
	require.NoError(t, err)
	assert.True(t, status2 == http.StatusCreated || status2 == http.StatusOK, "cached response status: %d", status2)
	require.NotNil(t, order2)
	assert.Equal(t, order1.ID, order2.ID, "idempotent retry must return the exact same order ID")
	assert.Equal(t, order1.TotalAmountMinor, order2.TotalAmountMinor)

	// 3. Request with Same Idempotency-Key but DIFFERENT payload -> Must return 409 Conflict
	tamperedReq := harness.CreateOrderRequest{
		Currency: "USD",
		Items: []harness.OrderItemRequest{
			{SKU: sku, Quantity: 99}, // Changed quantity
		},
	}
	_, prob, status3, err := client.CreateOrder(ctx, userID, tamperedReq, idemKey)
	require.NoError(t, err)
	assert.Equal(t, http.StatusConflict, status3, "re-using idempotency key with different payload must fail with 409 Conflict")
	if prob != nil {
		assert.NotEmpty(t, prob.Title)
	}
}

// FEAT-ORD-01 / FEAT-ORD-02: Direct Database Totals Invariant Assertion
func TestOrderDatabaseTotalsInvariant(t *testing.T) {
	env := harness.SetupEnvironment(t)
	db := env.RequireDB(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Seed order and line items directly
	orderID := uuid.New().String()
	userID := uuid.New().String()
	idemKey := uuid.New().String()

	tx, err := db.Pool.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx)

	_, err = tx.Exec(ctx, `
		INSERT INTO orders (id, user_id, idempotency_key, status, total_amount_minor, currency)
		VALUES ($1, $2, $3, 'PENDING', 7000, 'USD');
	`, orderID, userID, idemKey)
	require.NoError(t, err)

	_, err = tx.Exec(ctx, `
		INSERT INTO order_items (order_id, sku, title_snapshot, unit_price_minor, quantity, subtotal_minor)
		VALUES
			($1, 'SKU-A', 'Item A', 2000, 2, 4000),
			($1, 'SKU-B', 'Item B', 3000, 1, 3000);
	`, orderID)
	require.NoError(t, err)

	err = tx.Commit(ctx)
	require.NoError(t, err)

	// Assert order invariants
	db.AssertOrder(ctx, t, orderID, "PENDING", 7000)
	db.AssertOrderTotalsInvariant(ctx, t, orderID)
}
