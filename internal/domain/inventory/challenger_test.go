package inventory_test

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"shopflow/internal/domain/inventory"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

// TestChallenger_MultiSKU_AllOrNothing_Rollback tests that when any SKU in a multi-item
// reservation is deficient or missing, ALL reservations are rolled back completely (zero partial reservations).
func TestChallenger_MultiSKU_AllOrNothing_Rollback(t *testing.T) {
	svc, repo := setupInventoryService()
	ctx := context.Background()

	t.Run("Ten_SKUs_With_Tenth_SKU_Deficient", func(t *testing.T) {
		skus := make([]string, 10)
		for i := 0; i < 10; i++ {
			skus[i] = fmt.Sprintf("SKU-ALL-NOTHING-%02d", i)
			qty := 10
			if i == 9 {
				qty = 4 // Only 4 available for SKU-09
			}
			_, err := svc.ReplenishStock(ctx, skus[i], qty, "PO-TEST")
			require.NoError(t, err)
		}

		// Request 5 units of each of the 10 SKUs (SKU-09 has only 4)
		reqItems := make([]inventory.StockItemRequest, 10)
		for i := 0; i < 10; i++ {
			reqItems[i] = inventory.StockItemRequest{
				SKU:      skus[i],
				Quantity: 5,
			}
		}

		result, err := svc.ReserveStock(ctx, inventory.ReserveStockRequest{
			OrderID: uuid.New(),
			Items:   reqItems,
		})
		require.Error(t, err)
		assert.Nil(t, result)

		var insufficientErr *inventory.ErrInsufficientStock
		require.True(t, errors.As(err, &insufficientErr), "expected ErrInsufficientStock, got %v", err)
		assert.Equal(t, []string{skus[9]}, insufficientErr.FailedSKUs)

		// EMPIRICAL VERIFICATION: Check all 10 SKUs in repository
		// Absolutely ZERO units must be reserved for any SKU!
		for i := 0; i < 10; i++ {
			item, getErr := repo.GetStock(ctx, skus[i])
			require.NoError(t, getErr)
			assert.Equal(t, 0, item.Reserved, "SKU %s must have 0 reserved units (all-or-nothing rollback violation!)", skus[i])
			if i == 9 {
				assert.Equal(t, 4, item.Available)
			} else {
				assert.Equal(t, 10, item.Available)
			}
		}
	})

	t.Run("Multi_SKU_With_One_SKU_Completely_Missing_From_Catalog", func(t *testing.T) {
		existingSKUs := []string{"SKU-EXIST-1", "SKU-EXIST-2", "SKU-EXIST-3"}
		for _, s := range existingSKUs {
			_, err := svc.ReplenishStock(ctx, s, 20, "PO-EXIST")
			require.NoError(t, err)
		}

		reqItems := []inventory.StockItemRequest{
			{SKU: "SKU-EXIST-1", Quantity: 5},
			{SKU: "SKU-NOT-EXISTS", Quantity: 2}, // Missing from inventory table!
			{SKU: "SKU-EXIST-2", Quantity: 5},
		}

		result, err := svc.ReserveStock(ctx, inventory.ReserveStockRequest{
			OrderID: uuid.New(),
			Items:   reqItems,
		})
		require.Error(t, err)
		assert.Nil(t, result)

		var insufficientErr *inventory.ErrInsufficientStock
		require.True(t, errors.As(err, &insufficientErr))
		assert.Contains(t, insufficientErr.FailedSKUs, "SKU-NOT-EXISTS")

		// Existing SKUs must remain with 0 reserved
		for _, s := range existingSKUs {
			item, getErr := repo.GetStock(ctx, s)
			require.NoError(t, getErr)
			assert.Equal(t, 0, item.Reserved, "SKU %s must have 0 reserved", s)
		}
	})

	t.Run("Duplicate_SKU_Consolidation_Exceeds_Stock_Rolls_Back", func(t *testing.T) {
		sku := "SKU-DUP-CHECK"
		_, err := svc.ReplenishStock(ctx, sku, 10, "PO-DUP")
		require.NoError(t, err)

		// 3 entries for same SKU: 4 + 4 + 4 = 12 (available is 10)
		reqItems := []inventory.StockItemRequest{
			{SKU: sku, Quantity: 4},
			{SKU: sku, Quantity: 4},
			{SKU: sku, Quantity: 4},
		}

		result, err := svc.ReserveStock(ctx, inventory.ReserveStockRequest{
			OrderID: uuid.New(),
			Items:   reqItems,
		})
		require.Error(t, err)
		assert.Nil(t, result)

		item, getErr := repo.GetStock(ctx, sku)
		require.NoError(t, getErr)
		assert.Equal(t, 0, item.Reserved, "SKU must have 0 reserved after duplicate overflow")
		assert.Equal(t, 10, item.Available)
	})
}

