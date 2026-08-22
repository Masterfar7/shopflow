package tier4_workloads

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"shopflow/test/e2e/harness"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The Definitive Concurrency & Anti-Overselling Workload
// Specification: 100 concurrent reservation requests competing for 10 items
// Expected Outcome: Exactly 10 successes, exactly 90 rejections, exactly 0 overselling.
// References: SPEC §53, PROJECT.md §65, Architecture Guidelines §3.1
func Test100ConcurrentReservationsFor10Items(t *testing.T) {
	env := harness.SetupEnvironment(t)
	db := env.RequireDB(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	sku := fmt.Sprintf("SKU-STRESS-100-%s", uuid.New().String()[:8])
	initialOnHand := 10
	initialReserved := 0

	// 1. Seed initial inventory: 10 available units
	require.NoError(t, db.SeedInventory(ctx, sku, initialOnHand, initialReserved))

	// 2. Concurrency stress parameters
	concurrency := 100
	var successCount atomic.Int64
	var insufficientStockCount atomic.Int64
	var errorCount atomic.Int64

	var wg sync.WaitGroup
	startBarrier := make(chan struct{})

	// 3. Spawn 100 concurrent workers
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			<-startBarrier // Coordinate synchronized spike

			orderID := uuid.New().String()
			resID := uuid.New().String()

			// Execute atomic reservation transaction with deterministic ASC row locking
			tx, err := db.Pool.Begin(ctx)
			if err != nil {
				errorCount.Add(1)
				return
			}
			defer tx.Rollback(ctx)

			// Step 1: SELECT FOR UPDATE on inventory
			var currentOnHand, currentReserved int
			err = tx.QueryRow(ctx, `
				SELECT on_hand, reserved
				FROM inventory
				WHERE sku = $1
				FOR UPDATE;
			`, sku).Scan(&currentOnHand, &currentReserved)

			if err != nil {
				errorCount.Add(1)
				return
			}

			// Step 2: Invariant Check: (on_hand - reserved) >= 1
			if (currentOnHand - currentReserved) < 1 {
				// Insufficient stock -> Abort immediately
				_ = tx.Rollback(ctx)
				insufficientStockCount.Add(1)
				return
			}

			// Step 3: Increment reserved count
			_, err = tx.Exec(ctx, `
				UPDATE inventory
				SET reserved = reserved + 1, updated_at = NOW()
				WHERE sku = $1;
			`, sku)
			if err != nil {
				errorCount.Add(1)
				return
			}

			// Step 4: Record stock reservation
			_, err = tx.Exec(ctx, `
				INSERT INTO stock_reservations (id, order_id, status, expires_at)
				VALUES ($1, $2, 'PENDING', NOW() + INTERVAL '15 minutes');
			`, resID, orderID)
			if err != nil {
				errorCount.Add(1)
				return
			}

			_, err = tx.Exec(ctx, `
				INSERT INTO stock_reservation_items (reservation_id, sku, quantity)
				VALUES ($1, $2, 1);
			`, resID, sku)
			if err != nil {
				errorCount.Add(1)
				return
			}

			// Step 5: Commit transaction
			if err := tx.Commit(ctx); err != nil {
				errorCount.Add(1)
				return
			}

			successCount.Add(1)
		}(i)
	}

	// 4. Release all 100 workers simultaneously
	close(startBarrier)
	wg.Wait()

	// 5. Invariant Verifications:
	t.Logf("100 Concurrent Reservations Result: Successes=%d, InsufficientStock=%d, Errors=%d",
		successCount.Load(), insufficientStockCount.Load(), errorCount.Load())

	assert.Equal(t, int64(0), errorCount.Load(), "zero unexpected database or deadlock errors permitted")
	assert.Equal(t, int64(10), successCount.Load(), "MUST achieve exactly 10 successful reservations")
	assert.Equal(t, int64(90), insufficientStockCount.Load(), "MUST achieve exactly 90 rejections for insufficient stock")

	// 6. Database Level Conservation Invariants
	db.AssertInventory(ctx, t, sku, 10, 10)
	db.AssertInventoryConservation(ctx, t, sku)

	// Count total reservation records in DB
	var resCount int
	err := db.Pool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM stock_reservation_items sri
		JOIN stock_reservations sr ON sr.id = sri.reservation_id
		WHERE sri.sku = $1 AND sr.status = 'PENDING';
	`, sku).Scan(&resCount)
	require.NoError(t, err)
	assert.Equal(t, 10, resCount, "must have exactly 10 active reservation records")
}
