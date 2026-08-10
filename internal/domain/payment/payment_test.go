package payment

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"shopflow/internal/domain/saga"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

func TestPayment_StrictZeroFloatMoneyInvariants(t *testing.T) {
	defer goleak.VerifyNone(t)

	// Invariant: Verify domain models strictly use int64 for money and contain zero float32/float64 fields
	models := []any{
		Payment{},
		Refund{},
		SimulatePaymentRequest{},
		RefundPaymentRequest{},
		PaymentResponse{},
		RefundResponse{},
	}

	for _, m := range models {
		val := reflect.TypeOf(m)
		for i := 0; i < val.NumField(); i++ {
			field := val.Field(i)
			assert.NotEqual(t, reflect.Float32, field.Type.Kind(), "field %s in %s must not be float32", field.Name, val.Name())
			assert.NotEqual(t, reflect.Float64, field.Type.Kind(), "field %s in %s must not be float64", field.Name, val.Name())
		}
	}
}

func TestPaymentService_AuthorizeAndCapture_Success(t *testing.T) {
	defer goleak.VerifyNone(t)

	ctx := context.Background()
	repo := NewMockRepository()
	svc := NewService(repo, nil)

	orderID := uuid.New()
	userID := uuid.New()
	idemKey := "idem-success-1"

	p, err := svc.AuthorizeAndCapture(ctx, orderID, userID, 5000, "USD", "FORCE_SUCCESS", idemKey)
	require.NoError(t, err)
	require.NotNil(t, p)

	assert.Equal(t, orderID, p.OrderID)
	assert.Equal(t, userID, p.UserID)
	assert.Equal(t, int64(5000), p.AmountMinor)
	assert.Equal(t, "USD", p.Currency)
	assert.Equal(t, PaymentStatusSuccess, p.Status)
	assert.Nil(t, p.ErrorCode)

	// Verify persisted in repository
	saved, err := repo.GetPaymentByID(ctx, p.ID)
	require.NoError(t, err)
	assert.Equal(t, p.ID, saved.ID)
	assert.Equal(t, PaymentStatusSuccess, saved.Status)
}

func TestPaymentService_IdempotencyReplay(t *testing.T) {
	defer goleak.VerifyNone(t)

	ctx := context.Background()
	repo := NewMockRepository()
	svc := NewService(repo, nil)

	orderID := uuid.New()
	userID := uuid.New()
	idemKey := "idem-replay-1"

	// 1. Initial execution
	p1, err := svc.AuthorizeAndCapture(ctx, orderID, userID, 7500, "USD", "tok_valid", idemKey)
	require.NoError(t, err)
	require.NotNil(t, p1)

	// 2. Replay with identical key and parameters
	p2, err := svc.AuthorizeAndCapture(ctx, orderID, userID, 7500, "USD", "tok_valid", idemKey)
	require.NoError(t, err)
	require.NotNil(t, p2)
	assert.Equal(t, p1.ID, p2.ID, "idempotent replay must return original payment ID")

	// 3. Replay with conflicting parameters
	_, errConflict := svc.AuthorizeAndCapture(ctx, orderID, userID, 9999, "USD", "tok_valid", idemKey)
	assert.ErrorIs(t, errConflict, ErrIdempotencyConflict)
}

func TestPaymentService_DeclineTriggers(t *testing.T) {
	defer goleak.VerifyNone(t)

	ctx := context.Background()
	repo := NewMockRepository()
	svc := NewService(repo, nil)

	declineTokens := []string{
		"fail",
		"decline",
		"FORCE_FAILURE",
		"sim_tok_decline",
		"DECLINE_INSUFFICIENT_FUNDS",
		"DECLINE_FRAUD",
	}

	for _, tok := range declineTokens {
		t.Run("token_"+tok, func(t *testing.T) {
			orderID := uuid.New()
			userID := uuid.New()
			idemKey := uuid.New().String()

			p, err := svc.AuthorizeAndCapture(ctx, orderID, userID, 2500, "USD", tok, idemKey)
			assert.ErrorIs(t, err, ErrPaymentDeclined)
			require.NotNil(t, p)
			assert.Equal(t, PaymentStatusFailed, p.Status)
			require.NotNil(t, p.ErrorCode)
			assert.Equal(t, "PAYMENT_DECLINED", *p.ErrorCode)

			// Record is persisted as FAILED
			saved, err := repo.GetPaymentByID(ctx, p.ID)
			require.NoError(t, err)
			assert.Equal(t, PaymentStatusFailed, saved.Status)
		})
	}
}

