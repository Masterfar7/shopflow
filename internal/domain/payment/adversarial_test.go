package payment

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

// TestPayment_ConcurrentIdenticalIdempotency tests high concurrency with identical idempotency keys.
func TestPayment_ConcurrentIdenticalIdempotency(t *testing.T) {
	defer goleak.VerifyNone(t)

	ctx := context.Background()
	repo := NewMockRepository()
	svc := NewService(repo, nil)

	orderID := uuid.New()
	userID := uuid.New()
	idemKey := "concurrent-idem-key"
	amountMinor := int64(10000)

	numGoroutines := 25
	var wg sync.WaitGroup
	results := make([]*Payment, numGoroutines)
	errors := make([]error, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			p, err := svc.AuthorizeAndCapture(ctx, orderID, userID, amountMinor, "USD", "SUCCESS", idemKey)
			results[idx] = p
			errors[idx] = err
		}(i)
	}

	wg.Wait()

	// Verify all returned successful and point to identical payment ID
	var expectedID uuid.UUID
	for i := 0; i < numGoroutines; i++ {
		require.NoError(t, errors[i])
		require.NotNil(t, results[i])
		if expectedID == uuid.Nil {
			expectedID = results[i].ID
		} else {
			assert.Equal(t, expectedID, results[i].ID, "concurrent call %d returned divergent payment ID", i)
		}
	}

	// Verify only 1 payment was created in repository
	allPayments, err := repo.GetPaymentByID(ctx, expectedID)
	require.NoError(t, err)
	assert.Equal(t, expectedID, allPayments.ID)
}

// TestPayment_ConcurrentDifferentIdempotencyKeys tests that different keys create separate payments concurrently.
func TestPayment_ConcurrentDifferentIdempotencyKeys(t *testing.T) {
	defer goleak.VerifyNone(t)

	ctx := context.Background()
	repo := NewMockRepository()
	svc := NewService(repo, nil)

	numGoroutines := 20
	var wg sync.WaitGroup
	var mu sync.Mutex
	seenIDs := make(map[uuid.UUID]bool)

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			orderID := uuid.New()
			userID := uuid.New()
			idemKey := fmt.Sprintf("diff-key-%d", idx)

			p, err := svc.AuthorizeAndCapture(ctx, orderID, userID, 1500, "USD", "SUCCESS", idemKey)
			require.NoError(t, err)
			require.NotNil(t, p)

			mu.Lock()
			seenIDs[p.ID] = true
			mu.Unlock()
		}(i)
	}

	wg.Wait()

	assert.Len(t, seenIDs, numGoroutines, "expected each goroutine to produce a distinct payment ID")
}

// TestPayment_ConcurrentRefundAttempts tests terminal state protection under race conditions.
func TestPayment_ConcurrentRefundAttempts(t *testing.T) {
	defer goleak.VerifyNone(t)

	ctx := context.Background()
	repo := NewMockRepository()
	svc := NewService(repo, nil)

	orderID := uuid.New()
	userID := uuid.New()

	// Initial capture
	pay, err := svc.AuthorizeAndCapture(ctx, orderID, userID, 25000, "USD", "SUCCESS", "refund-race-init")
	require.NoError(t, err)
	require.Equal(t, PaymentStatusSuccess, pay.Status)

	numGoroutines := 20
	var wg sync.WaitGroup
	var successCount int64
	var alreadyRefundedCount int64

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, refErr := svc.Refund(ctx, pay.ID, userID, 25000, "Race refund test", fmt.Sprintf("race-ref-%d", idx))
			if refErr == nil {
				atomic.AddInt64(&successCount, 1)
			} else if assert.ErrorIs(t, refErr, ErrPaymentAlreadyRefunded) {
				atomic.AddInt64(&alreadyRefundedCount, 1)
			}
		}(i)
	}

	wg.Wait()

	assert.Equal(t, int64(1), successCount, "exactly one refund must succeed")
	assert.Equal(t, int64(numGoroutines-1), alreadyRefundedCount, "all subsequent concurrent refunds must be rejected")

	// Final status in DB must be REFUNDED
	finalPay, err := repo.GetPaymentByID(ctx, pay.ID)
	require.NoError(t, err)
	assert.Equal(t, PaymentStatusRefunded, finalPay.Status)
}

