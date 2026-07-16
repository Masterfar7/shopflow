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

// FEAT-CAT-01: SKU & Product Management (Catalog CRUD & queries)
func TestCatalogProductLifecycle(t *testing.T) {
	env := harness.SetupEnvironment(t)
	client := env.RequireHTTPServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	sku := fmt.Sprintf("SKU-TEST-%s", uuid.New().String()[:8])
	req := harness.CreateProductRequest{
		SKU:         sku,
		Title:       "Mechanical Keyboard RGB",
		Description: "Ergonomic clicky mechanical switches",
		PriceMinor:  14999, // $149.99
		Currency:    "USD",
	}

	idemKey := uuid.New().String()
	prod, prob, status, err := client.CreateProduct(ctx, req, idemKey)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, status, "expected 201 Created, got %d (prob: %+v)", status, prob)
	require.NotNil(t, prod)
	assert.Equal(t, sku, prod.SKU)
	assert.Equal(t, int64(14999), prod.PriceMinor)
	assert.Equal(t, "USD", prod.Currency)
	assert.Equal(t, int64(1), prod.Version)

	// Fetch created product
	fetched, prob, status, err := client.GetProduct(ctx, prod.ID)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	require.NotNil(t, fetched)
	assert.Equal(t, prod.ID, fetched.ID)
	assert.Equal(t, prod.Title, fetched.Title)

	// List products
	list, prob, status, err := client.ListProducts(ctx, 10, "")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	require.NotNil(t, list)
	assert.NotEmpty(t, list.Items)
}

// FEAT-CAT-02: Price & Currency Management (Strict int64 minor units, zero floats)
func TestCatalogMoneyIntegerUnits(t *testing.T) {
	env := harness.SetupEnvironment(t)
	client := env.RequireHTTPServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	sku := fmt.Sprintf("SKU-MONEY-%s", uuid.New().String()[:8])
	req := harness.CreateProductRequest{
		SKU:         sku,
		Title:       "Precision Monitor Stand",
		Description: "Solid aluminum stand",
		PriceMinor:  4900, // $49.00 in minor units
		Currency:    "USD",
	}

	prod, prob, status, err := client.CreateProduct(ctx, req, uuid.New().String())
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, status, "expected 201 Created, got prob: %+v", prob)
	require.NotNil(t, prod)
	assert.Equal(t, int64(4900), prod.PriceMinor)
}

// FEAT-CAT-03: Catalog Optimistic Concurrency Control (Version checks / ETag)
func TestCatalogOptimisticConcurrency(t *testing.T) {
	env := harness.SetupEnvironment(t)
	client := env.RequireHTTPServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	sku := fmt.Sprintf("SKU-OCC-%s", uuid.New().String()[:8])
	req := harness.CreateProductRequest{
		SKU:         sku,
		Title:       "Wireless Gaming Mouse",
		Description: "Ultra-low latency sensor",
		PriceMinor:  8999,
		Currency:    "USD",
	}

	prod, prob, status, err := client.CreateProduct(ctx, req, uuid.New().String())
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, status, "prob: %+v", prob)
	require.NotNil(t, prod)

	// Attempt update with mismatched If-Match / version header
	updatePayload := []byte(`{"title":"Updated Gaming Mouse","price_minor":9999,"currency":"USD"}`)
	headers := map[string]string{
		"Content-Type": "application/json",
		"If-Match":     "999", // Mismatched version
	}

	statusCode, _, _, err := client.SendRaw(ctx, http.MethodPut, "/api/v1/products/"+prod.ID, updatePayload, headers)
	require.NoError(t, err)
	assert.Equal(t, http.StatusConflict, statusCode, "expected 409 Conflict for OCC mismatch")
}

// FEAT-CAT-01: Duplicate SKU Rejection
func TestCatalogDuplicateSKURejection(t *testing.T) {
	env := harness.SetupEnvironment(t)
	client := env.RequireHTTPServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	sku := fmt.Sprintf("SKU-DUP-%s", uuid.New().String()[:8])
	req := harness.CreateProductRequest{
		SKU:         sku,
		Title:       "Unique Mug",
		Description: "Ceramic coffee mug",
		PriceMinor:  1500,
		Currency:    "USD",
	}

	_, _, status, err := client.CreateProduct(ctx, req, uuid.New().String())
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, status)

	// Attempt creating another product with the same SKU
	_, prob, dupStatus, err := client.CreateProduct(ctx, req, uuid.New().String())
	require.NoError(t, err)
	assert.Equal(t, http.StatusConflict, dupStatus, "expected 409 Conflict for duplicate SKU")
	if prob != nil {
		assert.NotEmpty(t, prob.Title)
	}
}

// FEAT-CAT-01: Direct Database Invariant Check
func TestCatalogDatabaseInvariants(t *testing.T) {
	env := harness.SetupEnvironment(t)
	db := env.RequireDB(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sku := fmt.Sprintf("SKU-DB-%s", uuid.New().String()[:8])
	err := db.SeedProduct(ctx, sku, "DB Invariant Product", 3500, "USD", 50)
	require.NoError(t, err)

	db.AssertInventory(ctx, t, sku, 50, 0)
	db.AssertInventoryConservation(ctx, t, sku)
}
