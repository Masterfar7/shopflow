package saga

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

func TestEmpirical_ConcurrentSagaExecutions(t *testing.T) {
	defer goleak.VerifyNone(t)

	ctx := context.Background()
	repo := newMockRepository()
	inv := &mockInventoryCommander{}
	ord := &mockOrderCommander{}
	pay := &mockPaymentCommander{}

	coord := NewCoordinator(repo, nil, inv, ord, pay, DefaultConfig(), slog.Default())

	orderID := uuid.New()
	saga, err := coord.StartSaga(ctx, SagaPayload{
		OrderID:          orderID,
		CustomerID:       uuid.New(),
		TotalAmountMinor: 10000,
		Currency:         "USD",
		Items: []SagaItem{
			{SKU: "CONCURRENT-SKU", Quantity: 1, UnitPriceMinor: 10000},
		},
	})
	require.NoError(t, err)

	const concurrency = 10
	var wg sync.WaitGroup
	errCh := make(chan error, concurrency)

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := coord.ExecuteSaga(ctx, saga.SagaID)
			errCh <- err
		}()
	}

	wg.Wait()
	close(errCh)

	// Exactly one or more may succeed or return ErrSagaAlreadyTerminal/optimistic lock,
	// but the saga must end in CONFIRMED state without corruption.
	finalSaga, err := repo.GetSagaByID(ctx, saga.SagaID)
	require.NoError(t, err)
	assert.Equal(t, StateConfirmed, finalSaga.CurrentState)

	// Assert order confirmed exactly once
	assert.Len(t, ord.confirmCalls, 1)
	assert.Empty(t, ord.cancelCalls)
}

func TestEmpirical_ConcurrentWatchdogVsExecution(t *testing.T) {
	defer goleak.VerifyNone(t)

	ctx := context.Background()
	repo := newMockRepository()
	inv := &mockInventoryCommander{}
	ord := &mockOrderCommander{}
	pay := &mockPaymentCommander{}

	coord := NewCoordinator(repo, nil, inv, ord, pay, DefaultConfig(), slog.Default())
	wd := NewWatchdog(coord, repo, WatchdogConfig{PollInterval: 10 * time.Millisecond, BatchSize: 5}, slog.Default())

	orderID := uuid.New()
	saga, err := coord.StartSaga(ctx, SagaPayload{
		OrderID:          orderID,
		CustomerID:       uuid.New(),
		TotalAmountMinor: 5000,
		Currency:         "USD",
		Items:            []SagaItem{{SKU: "RACE-SKU", Quantity: 1, UnitPriceMinor: 5000}},
	})
	require.NoError(t, err)

	// Start watchdog
	require.NoError(t, wd.Start(ctx))

	// Concurrently execute saga
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = coord.ExecuteSaga(ctx, saga.SagaID)
	}()

	wg.Wait()

	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.NoError(t, wd.Stop(stopCtx))

	finalSaga, err := repo.GetSagaByID(ctx, saga.SagaID)
	require.NoError(t, err)
	assert.True(t, IsTerminal(finalSaga.CurrentState), "final state must be terminal: %s", finalSaga.CurrentState)
}

func TestEmpirical_LargePayloadIntegrity(t *testing.T) {
	orderID := uuid.New()
	customerID := uuid.New()
	resID := uuid.New()
	payID := uuid.New()

	items := make([]SagaItem, 100)
	var expectedTotal int64
	for i := 0; i < 100; i++ {
		qty := i + 1
		price := int64((i + 1) * 150)
		items[i] = SagaItem{
			SKU:            fmt.Sprintf("SKU-LARGE-ITEM-%04d-ÜNÏCÖDÉ", i),
			Quantity:       qty,
			UnitPriceMinor: price,
		}
		expectedTotal += int64(qty) * price
	}

	payload := SagaPayload{
		OrderID:          orderID,
		CustomerID:       customerID,
		TotalAmountMinor: expectedTotal,
		Currency:         "EUR",
		Items:            items,
		ReservationID:    &resID,
		PaymentID:        &payID,
		PaymentToken:     "tok_secure_multi_item_999",
		FailureReason:    "Special char error: !@#$%^&*()_+-=[]{}|;':,.<>/?~`",
		CompensatingStep: "releasing_stock",
	}

	data, err := payload.Marshal()
	require.NoError(t, err)

	restored, err := UnmarshalPayload(data)
	require.NoError(t, err)
	assert.Equal(t, expectedTotal, restored.TotalAmountMinor)
	assert.Len(t, restored.Items, 100)
	assert.Equal(t, "SKU-LARGE-ITEM-0099-ÜNÏCÖDÉ", restored.Items[99].SKU)
	assert.Equal(t, 100, restored.Items[99].Quantity)
	assert.Equal(t, int64(15000), restored.Items[99].UnitPriceMinor)
	assert.Equal(t, "Special char error: !@#$%^&*()_+-=[]{}|;':,.<>/?~`", restored.FailureReason)
}

func TestEmpirical_CompensationWithoutReservationID(t *testing.T) {
	ctx := context.Background()
	repo := newMockRepository()
	inv := &mockInventoryCommander{}
	ord := &mockOrderCommander{}
	pay := &mockPaymentCommander{}

	coord := NewCoordinator(repo, nil, inv, ord, pay, DefaultConfig(), slog.Default())

	orderID := uuid.New()
	saga, err := coord.StartSaga(ctx, SagaPayload{
		OrderID:       orderID,
		ReservationID: nil, // Explicitly nil
	})
	require.NoError(t, err)

	err = coord.Compensate(ctx, saga.SagaID, "timeout before reservation")
	require.NoError(t, err)

	// ReleaseStock must NOT be called
	assert.Empty(t, inv.releaseCalls, "ReleaseStock should not be called when ReservationID is nil")
	// CancelOrder must be called
	require.Len(t, ord.cancelCalls, 1)
	assert.Equal(t, orderID, ord.cancelCalls[0])

	finalSaga, err := repo.GetSagaByID(ctx, saga.SagaID)
	require.NoError(t, err)
	assert.Equal(t, StateFailed, finalSaga.CurrentState)
}

func TestEmpirical_FSMStateTransitionsExhaustive(t *testing.T) {
	allStates := []SagaState{
		StateStarted,
		StatePending,
		StateReservingStock,
		StateStockReserved,
		StatePaying,
		StatePaid,
		StateConfirmed,
		StateCompensating,
		StateFailed,
		StateFailedCompensated,
	}

	for _, from := range allStates {
		for _, to := range allStates {
			allowed := CanTransition(from, to)
			if IsTerminal(from) {
				assert.False(t, allowed, "terminal state %s must never transition to %s", from, to)
			}
		}
	}
}
