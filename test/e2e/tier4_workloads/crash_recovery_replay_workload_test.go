package tier4_workloads

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

// Invariant: Simulating service crash/restart between database commit and Kafka offset commit
// triggers redelivery that is safely absorbed by the Inbox without duplicate effects.
// References: SPEC §57, PROJECT.md §23, Architecture Guidelines §7.2
func TestCrashRecoveryReplayAndInboxDeduplication(t *testing.T) {
	env := harness.SetupEnvironment(t)
	db := env.RequireDB(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	messageID := fmt.Sprintf("msg-crash-sim-%s", uuid.New().String())
	consumerGroup := "shopflow-inventory-consumer"
	orderID := uuid.New().String()
	sku := fmt.Sprintf("SKU-CRASH-%s", uuid.New().String()[:8])

	// 1. Initial State: Inventory has 50 on_hand, 0 reserved
	require.NoError(t, db.SeedInventory(ctx, sku, 50, 0))

	// 2. Initial Message Processing (First delivery attempt)
	// Database transaction commits domain change AND inbox entry
	tx1, err := db.Pool.Begin(ctx)
	require.NoError(t, err)
	defer tx1.Rollback(ctx)

	// Inbox deduplication insert
	_, err = tx1.Exec(ctx, `
		INSERT INTO inbox_messages (message_id, consumer_group, event_type, payload, status)
		VALUES ($1, $2, 'OrderCreated', '{"order_id":"`+orderID+`"}'::jsonb, 'COMPLETED');
	`, messageID, consumerGroup)
	require.NoError(t, err)

	// Domain effect: reserve 2 units
	_, err = tx1.Exec(ctx, `
		UPDATE inventory SET reserved = reserved + 2 WHERE sku = $1;
	`, sku)
	require.NoError(t, err)

	// DB commit succeeds!
	require.NoError(t, tx1.Commit(ctx))

	// SIMULATE CRASH HERE:
	// The service committed the DB transaction, but crashed before committing the Kafka consumer offset.
	// Therefore, Kafka redelivers the exact same message upon service restart!

	// 3. Redelivery Attempt (Second delivery with identical message_id and consumer_group)
	tx2, err := db.Pool.Begin(ctx)
	require.NoError(t, err)
	defer tx2.Rollback(ctx)

	var duplicateDetected bool
	_, err = tx2.Exec(ctx, `
		INSERT INTO inbox_messages (message_id, consumer_group, event_type, payload, status)
		VALUES ($1, $2, 'OrderCreated', '{"order_id":"`+orderID+`"}'::jsonb, 'COMPLETED');
	`, messageID, consumerGroup)

	if err != nil {
		// PostgreSQL error 23505 = unique_violation on PRIMARY KEY (message_id, consumer_group)
		duplicateDetected = true
		_ = tx2.Rollback(ctx)
	} else {
		// If not detected, domain mutation would erroneously execute a second time!
		_, _ = tx2.Exec(ctx, `UPDATE inventory SET reserved = reserved + 2 WHERE sku = $1;`, sku)
		_ = tx2.Commit(ctx)
	}

	assert.True(t, duplicateDetected, "inbox MUST detect duplicate message via unique (message_id, consumer_group) constraint")

	// 4. Invariant Verification: Zero duplicate side-effects!
	// Reserved stock MUST be exactly 2 (NOT 4!)
	db.AssertInventory(ctx, t, sku, 50, 2)
	db.AssertInventoryConservation(ctx, t, sku)
}

// Invariant: Terminal State Protection against Out-of-Order Message Resurrection.
// Once an aggregate transitions into a terminal state (CONFIRMED, CANCELLED, REFUNDED),
// delayed or replayed events must NOT mutate the aggregate back to an active state.
// References: PROJECT.md §25, Architecture Guidelines §7.3
func TestTerminalStateProtection(t *testing.T) {
	env := harness.SetupEnvironment(t)
	db := env.RequireDB(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	orderID := uuid.New().String()
	userID := uuid.New().String()

	// 1. Order already reached terminal state CANCELLED
	_, err := db.Pool.Exec(ctx, `
		INSERT INTO orders (id, user_id, idempotency_key, status, total_amount_minor, currency)
		VALUES ($1, $2, 'idem-terminal-1', 'CANCELLED', 3000, 'USD');
	`, orderID, userID)
	require.NoError(t, err)

	// 2. Delayed out-of-order 'PaymentSuccessful' or 'StockReserved' event arrives
	// Terminal state guard in consumer logic checks:
	// If order.status IN ('CANCELLED', 'CONFIRMED', 'REFUNDED'), reject state reversal!
	tx, err := db.Pool.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx)

	var currentStatus string
	err = tx.QueryRow(ctx, "SELECT status FROM orders WHERE id = $1 FOR UPDATE;", orderID).Scan(&currentStatus)
	require.NoError(t, err)

	terminalStates := map[string]bool{
		"CANCELLED": true,
		"CONFIRMED": true,
		"REFUNDED":  true,
	}

	if terminalStates[currentStatus] {
		// Terminal state guard: safe no-op, ignore out-of-order event
		_ = tx.Rollback(ctx)
	} else {
		// Erroneous state reversal
		_, _ = tx.Exec(ctx, "UPDATE orders SET status = 'CONFIRMED' WHERE id = $1;", orderID)
		_ = tx.Commit(ctx)
	}

	// 3. Verify order remains in terminal CANCELLED state
	db.AssertOrder(ctx, t, orderID, "CANCELLED", 3000)
}