func TestPaymentService_TimeoutTrigger(t *testing.T) {
	defer goleak.VerifyNone(t)

	ctx := context.Background()
	repo := NewMockRepository()
	svc := NewService(repo, nil)

	orderID := uuid.New()
	userID := uuid.New()
	idemKey := "idem-timeout"

	p, err := svc.AuthorizeAndCapture(ctx, orderID, userID, 3000, "USD", "timeout", idemKey)
	assert.ErrorIs(t, err, ErrPaymentTimeout)
	require.NotNil(t, p)
	assert.Equal(t, PaymentStatusFailed, p.Status)
	assert.Equal(t, "GATEWAY_TIMEOUT", *p.ErrorCode)
}

func TestPaymentService_InvalidAmount(t *testing.T) {
	defer goleak.VerifyNone(t)

	ctx := context.Background()
	repo := NewMockRepository()
	svc := NewService(repo, nil)

	_, errZero := svc.AuthorizeAndCapture(ctx, uuid.New(), uuid.New(), 0, "USD", "SUCCESS", "k1")
	assert.ErrorIs(t, errZero, ErrInvalidPaymentAmount)

	_, errNeg := svc.AuthorizeAndCapture(ctx, uuid.New(), uuid.New(), -500, "USD", "SUCCESS", "k2")
	assert.ErrorIs(t, errNeg, ErrInvalidPaymentAmount)
}

func TestPaymentService_Refund_SuccessAndInvariants(t *testing.T) {
	defer goleak.VerifyNone(t)

	ctx := context.Background()
	repo := NewMockRepository()
	svc := NewService(repo, nil)

	orderID := uuid.New()
	userID := uuid.New()

	// 1. Create a captured payment
	pay, err := svc.AuthorizeAndCapture(ctx, orderID, userID, 10000, "USD", "SUCCESS", "idem-refund-init")
	require.NoError(t, err)
	require.Equal(t, PaymentStatusSuccess, pay.Status)

	// 2. Refund amount > payment amount fails
	_, errOver := svc.Refund(ctx, pay.ID, userID, 15000, "Customer return", "r1")
	assert.ErrorIs(t, errOver, ErrInvalidRefundAmount)

	// 3. Refund non-existent payment fails
	_, errNotFound := svc.Refund(ctx, uuid.New(), userID, 5000, "reason", "r2")
	assert.ErrorIs(t, errNotFound, ErrPaymentNotFound)

	// 4. Successful refund
	ref, err := svc.Refund(ctx, pay.ID, userID, 10000, "Customer returned product", "r3")
	require.NoError(t, err)
	require.NotNil(t, ref)
	assert.Equal(t, pay.ID, ref.PaymentID)
	assert.Equal(t, orderID, ref.OrderID)
	assert.Equal(t, int64(10000), ref.AmountMinor)
	assert.Equal(t, RefundStatusSuccess, ref.Status)

	// 5. Payment status updated to REFUNDED
	updatedPay, err := repo.GetPaymentByID(ctx, pay.ID)
	require.NoError(t, err)
	assert.Equal(t, PaymentStatusRefunded, updatedPay.Status)

	// 6. Terminal state protection: attempting to refund an already refunded payment fails
	_, errAlready := svc.Refund(ctx, pay.ID, userID, 10000, "Duplicate refund attempt", "r4")
	assert.ErrorIs(t, errAlready, ErrPaymentAlreadyRefunded)
}

func TestPaymentService_Refund_CannotRefundFailedPayment(t *testing.T) {
	defer goleak.VerifyNone(t)

	ctx := context.Background()
	repo := NewMockRepository()
	svc := NewService(repo, nil)

	// Failed payment
	p, err := svc.AuthorizeAndCapture(ctx, uuid.New(), uuid.New(), 5000, "USD", "decline", "idem-fail")
	assert.ErrorIs(t, err, ErrPaymentDeclined)
	require.NotNil(t, p)

	_, errRefund := svc.Refund(ctx, p.ID, p.UserID, 5000, "reason", "idem-fail-ref")
	assert.ErrorIs(t, errRefund, ErrPaymentCannotBeRefunded)
}

