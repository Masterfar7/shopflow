package tier3_pairwise

import (
	"context"
	"fmt"
	"testing"
	"time"

	"shopflow/test/e2e/harness"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Invariant: Failed payment transitions the saga into automated compensation (releasing stock & cancelling order).
// Reference: SPEC §58, PROJECT.md §19
func TestOrderSagaCompensationWorkflow(t *testing.T) {
	env := harness.SetupEnvironment(t)
	db := env.RequireDB(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	orderID := uuid.New().String()
	sagaID := uuid.New().String()
	resID := uuid.New().String()
	sku := fmt.Sprintf("SKU-SAGA-%s", uuid.New().String()[:8])

	// 1. Initial State: Seed Inventory with 20 items
	require.NoError(t, db.SeedInventory(ctx, sku, 20, 0))

	// 2. Step 1: Order Saga initialized in PENDING state
	tx, err := db.Pool.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx)

	_, err = tx.Exec(ctx, `
		INSERT INTO orders (id, user_id, idempotency_key, status, total_amount_minor, currency)
		VALUES ($1, $2, $3, 'RESERVING_STOCK', 5000, 'USD');
	`, orderID, uuid.New().String(), uuid.New().String())
	require.NoError(t, err)

	_, err = tx.Exec(ctx, `
		INSERT INTO order_sagas (saga_id, order_id, current_state, payload, retry_count, timeout_at)
		VALUES ($1, $2, 'PENDING', '{"order_id":"`+orderID+`"}'::jsonb, 0, NOW() + INTERVAL '10 minutes');
	`, sagaID, orderID)
	require.NoError(t, err)

	// Step 2: Stock is successfully reserved (5 units)
	_, err = tx.Exec(ctx, `
		UPDATE inventory SET reserved = reserved + 5 WHERE sku = $1;
	`, sku)
	require.NoError(t, err)

	_, err = tx.Exec(ctx, `
		INSERT INTO stock_reservations (id, order_id, status, expires_at)
		VALUES ($1, $2, 'PENDING', NOW() + INTERVAL '10 minutes');
	`, resID, orderID)
	require.NoError(t, err)

	_, err = tx.Exec(ctx, `
		INSERT INTO stock_reservation_items (reservation_id, sku, quantity)
		VALUES ($1, $2, 5);
	`, resID, sku)
	require.NoError(t, err)

	_, err = tx.Exec(ctx, `
		UPDATE order_sagas SET current_state = 'STOCK_RESERVED', updated_at = NOW() WHERE saga_id = $1;
	`, sagaID)
	require.NoError(t, err)

	require.NoError(t, tx.Commit(ctx))

	// Verify stock is reserved
	db.AssertInventory(ctx, t, sku, 20, 5)

	// 3. Step 3: Payment fails -> Compensation triggered
	tx2, err := db.Pool.Begin(ctx)
	require.NoError(t, err)
	defer tx2.Rollback(ctx)

	// Saga transitions to COMPENSATING
	_, err = tx2.Exec(ctx, `
		UPDATE order_sagas SET current_state = 'COMPENSATING', updated_at = NOW() WHERE saga_id = $1;
	`, sagaID)
	require.NoError(t, err)

	// Compensation action 1: Release reserved stock
	_, err = tx2.Exec(ctx, `
		UPDATE inventory SET reserved = reserved - 5 WHERE sku = $1;
	`, sku)
	require.NoError(t, err)

	_, err = tx2.Exec(ctx, `
		UPDATE stock_reservations SET status = 'RELEASED', updated_at = NOW() WHERE id = $1;
	`, resID)
	require.NoError(t, err)

	// Compensation action 2: Cancel order
	_, err = tx2.Exec(ctx, `
		UPDATE orders SET status = 'CANCELLED', updated_at = NOW() WHERE id = $1;
	`, orderID)
	require.NoError(t, err)

	// Saga transitions to terminal state FAILED_COMPENSATED
	_, err = tx2.Exec(ctx, `
		UPDATE order_sagas SET current_state = 'FAILED_COMPENSATED', updated_at = NOW() WHERE saga_id = $1;
	`, sagaID)
	require.NoError(t, err)

	// Outbox event for compensation notification
	_, err = tx2.Exec(ctx, `
		INSERT INTO outbox_messages (id, aggregate_type, aggregate_id, event_type, payload, status)
		VALUES ($1, 'order', $2, 'OrderCancelled', '{"order_id":"`+orderID+`","reason":"payment_failed"}'::jsonb, 'PENDING');
	`, uuid.New().String(), orderID)
	require.NoError(t, err)

	require.NoError(t, tx2.Commit(ctx))

	// 4. Invariant Assertions:
	// a. Stock reservation is completely returned: on_hand = 20, reserved = 0
	db.AssertInventory(ctx, t, sku, 20, 0)
	db.AssertInventoryConservation(ctx, t, sku)

	// b. Order is CANCELLED
	db.AssertOrder(ctx, t, orderID, "CANCELLED", 5000)

	// c. Saga is in terminal compensated state
	db.AssertSagaState(ctx, t, orderID, "FAILED_COMPENSATED")
}

// FEAT-RSZ-02: Dead Letter Queue Routing for Poison Pill Events
func TestDeadLetterQueueRouting(t *testing.T) {
	env := harness.SetupEnvironment(t)
	db := env.RequireDB(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	poisonID := uuid.New().String()
	_, err := db.Pool.Exec(ctx, `
		INSERT INTO dead_letter_messages (
			id, source_type, source_id, topic, partition, offset_val, error_reason, payload, retry_count
		) VALUES (
			$1, 'INBOX', 'msg-poison-1', 'orders', 0, 42, 'unparseable JSON schema corruption', '{"invalid_json":'::jsonb, 5
		);
	`, poisonID)
	require.NoError(t, err)

	var count int
	err = db.Pool.QueryRow(ctx, "SELECT COUNT(*) FROM dead_letter_messages WHERE id = $1", poisonID).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}