// TestPayment_Handler_AdversarialInputs tests bad input, malformed JSON, and error HTTP codes.
func TestPayment_Handler_AdversarialInputs(t *testing.T) {
	defer goleak.VerifyNone(t)

	repo := NewMockRepository()
	svc := NewService(repo, nil)
	handler := NewHandler(svc, nil)
	r := handler.Routes()

	// 1. Malformed JSON on /simulate
	reqMalformed := httptest.NewRequest(http.MethodPost, "/simulate", bytes.NewReader([]byte("{invalid-json")))
	reqMalformed.Header.Set("Content-Type", "application/json")
	recMalformed := httptest.NewRecorder()
	r.ServeHTTP(recMalformed, reqMalformed)
	assert.Equal(t, http.StatusBadRequest, recMalformed.Code)

	// 2. Zero amount_minor on /simulate
	zeroReq := SimulatePaymentRequest{
		OrderID:     uuid.New(),
		AmountMinor: 0,
		Currency:    "USD",
		Trigger:     "SUCCESS",
	}
	zeroBytes, _ := json.Marshal(zeroReq)
	reqZero := httptest.NewRequest(http.MethodPost, "/simulate", bytes.NewReader(zeroBytes))
	reqZero.Header.Set("Content-Type", "application/json")
	recZero := httptest.NewRecorder()
	r.ServeHTTP(recZero, reqZero)
	assert.Equal(t, http.StatusBadRequest, recZero.Code)

	// 3. Invalid UUID on GET /{payment_id}
	reqBadUUID := httptest.NewRequest(http.MethodGet, "/not-a-valid-uuid", nil)
	recBadUUID := httptest.NewRecorder()
	r.ServeHTTP(recBadUUID, reqBadUUID)
	assert.Equal(t, http.StatusBadRequest, recBadUUID.Code)

	// 4. Non-existent UUID on GET /{payment_id}
	reqNotFound := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/%s", uuid.New()), nil)
	recNotFound := httptest.NewRecorder()
	r.ServeHTTP(recNotFound, reqNotFound)
	assert.Equal(t, http.StatusNotFound, recNotFound.Code)

	// 5. Refund non-existent payment ID
	refBody := RefundPaymentRequest{
		AmountMinor: 1000,
		Reason:      "Non existent refund",
	}
	refBytes, _ := json.Marshal(refBody)
	reqRefNotFound := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/%s/refund", uuid.New()), bytes.NewReader(refBytes))
	reqRefNotFound.Header.Set("Content-Type", "application/json")
	recRefNotFound := httptest.NewRecorder()
	r.ServeHTTP(recRefNotFound, reqRefNotFound)
	assert.Equal(t, http.StatusNotFound, recRefNotFound.Code)
}

// TestPayment_PartialRefundExceedingTotal tests bounds checking on refund amounts.
func TestPayment_PartialRefundExceedingTotal(t *testing.T) {
	defer goleak.VerifyNone(t)

	ctx := context.Background()
	repo := NewMockRepository()
	svc := NewService(repo, nil)

	orderID := uuid.New()
	userID := uuid.New()

	p, err := svc.AuthorizeAndCapture(ctx, orderID, userID, 1000, "USD", "SUCCESS", "bounds-test")
	require.NoError(t, err)

	_, errExceed := svc.Refund(ctx, p.ID, userID, 1001, "Over-refund", "ref-over")
	assert.ErrorIs(t, errExceed, ErrInvalidRefundAmount)
}

// TestPayment_NetworkErrorTrigger tests NETWORK_ERROR simulation trigger.
func TestPayment_NetworkErrorTrigger(t *testing.T) {
	defer goleak.VerifyNone(t)

	ctx := context.Background()
	repo := NewMockRepository()
	svc := NewService(repo, nil)

	p, err := svc.AuthorizeAndCapture(ctx, uuid.New(), uuid.New(), 2500, "USD", "NETWORK_ERROR", "k-net-err")
	assert.ErrorIs(t, err, ErrPaymentDeclined)
	require.NotNil(t, p)
	assert.Equal(t, PaymentStatusFailed, p.Status)
	assert.Equal(t, "NETWORK_ERROR", *p.ErrorCode)
}