func TestSagaPaymentAdapter(t *testing.T) {
	defer goleak.VerifyNone(t)

	ctx := context.Background()
	repo := NewMockRepository()
	svc := NewService(repo, nil)

	var adapter saga.PaymentCommander = NewSagaAdapter(svc)

	orderID := uuid.New()
	customerID := uuid.New()

	// 1. Test AuthorizeAndCapture via adapter
	paymentID, err := adapter.AuthorizeAndCapture(ctx, orderID, customerID, 4500, "USD", "tok_valid", "saga-idem-1")
	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, paymentID)

	// 2. Test Refund via adapter
	err = adapter.Refund(ctx, paymentID, orderID, 4500, "Saga compensation", "saga-ref-1")
	require.NoError(t, err)

	// Verify refunded state
	p, err := repo.GetPaymentByID(ctx, paymentID)
	require.NoError(t, err)
	assert.Equal(t, PaymentStatusRefunded, p.Status)

	// Adapter handles uuid.Nil payment gracefully
	errNil := adapter.Refund(ctx, uuid.Nil, orderID, 4500, "Noop", "k")
	assert.NoError(t, errNil)
}

func TestPaymentHandler_Endpoints(t *testing.T) {
	defer goleak.VerifyNone(t)

	repo := NewMockRepository()
	svc := NewService(repo, nil)
	handler := NewHandler(svc, nil)
	r := handler.Routes()

	// 1. POST /simulate success
	orderID := uuid.New()
	userID := uuid.New()
	reqBody := SimulatePaymentRequest{
		OrderID:     orderID,
		AmountMinor: 8900,
		Currency:    "USD",
		Trigger:     "FORCE_SUCCESS",
	}
	bodyBytes, err := json.Marshal(reqBody)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/simulate", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-ID", userID.String())
	req.Header.Set("Idempotency-Key", "http-idem-1")

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var payResp PaymentResponse
	err = json.NewDecoder(rec.Body).Decode(&payResp)
	require.NoError(t, err)
	assert.Equal(t, "SUCCESS", string(payResp.Status))
	assert.Equal(t, int64(8900), payResp.AmountMinor)
	assert.Equal(t, "SIMULATED", payResp.Provider)

	// 2. GET /{payment_id}
	getReq := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/%s", payResp.ID), nil)
	getRec := httptest.NewRecorder()
	r.ServeHTTP(getRec, getReq)

	require.Equal(t, http.StatusOK, getRec.Code)
	var fetchedPay PaymentResponse
	err = json.NewDecoder(getRec.Body).Decode(&fetchedPay)
	require.NoError(t, err)
	assert.Equal(t, payResp.ID, fetchedPay.ID)

	// 3. POST /{payment_id}/refund
	refBody := RefundPaymentRequest{
		AmountMinor: 8900,
		Reason:      "Customer requested cancellation",
	}
	refBytes, err := json.Marshal(refBody)
	require.NoError(t, err)

	refReq := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/%s/refund", payResp.ID), bytes.NewReader(refBytes))
	refReq.Header.Set("Content-Type", "application/json")
	refReq.Header.Set("X-User-ID", userID.String())
	refReq.Header.Set("Idempotency-Key", "http-ref-1")

	refRec := httptest.NewRecorder()
	r.ServeHTTP(refRec, refReq)

	require.Equal(t, http.StatusOK, refRec.Code)
	var refResp RefundResponse
	err = json.NewDecoder(refRec.Body).Decode(&refResp)
	require.NoError(t, err)
	assert.Equal(t, "SUCCESS", string(refResp.Status))
	assert.Equal(t, payResp.ID, refResp.PaymentID)
	assert.Equal(t, int64(8900), refResp.AmountMinor)

	// 4. POST /simulate failure trigger returns 200 OK with FAILED status
	failReqBody := SimulatePaymentRequest{
		OrderID:     uuid.New(),
		AmountMinor: 1500,
		Currency:    "USD",
		Trigger:     "FORCE_FAILURE",
	}
	failBytes, _ := json.Marshal(failReqBody)
	failReq := httptest.NewRequest(http.MethodPost, "/simulate", bytes.NewReader(failBytes))
	failReq.Header.Set("Content-Type", "application/json")
	failReq.Header.Set("X-User-ID", userID.String())
	failReq.Header.Set("Idempotency-Key", "http-fail-1")

	failRec := httptest.NewRecorder()
	r.ServeHTTP(failRec, failReq)

	require.Equal(t, http.StatusOK, failRec.Code)
	var failResp PaymentResponse
	err = json.NewDecoder(failRec.Body).Decode(&failResp)
	require.NoError(t, err)
	assert.Equal(t, "FAILED", string(failResp.Status))
}
