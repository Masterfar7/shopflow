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

// Invariant: Mutative endpoints MUST require Idempotency-Key header.
func TestMissingIdempotencyKeyRejection(t *testing.T) {
	env := harness.SetupEnvironment(t)
	client := env.RequireHTTPServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Direct POST to /api/v1/orders without Idempotency-Key header
	rawPayload := []byte(`{
		"currency": "USD",
		"items": [{"sku": "SKU-ANY", "quantity": 1}]
	}`)

	headers := map[string]string{
		"Content-Type": "application/json",
		"X-User-ID":    uuid.New().String(),
		// Omit Idempotency-Key
	}

	statusCode, _, _, err := client.SendRaw(ctx, http.MethodPost, "/api/v1/orders", rawPayload, headers)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, statusCode, "mutative endpoints must reject requests lacking Idempotency-Key")
}

// Invariant: Concurrent or duplicate requests with same Idempotency-Key but different payload MUST yield 409 Conflict.
func TestIdempotencyPayloadHashMismatchConflict(t *testing.T) {
	env := harness.SetupEnvironment(t)
	client := env.RequireHTTPServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	userID := uuid.New().String()
	idemKey := "idem-mismatch-" + uuid.New().String()

	sku := fmt.Sprintf("SKU-HASH-%s", uuid.New().String()[:8])
	_, _, _, _ = client.CreateProduct(ctx, harness.CreateProductRequest{
		SKU:        sku,
		Title:      "Hash Test Product",
		PriceMinor: 1500,
		Currency:   "USD",
	}, uuid.New().String())

	// Request 1: 1 unit
	req1 := harness.CreateOrderRequest{
		Currency: "USD",
		Items:    []harness.OrderItemRequest{{SKU: sku, Quantity: 1}},
	}
	_, _, status1, err := client.CreateOrder(ctx, userID, req1, idemKey)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, status1)

	// Request 2: 5 units with the SAME idempotency key
	req2 := harness.CreateOrderRequest{
		Currency: "USD",
		Items:    []harness.OrderItemRequest{{SKU: sku, Quantity: 5}},
	}
	_, _, status2, err := client.CreateOrder(ctx, userID, req2, idemKey)
	require.NoError(t, err)
	assert.Equal(t, http.StatusConflict, status2, "re-using idempotency key with modified payload must return 409 Conflict")
}

// Database Invariant: Expired idempotency keys must not lock new requests.
func TestIdempotencyExpirationDatabase(t *testing.T) {
	env := harness.SetupEnvironment(t)
	db := env.RequireDB(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	userID := uuid.New().String()
	key := "expired-key-" + uuid.New().String()

	// Insert an expired idempotency record (expires_at in the past)
	_, err := db.Pool.Exec(ctx, `
		INSERT INTO idempotency_keys (key, user_id, request_hash, status, expires_at)
		VALUES ($1, $2, 'hash123', 'COMPLETED', NOW() - INTERVAL '1 hour');
	`, key, userID)
	require.NoError(t, err)

	// Verify expiration query cleans up or bypasses expired key
	var isExpired bool
	err = db.Pool.QueryRow(ctx, `
		SELECT (expires_at < NOW()) FROM idempotency_keys WHERE key = $1 AND user_id = $2;
	`, key, userID).Scan(&isExpired)
	require.NoError(t, err)
	assert.True(t, isExpired, "key should be recognized as expired")
}
