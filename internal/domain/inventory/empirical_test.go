package inventory_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"shopflow/internal/domain/inventory"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Empirical Test 1: Full stock lifecycle from replenishment to reservation, commit, and release.
func TestEmpirical_StockLifecycle(t *testing.T) {
	svc, repo := setupInventoryService()
	ctx := context.Background()

	sku := fmt.Sprintf("SKU-LIFE-%s", uuid.New().String()[:8])

	// 1. Initial Replenishment
	item, err := svc.ReplenishStock(ctx, sku, 100, "PO-INIT")
	require.NoError(t, err)
	assert.Equal(t, 100, item.OnHand)
	assert.Equal(t, 0, item.Reserved)
	assert.Equal(t, 100, item.Available)

	// 2. Query Initial Stock
	fetched, err := svc.GetStock(ctx, sku)
	require.NoError(t, err)
	assert.Equal(t, 100, fetched.OnHand)
	assert.Equal(t, 0, fetched.Reserved)
	assert.Equal(t, 100, fetched.Available)

	// 3. Reserve 30 units
	resID1 := uuid.New()
	orderID1 := uuid.New()
	resResult1, err := svc.ReserveStock(ctx, inventory.ReserveStockRequest{
		ReservationID: resID1,
		OrderID:       orderID1,
		Items: []inventory.StockItemRequest{
			{SKU: sku, Quantity: 30},
		},
		TTLSeconds: 600,
	})
	require.NoError(t, err)
	assert.Equal(t, inventory.ReservationStatusPending, resResult1.Status)

	// Invariant check: on_hand = 100, reserved = 30, available = 70
	afterRes1, err := repo.GetStock(ctx, sku)
	require.NoError(t, err)
	assert.Equal(t, 100, afterRes1.OnHand)
	assert.Equal(t, 30, afterRes1.Reserved)
	assert.Equal(t, 70, afterRes1.Available)
	assert.True(t, afterRes1.Reserved <= afterRes1.OnHand, "Physical conservation check")

	// 4. Reserve another 20 units for a second order
	resID2 := uuid.New()
	orderID2 := uuid.New()
	resResult2, err := svc.ReserveStock(ctx, inventory.ReserveStockRequest{
		ReservationID: resID2,
		OrderID:       orderID2,
		Items: []inventory.StockItemRequest{
			{SKU: sku, Quantity: 20},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, inventory.ReservationStatusPending, resResult2.Status)

	// Invariant check: on_hand = 100, reserved = 50, available = 50
	afterRes2, err := repo.GetStock(ctx, sku)
	require.NoError(t, err)
	assert.Equal(t, 100, afterRes2.OnHand)
	assert.Equal(t, 50, afterRes2.Reserved)
	assert.Equal(t, 50, afterRes2.Available)

	// 5. Commit first reservation (Order 1 payment confirmed)
	err = svc.CommitStock(ctx, resID1)
	require.NoError(t, err)

	// Invariant check: on_hand = 70, reserved = 20, available = 50
	afterCommit, err := repo.GetStock(ctx, sku)
	require.NoError(t, err)
	assert.Equal(t, 70, afterCommit.OnHand)
	assert.Equal(t, 20, afterCommit.Reserved)
	assert.Equal(t, 50, afterCommit.Available)

	// 6. Release second reservation (Order 2 payment failed / compensation)
	err = svc.ReleaseStock(ctx, resID2, "Payment declined")
	require.NoError(t, err)

	// Invariant check: on_hand = 70, reserved = 0, available = 70
	afterRelease, err := repo.GetStock(ctx, sku)
	require.NoError(t, err)
	assert.Equal(t, 70, afterRelease.OnHand)
	assert.Equal(t, 0, afterRelease.Reserved)
	assert.Equal(t, 70, afterRelease.Available)
}

// Empirical Test 2: Multi-SKU All-or-Nothing Invariant Check
func TestEmpirical_MultiSKU_AllOrNothing(t *testing.T) {
	svc, repo := setupInventoryService()
	ctx := context.Background()

	skuA := "SKU-EMP-A"
	skuB := "SKU-EMP-B"
	skuC := "SKU-EMP-C"

	_, _ = svc.ReplenishStock(ctx, skuA, 10, "PO-A")
	_, _ = svc.ReplenishStock(ctx, skuB, 05, "PO-B")
	_, _ = svc.ReplenishStock(ctx, skuC, 02, "PO-C")

	// Order requests:
	// SKU-A: 5 (sufficient)
	// SKU-B: 3 (sufficient)
	// SKU-C: 5 (INSUFFICIENT, only 2 available!)
	_, err := svc.ReserveStock(ctx, inventory.ReserveStockRequest{
		OrderID: uuid.New(),
		Items: []inventory.StockItemRequest{
			{SKU: skuA, Quantity: 5},
			{SKU: skuB, Quantity: 3},
			{SKU: skuC, Quantity: 5},
		},
	})
	require.Error(t, err)

	var insufficient *inventory.ErrInsufficientStock
	require.True(t, errors.As(err, &insufficient))
	assert.Equal(t, []string{skuC}, insufficient.FailedSKUs)

	// Verify that SKU-A and SKU-B have ZERO units reserved
	itemA, err := repo.GetStock(ctx, skuA)
	require.NoError(t, err)
	assert.Equal(t, 0, itemA.Reserved, "SKU-A must remain at reserved = 0")

	itemB, err := repo.GetStock(ctx, skuB)
	require.NoError(t, err)
	assert.Equal(t, 0, itemB.Reserved, "SKU-B must remain at reserved = 0")

	itemC, err := repo.GetStock(ctx, skuC)
	require.NoError(t, err)
	assert.Equal(t, 0, itemC.Reserved, "SKU-C must remain at reserved = 0")
}

// Empirical Test 3: High Contention Stock Exhaustion
func TestEmpirical_StockExhaustion_Boundary(t *testing.T) {
	svc, repo := setupInventoryService()
	ctx := context.Background()

	sku := "SKU-EXHAUST"
	_, _ = svc.ReplenishStock(ctx, sku, 5, "PO-EX")

	// 5 individual orders reserve 1 unit each
	for i := 0; i < 5; i++ {
		_, err := svc.ReserveStock(ctx, inventory.ReserveStockRequest{
			OrderID: uuid.New(),
			Items: []inventory.StockItemRequest{
				{SKU: sku, Quantity: 1},
			},
		})
		require.NoError(t, err)
	}

	// 6th order attempts to reserve 1 unit -> must fail
	_, err := svc.ReserveStock(ctx, inventory.ReserveStockRequest{
		OrderID: uuid.New(),
		Items: []inventory.StockItemRequest{
			{SKU: sku, Quantity: 1},
		},
	})
	require.Error(t, err)
	var insufficient *inventory.ErrInsufficientStock
	require.True(t, errors.As(err, &insufficient))

	// Release 1 unit from one of the reservations
	resList := make([]uuid.UUID, 0)
	repo.mu.RLock()
	for id := range repo.reservations {
		resList = append(resList, id)
	}
	repo.mu.RUnlock()
	require.Len(t, resList, 5)

	err = svc.ReleaseStock(ctx, resList[0], "customer changed mind")
	require.NoError(t, err)

	// Now 1 unit should be available again!
	result, err := svc.ReserveStock(ctx, inventory.ReserveStockRequest{
		OrderID: uuid.New(),
		Items: []inventory.StockItemRequest{
			{SKU: sku, Quantity: 1},
		},
	})
	require.NoError(t, err)
	assert.NotNil(t, result)
}

// Empirical Test 4: Concurrency Race Invariant Check
func TestEmpirical_ConcurrentInvariants(t *testing.T) {
	svc, repo := setupInventoryService()
	ctx := context.Background()

	sku := "SKU-CONC-INV"
	_, _ = svc.ReplenishStock(ctx, sku, 100, "PO-INV")

	var wg sync.WaitGroup
	workers := 20
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 5; j++ {
				res, err := svc.ReserveStock(ctx, inventory.ReserveStockRequest{
					OrderID: uuid.New(),
					Items: []inventory.StockItemRequest{
						{SKU: sku, Quantity: 1},
					},
				})
				if err == nil {
					// 50% commit, 50% release
					if j%2 == 0 {
						_ = svc.CommitStock(ctx, res.ReservationID)
					} else {
						_ = svc.ReleaseStock(ctx, res.ReservationID, "test release")
					}
				}
			}
		}()
	}

	wg.Wait()

	// Verify stock conservation at the end
	item, err := repo.GetStock(ctx, sku)
	require.NoError(t, err)
	assert.True(t, item.OnHand >= 0, "OnHand must be >= 0")
	assert.True(t, item.Reserved >= 0, "Reserved must be >= 0")
	assert.True(t, item.Reserved <= item.OnHand, "Reserved must be <= OnHand")
	assert.Equal(t, item.OnHand-item.Reserved, item.Available, "Available must equal OnHand - Reserved")
}