// TestChallenger_DeterministicAscendingLocking_NoDeadlocks verifies that concurrent
// reservations across identical and overlapping SKU sets in opposing/random permutations
// never deadlock and always acquire row locks in strict lexicographical ascending order.
func TestChallenger_DeterministicAscendingLocking_NoDeadlocks(t *testing.T) {
	defer goleak.VerifyNone(t)
	svc, repo := setupInventoryService()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	allSKUs := []string{"SKU-A", "SKU-B", "SKU-C", "SKU-D", "SKU-E", "SKU-F"}
	for _, s := range allSKUs {
		_, err := repo.UpsertStock(ctx, s, 1000)
		require.NoError(t, err)
	}

	concurrency := 100
	var wg sync.WaitGroup
	wg.Add(concurrency)

	var successCount atomic.Int64
	var errCount atomic.Int64

	startGate := make(chan struct{})

	for i := 0; i < concurrency; i++ {
		workerID := i
		go func() {
			defer wg.Done()
			<-startGate

			// Create a pseudo-random permutation of 3 SKUs
			r := rand.New(rand.NewSource(int64(workerID * 7919)))
			perm := r.Perm(len(allSKUs))
			selectedSKUs := []string{
				allSKUs[perm[0]],
				allSKUs[perm[1]],
				allSKUs[perm[2]],
			}

			items := make([]inventory.StockItemRequest, len(selectedSKUs))
			for idx, sku := range selectedSKUs {
				items[idx] = inventory.StockItemRequest{
					SKU:      sku,
					Quantity: 1,
				}
			}

			_, err := svc.ReserveStock(ctx, inventory.ReserveStockRequest{
				OrderID: uuid.New(),
				Items:   items,
			})
			if err != nil {
				errCount.Add(1)
			} else {
				successCount.Add(1)
			}
		}()
	}

	close(startGate)
	wg.Wait()

	assert.Equal(t, int64(concurrency), successCount.Load(), "All concurrent reservations must succeed without deadlock")
	assert.Equal(t, int64(0), errCount.Load(), "Zero deadlocks or errors expected")

	// EMPIRICAL VERIFICATION: Every single lock acquisition MUST be sorted ascending
	repo.lockMu.Lock()
	require.NotEmpty(t, repo.lockOrderLog)
	for idx, lockSequence := range repo.lockOrderLog {
		require.True(t, sort.StringsAreSorted(lockSequence),
			"Lock sequence #%d is NOT sorted lexicographically ascending: %v", idx, lockSequence)
	}
	repo.lockMu.Unlock()

	// Defensive check: passing unsorted slice to LockSKUsForUpdate directly must fail
	memBeginner := &memoryTxBeginner{repo: repo}
	tx, err := memBeginner.BeginTx(ctx, pgx.TxOptions{})
	require.NoError(t, err)
	defer tx.Rollback(ctx)

	unsortedSKUs := []string{"SKU-Z", "SKU-A"}
	_, err = repo.LockSKUsForUpdate(ctx, tx, unsortedSKUs)
	require.ErrorIs(t, err, inventory.ErrDeadlockAvoidanceViolation,
		"LockSKUsForUpdate MUST defensively reject unsorted SKU slices")
}

