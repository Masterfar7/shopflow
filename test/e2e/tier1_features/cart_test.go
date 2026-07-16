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

// FEAT-CRT-01: Cart Creation & Ownership-Based Authorization
func TestCartCreationAndOwnership(t *testing.T) {
	env := harness.SetupEnvironment(t)
	client := env.RequireHTTPServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	userID := uuid.New().String()
	cart, prob, status, err := client.CreateCart(ctx, userID)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, status, "expected 201 Created, prob: %+v", prob)
	require.NotNil(t, cart)
	assert.Equal(t, userID, cart.UserID)
	assert.Equal(t, "ACTIVE", cart.Status)
	assert.Equal(t, int64(1), cart.Version)
	assert.Empty(t, cart.Items)
}

// FEAT-CRT-02: Cart Item Add/Update/Remove with Price Snapshotting
func TestCartItemOperations(t *testing.T) {
	env := harness.SetupEnvironment(t)
	client := env.RequireHTTPServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 1. Seed Product
	sku := fmt.Sprintf("SKU-CART-%s", uuid.New().String()[:8])
	prodReq := harness.CreateProductRequest{
		SKU:         sku,
		Title:       "Noise-Cancelling Headphones",
		Description: "Over-ear bluetooth headphones",
		PriceMinor:  29900, // $299.00
		Currency:    "USD",
	}
	prod, _, _, err := client.CreateProduct(ctx, prodReq, uuid.New().String())
	require.NoError(t, err)

	// 2. Create Cart
	userID := uuid.New().String()
	cart, _, _, err := client.CreateCart(ctx, userID)
	require.NoError(t, err)

	// 3. Add Item to Cart
	addReq := harness.AddCartItemRequest{
		SKU:      sku,
		Quantity: 2,
	}
	updatedCart, prob, status, err := client.AddCartItem(ctx, cart.ID, userID, addReq, "1")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status, "expected 200 OK, prob: %+v", prob)
	require.NotNil(t, updatedCart)
	assert.Len(t, updatedCart.Items, 1)
	assert.Equal(t, sku, updatedCart.Items[0].SKU)
	assert.Equal(t, 2, updatedCart.Items[0].Quantity)
	if prod != nil {
		assert.Equal(t, prod.PriceMinor, updatedCart.Items[0].UnitPriceMinor)
	}

	// 4. Update Item Quantity
	updateReq := harness.AddCartItemRequest{
		SKU:      sku,
		Quantity: 5,
	}
	updatedCart2, prob, status, err := client.AddCartItem(ctx, cart.ID, userID, updateReq, fmt.Sprintf("%d", updatedCart.Version))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status, "prob: %+v", prob)
	assert.Equal(t, 5, updatedCart2.Items[0].Quantity)

	// 5. Remove Item from Cart
	status, _, _, err = client.SendRaw(ctx, http.MethodDelete, fmt.Sprintf("/api/v1/carts/%s/items/%s", cart.ID, sku), nil, map[string]string{
		"X-User-ID": userID,
	})
	require.NoError(t, err)
	assert.True(t, status == http.StatusOK || status == http.StatusNoContent)
}

// FEAT-CRT-03: Cart Optimistic Concurrency & Item Lifecycle
func TestCartOptimisticConcurrency(t *testing.T) {
	env := harness.SetupEnvironment(t)
	client := env.RequireHTTPServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	userID := uuid.New().String()
	cart, _, _, err := client.CreateCart(ctx, userID)
	require.NoError(t, err)

	addReq := harness.AddCartItemRequest{
		SKU:      "SKU-ANY",
		Quantity: 1,
	}

	// Send request with mismatched If-Match version
	_, prob, status, err := client.AddCartItem(ctx, cart.ID, userID, addReq, "999999")
	require.NoError(t, err)
	assert.Equal(t, http.StatusConflict, status, "expected 409 Conflict for OCC mismatch on cart")
	if prob != nil {
		assert.NotEmpty(t, prob.Title)
	}
}

// FEAT-CRT-01: Cart Checkout and State Mutation
func TestCartCheckoutLifecycle(t *testing.T) {
	env := harness.SetupEnvironment(t)
	client := env.RequireHTTPServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	userID := uuid.New().String()
	cart, _, _, err := client.CreateCart(ctx, userID)
	require.NoError(t, err)

	// Add item
	sku := fmt.Sprintf("SKU-CO-%s", uuid.New().String()[:8])
	_, _, _, _ = client.CreateProduct(ctx, harness.CreateProductRequest{
		SKU:        sku,
		Title:      "Checkout Item",
		PriceMinor: 5000,
		Currency:   "USD",
	}, uuid.New().String())

	_, _, _, err = client.AddCartItem(ctx, cart.ID, userID, harness.AddCartItemRequest{
		SKU:      sku,
		Quantity: 1,
	}, "1")
	require.NoError(t, err)

	// Checkout cart
	idemKey := uuid.New().String()
	order, prob, status, err := client.CheckoutCart(ctx, cart.ID, userID, idemKey)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, status, "expected 201 Created on checkout, prob: %+v", prob)
	require.NotNil(t, order)
	assert.Equal(t, userID, order.UserID)
	assert.Equal(t, "PENDING", order.Status)
}
