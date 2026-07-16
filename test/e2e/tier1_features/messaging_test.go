package tier1_features

import (
	"context"
	"testing"
	"time"

	"shopflow/test/e2e/harness"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// FEAT-TXO-01: Transactional Outbox Schema & Insertion within Local DB Transactions
func TestTransactionalOutboxInsertion(t *testing.T) {
	env := harness.SetupEnvironment(t)
	db := env.RequireDB(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	orderID := uuid.New().String()
	eventID := uuid.New().String()

	// Atomic local transaction: insert order AND outbox record
	tx, err := db.Pool.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx)

	_, err = tx.Exec(ctx, `
		INSERT INTO orders (id, user_id, idempotency_key, status, total_amount_minor, currency)
		VALUES ($1, $2, $3, 'PENDING', 5000, 'USD');
	`, orderID, uuid.New().String(), uuid.New().String())
	require.NoError(t, err)

	_, err = tx.Exec(ctx, `
		INSERT INTO outbox_messages (id, aggregate_type, aggregate_id, event_type, payload, status)
		VALUES ($1, 'order', $2, 'OrderCreated', '{"order_id":"`+orderID+`"}'::jsonb, 'PENDING');
	`, eventID, orderID)
	require.NoError(t, err)

	err = tx.Commit(ctx)
	require.NoError(t, err)

	// Verify outbox message is stored and pending
	db.AssertOutboxMessage(ctx, t, orderID, "OrderCreated", "PENDING")
}

// FEAT-TXO-02: Outbox Polling Publisher with FOR UPDATE SKIP LOCKED and Lease Renewal
func TestTransactionalOutboxPollingAndLease(t *testing.T) {
	env := harness.SetupEnvironment(t)
	db := env.RequireDB(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	orderID := uuid.New().String()
	eventID := uuid.New().String()

	_, err := db.Pool.Exec(ctx, `
		INSERT INTO outbox_messages (id, aggregate_type, aggregate_id, event_type, payload, status, leased_until)
		VALUES ($1, 'order', $2, 'StockReserved', '{"order_id":"`+orderID+`"}'::jsonb, 'PENDING', NULL);
	`, eventID, orderID)
	require.NoError(t, err)

	// Simulate polling publisher leasing via FOR UPDATE SKIP LOCKED
	tx, err := db.Pool.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx)

	var leasedID string
	err = tx.QueryRow(ctx, `
		SELECT id
		FROM outbox_messages
		WHERE status = 'PENDING' AND (leased_until IS NULL OR leased_until < NOW())
		ORDER BY created_at ASC
		LIMIT 1
		FOR UPDATE SKIP LOCKED;
	`).Scan(&leasedID)
	require.NoError(t, err)
	assert.Equal(t, eventID, leasedID)

	// Renew lease for 30 seconds
	_, err = tx.Exec(ctx, `
		UPDATE outbox_messages
		SET leased_until = NOW() + INTERVAL '30 seconds', leased_by = 'test-poller'
		WHERE id = $1;
	`, leasedID)
	require.NoError(t, err)

	err = tx.Commit(ctx)
	require.NoError(t, err)
}

// FEAT-TXO-03: Kafka Acknowledgement Handling & Outbox Message Archival
func TestTransactionalOutboxKafkaAck(t *testing.T) {
	env := harness.SetupEnvironment(t)
	db := env.RequireDB(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	orderID := uuid.New().String()
	eventID := uuid.New().String()

	_, err := db.Pool.Exec(ctx, `
		INSERT INTO outbox_messages (id, aggregate_type, aggregate_id, event_type, payload, status)
		VALUES ($1, 'order', $2, 'PaymentCompleted', '{"order_id":"`+orderID+`"}'::jsonb, 'PENDING');
	`, eventID, orderID)
	require.NoError(t, err)

	// Simulate successful Kafka transmission and ACK
	_, err = db.Pool.Exec(ctx, `
		UPDATE outbox_messages
		SET status = 'PUBLISHED', published_at = NOW()
		WHERE id = $1;
	`, eventID)
	require.NoError(t, err)

	db.AssertOutboxMessage(ctx, t, orderID, "PaymentCompleted", "PUBLISHED")
}