// TestChallenger_PhysicalStockConservation_HighContention tests that under extreme
// concurrency and mixed operations (reserving, committing, releasing), physical stock
// conservation laws are NEVER violated:
// 1. on_hand >= 0
// 2. reserved >= 0
// 3. reserved <= on_hand
// 4. available == on_hand - reserved
// 5. Total committed + total reserved + total available == initial on_hand
func TestChallenger_PhysicalStockConservation_HighContention(t *testing.T) {
	defer goleak.VerifyNone(t)
	svc, repo := setupInventoryService()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	sku := "SKU-CONSERVATION-STRESS"
	initialStock := 50
	_, err := repo.UpsertStock(ctx, sku, initialStock)
	require.NoError(t, err)

	type resInfo struct {
		id  uuid.UUID
		qty int
	}

	var successfulReservations []resInfo
	var resMu sync.Mutex

	concurrency := 150
	var wg sync.WaitGroup
	wg.Add(concurrency)

	var successResCount atomic.Int64
	var rejectedResCount atomic.Int64

	startGate := make(chan struct{})

	for i := 0; i < concurrency; i++ {
		workerID := i
		go func() {
			defer wg.Done()
			<-startGate

			reqQty := (workerID % 5) + 1 // 1 to 5 units
			res, err := svc.ReserveStock(ctx, inventory.ReserveStockRequest{
				OrderID: uuid.New(),
				Items: []inventory.StockItemRequest{
					{SKU: sku, Quantity: reqQty},
				},
			})
			if err == nil {
				successResCount.Add(1)
				resMu.Lock()
				successfulReservations = append(successfulReservations, resInfo{id: res.ReservationID, qty: reqQty})
				resMu.Unlock()
			} else {
				rejectedResCount.Add(1)
			}
		}()
	}

	close(startGate)
	wg.Wait()

	// Verify interim stock conservation
	itemMid, err := repo.GetStock(ctx, sku)
	require.NoError(t, err)
	assert.True(t, itemMid.OnHand >= 0, "OnHand must be >= 0")
	assert.True(t, itemMid.Reserved >= 0, "Reserved must be >= 0")
	assert.True(t, itemMid.Reserved <= itemMid.OnHand, "Reserved must be <= OnHand (Anti-overselling invariant)")
	assert.Equal(t, itemMid.OnHand-itemMid.Reserved, itemMid.Available, "Available must equal OnHand - Reserved")

	// Calculate sum of reserved units from successful reservations
	var totalReservedExpected int
	for _, r := range successfulReservations {
		totalReservedExpected += r.qty
	}
	assert.Equal(t, totalReservedExpected, itemMid.Reserved, "Reserved stock in repo must match sum of successful reservations")
	assert.Equal(t, initialStock, itemMid.OnHand, "OnHand must not decrease during reservation phase")

	// Phase 2: Concurrently commit 50% of reservations and release 50%
	var wg2 sync.WaitGroup
	wg2.Add(len(successfulReservations))

	var committedQty atomic.Int64
	var releasedQty atomic.Int64

	for idx, r := range successfulReservations {
		curIdx := idx
		curRes := r
		go func() {
			defer wg2.Done()
			if curIdx%2 == 0 {
				commitErr := svc.CommitStock(ctx, curRes.id)
				if commitErr == nil {
					committedQty.Add(int64(curRes.qty))
				}
			} else {
				releaseErr := svc.ReleaseStock(ctx, curRes.id, "stress release test")
				if releaseErr == nil {
					releasedQty.Add(int64(curRes.qty))
				}
			}
		}()
	}

	wg2.Wait()

	// Final verification of physical stock conservation
	itemFinal, err := repo.GetStock(ctx, sku)
	require.NoError(t, err)
	assert.True(t, itemFinal.OnHand >= 0, "Final OnHand must be >= 0")
	assert.True(t, itemFinal.Reserved >= 0, "Final Reserved must be >= 0")
	assert.Equal(t, 0, itemFinal.Reserved, "All reservations were either committed or released, reserved must be 0")
	assert.Equal(t, int(int64(initialStock)-committedQty.Load()), itemFinal.OnHand,
		"Final OnHand must equal initial stock minus committed quantity")
	assert.Equal(t, itemFinal.OnHand, itemFinal.Available, "Final Available must equal final OnHand")
}

