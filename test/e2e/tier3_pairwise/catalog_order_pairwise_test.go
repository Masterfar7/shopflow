package tier3_pairwise

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

// Invariant: Subsequent catalog modifications NEVER alter historical order snapshots or totals.
// Reference: SPEC §52, PROJECT.md §10
func TestCatalogModificationsNeverAlterExistingOrders(t *testing.T) {
	env := harness.SetupEnvironment(t)
	client := env.RequireHTTPServer(t)
	db := env.RequireDB(t)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	sku := fmt.Sprintf("SKU-PAIR-CAT-%s", uuid.New().String()[:8])
	initialPriceMinor := int64(1000) // $10.00
	initialTitle := "Original Vintage Desk Lamp"

	// 1. Create Product in Catalog
	prodReq := harness.CreateProductRequest{
		SKU:         sku,
		Title:       initialTitle,
		Description: "Brass desk lamp",
		PriceMinor:  initialPriceMinor,
		Currency:    "USD",
	}
	prod, _, status, err := client.CreateProduct(ctx, prodReq, uuid.New().String())
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, status)
	require.NotNil(t, prod)

	// Seed inventory stock so order can be created
	require.NoError(t, db.SeedInventory(ctx, sku, 100, 0))

	// 2. Customer 1 places Order 1 for 2 units
	user1 := uuid.New().String()
	order1Req := harness.CreateOrderRequest{
		Currency: "USD",
		Items: []harness.OrderItemRequest{
			{SKU: sku, Quantity: 2},
		},
	}
	order1, _, status, err := client.CreateOrder(ctx, user1, order1Req, uuid.New().String())
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, status)
	require.NotNil(t, order1)
	assert.Equal(t, int64(2000), order1.TotalAmountMinor, "Order 1 total must be $20.00 (2000 minor)")
	require.Len(t, order1.Items, 1)
	assert.Equal(t, initialTitle, order1.Items[0].TitleSnapshot)
	assert.Equal(t, initialPriceMinor, order1.Items[0].UnitPriceMinor)

	// 3. Catalog Admin updates Product title and price to $75.00 (7500 minor)
	updatedPriceMinor := int64(7500)
	updatedTitle := "Deluxe Modernized Brass Lamp"
	updatePayload := fmt.Sprintf(`{
		"title": "%s",
		"description": "Updated brass lamp",
		"price_minor": %d,
		"currency": "USD"
	}`, updatedTitle, updatedPriceMinor)

	putStatus, _, _, err := client.SendRaw(ctx, http.MethodPut, "/api/v1/products/"+prod.ID, []byte(updatePayload), map[string]string{
		"Content-Type": "application/json",
		"If-Match":     fmt.Sprintf("%d", prod.Version),
	})
	require.NoError(t, err)
	assert.True(t, putStatus == http.StatusOK || putStatus == http.StatusNoContent)

	// 4. Verification Check 1: Order 1 MUST preserve original historical snapshot!
	fetchedOrder1, _, status, err := client.GetOrder(ctx, order1.ID, user1)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	require.NotNil(t, fetchedOrder1)
	assert.Equal(t, int64(2000), fetchedOrder1.TotalAmountMinor, "CRITICAL INVARIANT VIOLATION: Order 1 total was altered by catalog change!")
	require.Len(t, fetchedOrder1.Items, 1)
	assert.Equal(t, initialTitle, fetchedOrder1.Items[0].TitleSnapshot, "Historical line item title must remain immutable")
	assert.Equal(t, initialPriceMinor, fetchedOrder1.Items[0].UnitPriceMinor, "Historical line item price must remain immutable")

	// 5. Verification Check 2: Direct Database Invariant
	db.AssertOrder(ctx, t, order1.ID, "PENDING", 2000)
	db.AssertOrderTotalsInvariant(ctx, t, order1.ID)

	// 6. Verification Check 3: Customer 2 places Order 2 and receives NEW catalog price
	user2 := uuid.New().String()
	order2Req := harness.CreateOrderRequest{
		Currency: "USD",
		Items: []harness.OrderItemRequest{
			{SKU: sku, Quantity: 1},
		},
	}
	order2, _, status, err := client.CreateOrder(ctx, user2, order2Req, uuid.New().String())
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, status)
	require.NotNil(t, order2)
	assert.Equal(t, updatedPriceMinor, order2.TotalAmountMinor, "Order 2 must reflect updated catalog price")
	require.Len(t, order2.Items, 1)
	assert.Equal(t, updatedTitle, order2.Items[0].TitleSnapshot)
}
