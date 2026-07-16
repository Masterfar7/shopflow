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

// Invariant: Checkout on an empty cart MUST be rejected with HTTP 400 or 422.
func TestEmptyCartCheckoutRejection(t *testing.T) {
	env := harness.SetupEnvironment(t)
	client := env.RequireHTTPServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	userID := uuid.New().String()
	cart, _, status, err := client.CreateCart(ctx, userID)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, status)

	// Attempt checkout without adding items
	_, prob, checkoutStatus, err := client.CheckoutCart(ctx, cart.ID, userID, uuid.New().String())
	require.NoError(t, err)
	assert.True(t, checkoutStatus == http.StatusBadRequest || checkoutStatus == http.StatusUnprocessableEntity,
		"checkout of empty cart must be rejected, got %d", checkoutStatus)
	if prob != nil {
		assert.NotEmpty(t, prob.Title)
	}
}

// Invariant: Cross-user cart access MUST be rejected with HTTP 403 Forbidden.
func TestCrossUserCartIsolation(t *testing.T) {
	env := harness.SetupEnvironment(t)
	client := env.RequireHTTPServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	userA := uuid.New().String()
	userB := uuid.New().String()

	cartA, _, _, err := client.CreateCart(ctx, userA)
	require.NoError(t, err)

	// User B attempts to access User A's cart
	status, _, _, err := client.SendRaw(ctx, http.MethodGet, fmt.Sprintf("/api/v1/carts/%s", cartA.ID), nil, map[string]string{
		"X-User-ID": userB,
	})
	require.NoError(t, err)
	assert.True(t, status == http.StatusForbidden || status == http.StatusNotFound,
		"cross-user access must be rejected with 403 or 404, got %d", status)

	// User B attempts to add item to User A's cart
	_, _, status, err = client.AddCartItem(ctx, cartA.ID, userB, harness.AddCartItemRequest{
		SKU:      "SKU-ANY",
		Quantity: 1,
	}, "1")
	require.NoError(t, err)
	assert.True(t, status == http.StatusForbidden || status == http.StatusNotFound)
}

// Invariant: Orders exceeding maximum line item limit must be rejected.
func TestOrderMaxItemsLimitRejection(t *testing.T) {
	env := harness.SetupEnvironment(t)
	client := env.RequireHTTPServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Build order with 150 distinct line items (> standard 100 max limit)
	var items []harness.OrderItemRequest
	for i := 0; i < 150; i++ {
		items = append(items, harness.OrderItemRequest{
			SKU:      fmt.Sprintf("SKU-LIMIT-%d", i),
			Quantity: 1,
		})
	}

	orderReq := harness.CreateOrderRequest{
		Currency: "USD",
		Items:    items,
	}

	_, _, status, err := client.CreateOrder(ctx, uuid.New().String(), orderReq, uuid.New().String())
	require.NoError(t, err)
	assert.True(t, status == http.StatusBadRequest || status == http.StatusUnprocessableEntity,
		"exceeding max line items must be rejected, got %d", status)
}