// TestChallenger_InputValidation_And_BoundaryCases validates boundary cases and input guards.
func TestChallenger_InputValidation_And_BoundaryCases(t *testing.T) {
	svc, repo := setupInventoryService()
	ctx := context.Background()

	sku := "SKU-BOUND"
	_, err := repo.UpsertStock(ctx, sku, 10)
	require.NoError(t, err)

	t.Run("Zero_Quantity_Rejected", func(t *testing.T) {
		_, err := svc.ReserveStock(ctx, inventory.ReserveStockRequest{
			OrderID: uuid.New(),
			Items:   []inventory.StockItemRequest{{SKU: sku, Quantity: 0}},
		})
		assert.ErrorIs(t, err, inventory.ErrInvalidQuantity)
	})

	t.Run("Negative_Quantity_Rejected", func(t *testing.T) {
		_, err := svc.ReserveStock(ctx, inventory.ReserveStockRequest{
			OrderID: uuid.New(),
			Items:   []inventory.StockItemRequest{{SKU: sku, Quantity: -5}},
		})
		assert.ErrorIs(t, err, inventory.ErrInvalidQuantity)
	})

	t.Run("Empty_SKU_Rejected", func(t *testing.T) {
		_, err := svc.ReserveStock(ctx, inventory.ReserveStockRequest{
			OrderID: uuid.New(),
			Items:   []inventory.StockItemRequest{{SKU: "   ", Quantity: 1}},
		})
		assert.ErrorIs(t, err, inventory.ErrEmptySKU)
	})

	t.Run("Empty_Items_Rejected", func(t *testing.T) {
		_, err := svc.ReserveStock(ctx, inventory.ReserveStockRequest{
			OrderID: uuid.New(),
			Items:   []inventory.StockItemRequest{},
		})
		assert.ErrorIs(t, err, inventory.ErrEmptyItems)
	})

	t.Run("Nil_OrderID_Rejected", func(t *testing.T) {
		_, err := svc.ReserveStock(ctx, inventory.ReserveStockRequest{
			OrderID: uuid.Nil,
			Items:   []inventory.StockItemRequest{{SKU: sku, Quantity: 1}},
		})
		assert.ErrorIs(t, err, inventory.ErrInvalidOrderID)
	})

	t.Run("Exact_Stock_Boundary_Succeeds", func(t *testing.T) {
		// Exactly 10 units available -> reserve all 10
		res, err := svc.ReserveStock(ctx, inventory.ReserveStockRequest{
			OrderID: uuid.New(),
			Items:   []inventory.StockItemRequest{{SKU: sku, Quantity: 10}},
		})
		require.NoError(t, err)
		assert.NotNil(t, res)

		item, err := repo.GetStock(ctx, sku)
		require.NoError(t, err)
		assert.Equal(t, 10, item.OnHand)
		assert.Equal(t, 10, item.Reserved)
		assert.Equal(t, 0, item.Available, "Available must be exactly 0 after reserving all")

		// Release it to restore
		err = svc.ReleaseStock(ctx, res.ReservationID, "restore")
		require.NoError(t, err)
	})

	t.Run("Available_Plus_One_Fails", func(t *testing.T) {
		// 10 units available -> request 11
		_, err := svc.ReserveStock(ctx, inventory.ReserveStockRequest{
			OrderID: uuid.New(),
			Items:   []inventory.StockItemRequest{{SKU: sku, Quantity: 11}},
		})
		require.Error(t, err)
		var insufficient *inventory.ErrInsufficientStock
		require.True(t, errors.As(err, &insufficient))
	})
}

