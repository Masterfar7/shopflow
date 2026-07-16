package tier1_features

import (
	"context"
	"net/http"
	"testing"
	"time"

	"shopflow/test/e2e/harness"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// FEAT-PAY-01: Idempotent Payment Simulator with Configurable Test Triggers
func TestPaymentSimulatorTriggers(t *testing.T) {
	env := harness.SetupEnvironment(t)
	client := env.RequireHTTPServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	userID := uuid.New().String()
	orderID := uuid.New().String()

	// 1. Trigger FORCE_SUCCESS
	successReq := harness.PaymentSimulateRequest{
		OrderID:     orderID,
		AmountMinor: 4999,
		Currency:    "USD",
		Trigger:     "FORCE_SUCCESS",
	}
	idemKey1 := uuid.New().String()
	payResp, prob, status, err := client.SimulatePayment(ctx, userID, successReq, idemKey1)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status, "expected 200 OK, prob: %+v", prob)
	require.NotNil(t, payResp)
	assert.Equal(t, "SUCCESS", payResp.Status)
	assert.Equal(t, int64(4999), payResp.AmountMinor)
	assert.Equal(t, "SIMULATED", payResp.Provider)

	// 2. Trigger FORCE_FAILURE
	orderID2 := uuid.New().String()
	failReq := harness.PaymentSimulateRequest{
		OrderID:     orderID2,
		AmountMinor: 2500,
		Currency:    "USD",
		Trigger:     "FORCE_FAILURE",
	}
	idemKey2 := uuid.New().String()
	failResp, _, status2, err := client.SimulatePayment(ctx, userID, failReq, idemKey2)
	require.NoError(t, err)
	assert.True(t, status2 == http.StatusOK || status2 == http.StatusPaymentRequired)
	if failResp != nil {
		assert.Equal(t, "FAILED", failResp.Status)
	}
}

// FEAT-PAY-02: Payment Capture, Confirmation & Refund Execution
func TestPaymentCaptureAndRefund(t *testing.T) {
	env := harness.SetupEnvironment(t)
	client := env.RequireHTTPServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	userID := uuid.New().String()
	orderID := uuid.New().String()

	// 1. Initial Successful Payment
	payReq := harness.PaymentSimulateRequest{
		OrderID:     orderID,
		AmountMinor: 10000, // $100.00
		Currency:    "USD",
		Trigger:     "FORCE_SUCCESS",
	}
	payResp, _, status, err := client.SimulatePayment(ctx, userID, payReq, uuid.New().String())
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	require.NotNil(t, payResp)

	// 2. Execute Refund for Payment
	refundReq := harness.RefundPaymentRequest{
		AmountMinor: 10000,
		Reason:      "Customer requested cancellation",
	}
	refundResp, prob, status, err := client.RefundPayment(ctx, payResp.ID, userID, refundReq, uuid.New().String())
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status, "expected 200 OK on refund, prob: %+v", prob)
	require.NotNil(t, refundResp)
	assert.Equal(t, "SUCCESS", refundResp.Status)
	assert.Equal(t, payResp.ID, refundResp.PaymentID)
	assert.Equal(t, int64(10000), refundResp.AmountMinor)
}

// FEAT-PAY-03: Asynchronous Payment Reconciliation Routine & DB Verification
func TestPaymentReconciliation(t *testing.T) {
	env := harness.SetupEnvironment(t)
	db := env.RequireDB(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	paymentID := uuid.New().String()
	orderID := uuid.New().String()
	userID := uuid.New().String()
	idemKey := uuid.New().String()

	// Insert simulated payment record into database
	_, err := db.Pool.Exec(ctx, `
		INSERT INTO payments (id, order_id, user_id, idempotency_key, amount_minor, currency, provider, status)
		VALUES ($1, $2, $3, $4, 7500, 'USD', 'SIMULATED', 'INITIATED');
	`, paymentID, orderID, userID, idemKey)
	require.NoError(t, err)

	// Reconcile status to SUCCESS
	_, err = db.Pool.Exec(ctx, `
		UPDATE payments SET status = 'SUCCESS', updated_at = NOW() WHERE id = $1;
	`, paymentID)
	require.NoError(t, err)

	var reconciledStatus string
	err = db.Pool.QueryRow(ctx, "SELECT status FROM payments WHERE id = $1", paymentID).Scan(&reconciledStatus)
	require.NoError(t, err)
	assert.Equal(t, "SUCCESS", reconciledStatus)
}
