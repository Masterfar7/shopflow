package tier3_pairwise

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"shopflow/test/e2e/harness"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Invariant: Concurrent cart modifications must enforce OCC via version checks.
func TestConcurrentCartModificationsOCC(t *testing.T) {
	env := harness.SetupEnvironment(t)
	client := env.RequireHTTPServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	userID := uuid.New().String()
	cart, _, status, err := client.CreateCart(ctx, userID)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, status)

	sku1 := fmt.Sprintf("SKU-CONC-1-%s", uuid.New().String()[:8])
	sku2 := fmt.Sprintf("SKU-CONC-2-%s", uuid.New().String()[:8])
	_, _, _, _ = client.CreateProduct(ctx, harness.CreateProductRequest{SKU: sku1, Title: "Item 1", PriceMinor: 1000, Currency: "USD"}, uuid.New().String())
	_, _, _, _ = client.CreateProduct(ctx, harness.CreateProductRequest{SKU: sku2, Title: "Item 2", PriceMinor: 2000, Currency: "USD"}, uuid.New().String())

	// Concurrently attempt to add items using the SAME initial version ("1")
	initialVersion := "1"
	var wg sync.WaitGroup
	var statuses = make([]int, 2)

	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _, s, _ := client.AddCartItem(ctx, cart.ID, userID, harness.AddCartItemRequest{SKU: sku1, Quantity: 1}, initialVersion)
		statuses[0] = s
	}()

	go func() {
		defer wg.Done()
		_, _, s, _ := client.AddCartItem(ctx, cart.ID, userID, harness.AddCartItemRequest{SKU: sku2, Quantity: 1}, initialVersion)
		statuses[1] = s
	}()

	wg.Wait()

	// Exactly one must succeed (200 OK), and the conflicting one must fail with 409 Conflict
	successCount := 0
	conflictCount := 0
	for _, s := range statuses {
		if s == http.StatusOK || s == http.StatusCreated {
			successCount++
		} else if s == http.StatusConflict {
			conflictCount++
		}
	}

	assert.Equal(t, 1, successCount, "exactly one concurrent cart modification should succeed with version 1")
	assert.Equal(t, 1, conflictCount, "conflicting concurrent cart modification must encounter 409 Conflict")
}