// TestChallenger_LifecycleIdempotency_And_StateTransitions verifies idempotency of commit/release
// and strict rejection of illegal state transitions.
func TestChallenger_LifecycleIdempotency_And_StateTransitions(t *testing.T) {
	svc, repo := setupInventoryService()
	ctx := context.Background()

	sku := "SKU-LIFECYCLE-IDEM"
	_, err := repo.UpsertStock(ctx, sku, 100)
	require.NoError(t, err)

	res, err := svc.ReserveStock(ctx, inventory.ReserveStockRequest{
		OrderID: uuid.New(),
		Items:   []inventory.StockItemRequest{{SKU: sku, Quantity: 25}},
	})
	require.NoError(t, err)

	// 1. Commit first time -> on_hand becomes 75, reserved becomes 0
	err = svc.CommitStock(ctx, res.ReservationID)
	require.NoError(t, err)

	itemAfterCommit, _ := repo.GetStock(ctx, sku)
	assert.Equal(t, 75, itemAfterCommit.OnHand)
	assert.Equal(t, 0, itemAfterCommit.Reserved)

	// 2. Commit second time -> Idempotent success, NO double decrement!
	err = svc.CommitStock(ctx, res.ReservationID)
	require.NoError(t, err, "CommitStock must be idempotent")

	itemAfterSecondCommit, _ := repo.GetStock(ctx, sku)
	assert.Equal(t, 75, itemAfterSecondCommit.OnHand, "OnHand must NOT decrement a second time")
	assert.Equal(t, 0, itemAfterSecondCommit.Reserved)

	// 3. Attempt to release committed reservation -> Must fail with ErrReservationAlreadyCommitted
	err = svc.ReleaseStock(ctx, res.ReservationID, "attempt illegal release")
	require.ErrorIs(t, err, inventory.ErrReservationAlreadyCommitted)

	// 4. Create another reservation and release it
	res2, err := svc.ReserveStock(ctx, inventory.ReserveStockRequest{
		OrderID: uuid.New(),
		Items:   []inventory.StockItemRequest{{SKU: sku, Quantity: 15}},
	})
	require.NoError(t, err)

	err = svc.ReleaseStock(ctx, res2.ReservationID, "cancelled")
	require.NoError(t, err)

	itemAfterRelease, _ := repo.GetStock(ctx, sku)
	assert.Equal(t, 75, itemAfterRelease.OnHand)
	assert.Equal(t, 0, itemAfterRelease.Reserved)
	assert.Equal(t, 75, itemAfterRelease.Available)

	// 5. Release second time -> Idempotent success, NO double increment!
	err = svc.ReleaseStock(ctx, res2.ReservationID, "cancelled again")
	require.NoError(t, err, "ReleaseStock must be idempotent")

	itemAfterSecondRelease, _ := repo.GetStock(ctx, sku)
	assert.Equal(t, 0, itemAfterSecondRelease.Reserved)
	assert.Equal(t, 75, itemAfterSecondRelease.Available)

	// 6. Attempt to commit released reservation -> Must fail with ErrReservationAlreadyReleased
	err = svc.CommitStock(ctx, res2.ReservationID)
	require.ErrorIs(t, err, inventory.ErrReservationAlreadyReleased)

	// 7. Non-existent reservation -> ErrReservationNotFound
	err = svc.CommitStock(ctx, uuid.New())
	require.ErrorIs(t, err, inventory.ErrReservationNotFound)

	err = svc.ReleaseStock(ctx, uuid.New(), "test")
	require.ErrorIs(t, err, inventory.ErrReservationNotFound)
}
