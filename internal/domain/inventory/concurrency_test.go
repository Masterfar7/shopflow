package inventory_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"shopflow/internal/domain/inventory"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestReverseOrderLockContention verifies that when concurrent goroutines submit multi-SKU
// reservation requests in opposing order (e.g. [A, B, C] vs [C, B, A]), zero deadlocks occur
// because internal deterministic sorting forces identical ascending lock acquisition order.
func TestReverseOrderLockContention(t *testing.T) {
	svc, repo := setupInventoryService()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, _ = repo.UpsertStock(ctx, "SKU-A", 100)
	_, _ = repo.UpsertStock(ctx, "SKU-B", 100)
	_, _ = repo.UpsertStock(ctx, "SKU-C", 100)

	iterations := 50
	var wg sync.WaitGroup
	wg.Add(iterations * 2)

	var successCount atomic.Int64
	var errCount atomic.Int64

	startBarrier := make(chan struct{})

	// Worker group 1: requests A, B, C
	for i := 0; i < iterations; i++ {
		go func() {
			defer wg.Done()
			<-startBarrier

			_, err := svc.ReserveStock(ctx, inventory.ReserveStockRequest{
				OrderID: uuid.New(),
				Items: []inventory.StockItemRequest{
					{SKU: "SKU-A", Quantity: 1},
					{SKU: "SKU-B", Quantity: 1},
					{SKU: "SKU-C", Quantity: 1},
				},
			})
			if err != nil {
				errCount.Add(1)
			} else {
				successCount.Add(1)
			}
		}()
	}

	// Worker group 2: requests in reverse order C, B, A
	for i := 0; i < iterations; i++ {
		go func() {
			defer wg.Done()
			<-startBarrier

			_, err := svc.ReserveStock(ctx, inventory.ReserveStockRequest{
				OrderID: uuid.New(),
				Items: []inventory.StockItemRequest{
					{SKU: "SKU-C", Quantity: 1},
					{SKU: "SKU-B", Quantity: 1},
					{SKU: "SKU-A", Quantity: 1},
				},
			})
			if err != nil {
				errCount.Add(1)
			} else {
				successCount.Add(1)
			}
		}()
	}

	close(startBarrier)
	wg.Wait()

	assert.Equal(t, int64(100), successCount.Load(), "All 100 reservations should succeed without deadlocks")
	assert.Equal(t, int64(0), errCount.Load(), "Zero deadlocks or errors expected")

	// Verify all locked orders recorded were strictly ascending
	repo.lockMu.Lock()
	for _, order := range repo.lockOrderLog {
		require.Equal(t, []string{"SKU-A", "SKU-B", "SKU-C"}, order, "All lock acquisitions MUST be sorted ascending")
	}
	repo.lockMu.Unlock()
}

// Test100ConcurrentReservationsFor10Items reproduces the definitive concurrency workload:
// 100 concurrent reservation requests competing for 10 units of a single SKU.
// Expected outcome: Exactly 10 successes, exactly 90 insufficient stock rejections, exactly 0 overselling.
func Test100ConcurrentReservationsFor10Items(t *testing.T) {
	svc, repo := setupInventoryService()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	sku := "SKU-STRESS-10"
	initialOnHand := 10
	_, err := repo.UpsertStock(ctx, sku, initialOnHand)
	require.NoError(t, err)

	concurrency := 100
	var successCount atomic.Int64
	var insufficientStockCount atomic.Int64
	var otherErrorCount atomic.Int64

	var wg sync.WaitGroup
	wg.Add(concurrency)
	startBarrier := make(chan struct{})

	for i := 0; i < concurrency; i++ {
		go func() {
			defer wg.Done()
			<-startBarrier

			orderID := uuid.New()
			_, err := svc.ReserveStock(ctx, inventory.ReserveStockRequest{
				OrderID: orderID,
				Items: []inventory.StockItemRequest{
					{SKU: sku, Quantity: 1},
				},
			})

			if err == nil {
				successCount.Add(1)
			} else {
				var insufficientErr *inventory.ErrInsufficientStock
				if errors.As(err, &insufficientErr) {
					insufficientStockCount.Add(1)
				} else {
					otherErrorCount.Add(1)
				}
			}
		}()
	}

	close(startBarrier)
	wg.Wait()

	assert.Equal(t, int64(10), successCount.Load(), "Exactly 10 workers must successfully reserve stock")
	assert.Equal(t, int64(90), insufficientStockCount.Load(), "Exactly 90 workers must receive insufficient stock")
	assert.Equal(t, int64(0), otherErrorCount.Load(), "Zero unexpected errors or deadlocks")

	// Verify database invariant: on_hand = 10, reserved = 10, available = 0
	item, err := repo.GetStock(ctx, sku)
	require.NoError(t, err)
	assert.Equal(t, 10, item.OnHand)
	assert.Equal(t, 10, item.Reserved)
	assert.Equal(t, 0, item.Available)
}

// TestConcurrentCommitAndReleaseContention validates that concurrent commits and releases
// maintain physical stock conservation invariants across high contention.
func TestConcurrentCommitAndReleaseContention(t *testing.T) {
	svc, repo := setupInventoryService()
	ctx := context.Background()

	sku := "SKU-CONC-CR"
	_, err := repo.UpsertStock(ctx, sku, 200)
	require.NoError(t, err)

	var reservationIDs []uuid.UUID
	for i := 0; i < 50; i++ {
		resID := uuid.New()
		_, err := svc.ReserveStock(ctx, inventory.ReserveStockRequest{
			ReservationID: resID,
			OrderID:       uuid.New(),
			Items: []inventory.StockItemRequest{
				{SKU: sku, Quantity: 2},
			},
		})
		require.NoError(t, err)
		reservationIDs = append(reservationIDs, resID)
	}

	var wg sync.WaitGroup
	// 25 workers commit, 25 workers release concurrently
	for i := 0; i < 25; i++ {
		resID := reservationIDs[i]
		wg.Add(1)
		go func(id uuid.UUID) {
			defer wg.Done()
			err := svc.CommitStock(ctx, id)
			assert.NoError(t, err)
		}(resID)
	}

	for i := 25; i < 50; i++ {
		resID := reservationIDs[i]
		wg.Add(1)
		go func(id uuid.UUID) {
			defer wg.Done()
			err := svc.ReleaseStock(ctx, id, "cancelled")
			assert.NoError(t, err)
		}(resID)
	}

	wg.Wait()

	// 25 committed (25 * 2 = 50 units permanently consumed from on_hand)
	// 25 released (25 * 2 = 50 units returned to available)
	// Final expected: on_hand = 150, reserved = 0, available = 150
	item, err := repo.GetStock(ctx, sku)
	require.NoError(t, err)
	assert.Equal(t, 150, item.OnHand)
	assert.Equal(t, 0, item.Reserved)
	assert.Equal(t, 150, item.Available)
}
