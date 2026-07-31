package saga

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

// TestChallenge_StockReservedTimeout_WatchdogBehavior verifies whether the Watchdog
// handles sagas that time out while in the STOCK_RESERVED state.
// (e.g., process crash or network partition after stock was reserved but before payment completed).
func TestChallenge_StockReservedTimeout_WatchdogBehavior(t *testing.T) {
	ctx := context.Background()
	repo := newMockRepository()
	inv := &mockInventoryCommander{}
	ord := &mockOrderCommander{}
	pay := &mockPaymentCommander{}

	coord := NewCoordinator(repo, nil, inv, ord, pay, DefaultConfig(), slog.Default())
	wd := NewWatchdog(coord, repo, WatchdogConfig{PollInterval: 50 * time.Millisecond, BatchSize: 10}, slog.Default())

	orderID := uuid.New()
	sagaID := uuid.New()
	resID := uuid.New()

	expiredSaga := &OrderSaga{
		SagaID:       sagaID,
		OrderID:      orderID,
		CurrentState: StateStockReserved,
		Payload: SagaPayload{
			OrderID:       orderID,
			ReservationID: &resID,
		},
		TimeoutAt: time.Now().Add(-10 * time.Second),
	}
	require.NoError(t, repo.CreateSaga(ctx, expiredSaga))

	repo.mu.Lock()
	repo.sagas[sagaID].CurrentState = StateStockReserved
	repo.sagas[sagaID].TimeoutAt = time.Now().Add(-10 * time.Second)
	repo.mu.Unlock()

	wd.ProcessExpiredSagas(ctx)

	updated, err := repo.GetSagaByID(ctx, sagaID)
	require.NoError(t, err)

	assert.Equal(t, StateFailed, updated.CurrentState)
	require.Len(t, inv.releaseCalls, 1, "ReleaseStock must be called when STOCK_RESERVED saga times out")
	assert.Equal(t, resID, inv.releaseCalls[0])
	require.Len(t, ord.cancelCalls, 1, "CancelOrder must be called when STOCK_RESERVED saga times out")
	assert.Equal(t, orderID, ord.cancelCalls[0])
}

// TestChallenge_ConcurrentExecuteSaga_StrictIdempotency tests 50 concurrent
// executions of the same saga to ensure exactly ONE successful reservation and payment.
func TestChallenge_ConcurrentExecuteSaga_StrictIdempotency(t *testing.T) {
	defer goleak.VerifyNone(t)

	ctx := context.Background()
	repo := newMockRepository()

	var reserveCount int32
	var authCount int32
	var confirmCount int32

	inv := &mockInventoryCommander{
		reserveFunc: func(ctx context.Context, orderID uuid.UUID, items []ReservationItem) (*ReservationResult, error) {
			atomic.AddInt32(&reserveCount, 1)
			return &ReservationResult{ReservationID: uuid.New(), Success: true}, nil
		},
	}
	ord := &mockOrderCommander{
		confirmFunc: func(ctx context.Context, orderID uuid.UUID) error {
			atomic.AddInt32(&confirmCount, 1)
			return nil
		},
	}
	pay := &mockPaymentCommander{
		authFunc: func(ctx context.Context, orderID, customerID uuid.UUID, amountMinor int64, currency, token, idemKey string) (uuid.UUID, error) {
			atomic.AddInt32(&authCount, 1)
			return uuid.New(), nil
		},
	}

	coord := NewCoordinator(repo, nil, inv, ord, pay, DefaultConfig(), slog.Default())

	orderID := uuid.New()
	saga, err := coord.StartSaga(ctx, SagaPayload{
		OrderID:          orderID,
		CustomerID:       uuid.New(),
		TotalAmountMinor: 10000,
		Currency:         "USD",
		Items:            []SagaItem{{SKU: "CONCURRENT-SKU", Quantity: 1, UnitPriceMinor: 10000}},
	})
	require.NoError(t, err)

	const concurrency = 50
	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = coord.ExecuteSaga(ctx, saga.SagaID)
		}()
	}
	wg.Wait()

	finalSaga, err := repo.GetSagaByID(ctx, saga.SagaID)
	require.NoError(t, err)
	assert.Equal(t, StateConfirmed, finalSaga.CurrentState)

	t.Logf("Concurrent executions result: reserveCount=%d, authCount=%d, confirmCount=%d",
		atomic.LoadInt32(&reserveCount),
		atomic.LoadInt32(&authCount),
		atomic.LoadInt32(&confirmCount),
	)

	// Confirm calls, reserve calls, and auth calls must each be exactly 1
	assert.Equal(t, int32(1), atomic.LoadInt32(&reserveCount), "Stock reservation should be called exactly once")
	assert.Equal(t, int32(1), atomic.LoadInt32(&authCount), "Payment authorization should be called exactly once")
	assert.Equal(t, int32(1), atomic.LoadInt32(&confirmCount), "Order should be confirmed exactly once")
}

// TestChallenge_WatchdogStressCycles_NoGoroutineLeaks tests multiple start/stop
// cycles of the Watchdog under load to verify zero goroutine leaks.
func TestChallenge_WatchdogStressCycles_NoGoroutineLeaks(t *testing.T) {
	defer goleak.VerifyNone(t)

	repo := newMockRepository()
	coord := NewCoordinator(repo, nil, &mockInventoryCommander{}, &mockOrderCommander{}, &mockPaymentCommander{}, DefaultConfig(), slog.Default())

	for i := 0; i < 15; i++ {
		wd := NewWatchdog(coord, repo, WatchdogConfig{PollInterval: 5 * time.Millisecond, BatchSize: 5}, slog.Default())
		ctx, cancel := context.WithCancel(context.Background())

		require.NoError(t, wd.Start(ctx))
		assert.True(t, wd.IsActive())

		time.Sleep(15 * time.Millisecond)

		stopCtx, stopCancel := context.WithTimeout(context.Background(), 1*time.Second)
		require.NoError(t, wd.Stop(stopCtx))
		assert.False(t, wd.IsActive())

		stopCancel()
		cancel()
	}
}

// TestChallenge_CompensationFailure_ThenWatchdogRecovery tests when compensation
// fails initially due to transient error, and the Watchdog subsequently retries and completes it.
func TestChallenge_CompensationFailure_ThenWatchdogRecovery(t *testing.T) {
	ctx := context.Background()
	repo := newMockRepository()
	resID := uuid.New()
	orderID := uuid.New()

	var releaseAttempts int32
	inv := &mockInventoryCommander{
		reserveFunc: func(ctx context.Context, orderID uuid.UUID, items []ReservationItem) (*ReservationResult, error) {
			return &ReservationResult{ReservationID: resID, Success: true}, nil
		},
		releaseFunc: func(ctx context.Context, reservationID uuid.UUID, reason string) error {
			if atomic.AddInt32(&releaseAttempts, 1) == 1 {
				return errors.New("temporary database connection drop")
			}
			return nil
		},
	}
	ord := &mockOrderCommander{}
	pay := &mockPaymentCommander{
		authFunc: func(ctx context.Context, orderID, customerID uuid.UUID, amountMinor int64, currency, token, idemKey string) (uuid.UUID, error) {
			return uuid.Nil, errors.New("card_declined")
		},
	}

	coord := NewCoordinator(repo, nil, inv, ord, pay, DefaultConfig(), slog.Default())
	wd := NewWatchdog(coord, repo, WatchdogConfig{PollInterval: 50 * time.Millisecond, BatchSize: 10}, slog.Default())

	saga, err := coord.StartSaga(ctx, SagaPayload{
		OrderID: orderID,
	})
	require.NoError(t, err)

	// Initial execution attempts payment, fails, attempts compensation, fails on releaseStock
	err = coord.ExecuteSaga(ctx, saga.SagaID)
	require.Error(t, err)

	// Saga should be in COMPENSATING state
	s, err := repo.GetSagaByID(ctx, saga.SagaID)
	require.NoError(t, err)
	assert.Equal(t, StateCompensating, s.CurrentState)

	// Fast-forward timeout to simulate expiration
	repo.mu.Lock()
	repo.sagas[saga.SagaID].TimeoutAt = time.Now().Add(-1 * time.Second)
	repo.mu.Unlock()

	// Watchdog processes expired sagas
	wd.ProcessExpiredSagas(ctx)

	// Verify that watchdog retried compensation and succeeded
	finalSaga, err := repo.GetSagaByID(ctx, saga.SagaID)
	require.NoError(t, err)
	assert.Equal(t, StateFailed, finalSaga.CurrentState)
	assert.Equal(t, int32(2), atomic.LoadInt32(&releaseAttempts))
	assert.Len(t, ord.cancelCalls, 1)
}

// TestChallenge_HugeScaleLineItemsSerialization verifies that 5,000 line items
// can be serialized, stored, retrieved, and compensated without memory explosion or panic.
func TestChallenge_HugeScaleLineItemsSerialization(t *testing.T) {
	const numItems = 5000
	items := make([]SagaItem, numItems)
	var expectedTotal int64
	for i := 0; i < numItems; i++ {
		qty := (i % 10) + 1
		price := int64(100 + (i % 50))
		items[i] = SagaItem{
			SKU:            fmt.Sprintf("SKU-STRESS-%06d", i),
			Quantity:       qty,
			UnitPriceMinor: price,
		}
		expectedTotal += int64(qty) * price
	}

	payload := SagaPayload{
		OrderID:          uuid.New(),
		CustomerID:       uuid.New(),
		TotalAmountMinor: expectedTotal,
		Currency:         "USD",
		Items:            items,
	}

	data, err := payload.Marshal()
	require.NoError(t, err)
	require.NotEmpty(t, data)

	restored, err := UnmarshalPayload(data)
	require.NoError(t, err)
	assert.Equal(t, expectedTotal, restored.TotalAmountMinor)
	assert.Len(t, restored.Items, numItems)
	assert.Equal(t, "SKU-STRESS-004999", restored.Items[numItems-1].SKU)
}

// failTransitionRepository injects a failure on specific state transition
type failTransitionRepository struct {
	*mockRepository
	failFrom SagaState
	failTo   SagaState
}

func (f *failTransitionRepository) TransitionState(ctx context.Context, tx pgx.Tx, sagaID uuid.UUID, fromState, toState SagaState, payload SagaPayload, nextTimeout time.Time) error {
	if fromState == f.failFrom && toState == f.failTo {
		return errors.New("injected database error during state transition")
	}
	return f.mockRepository.TransitionState(ctx, tx, sagaID, fromState, toState, payload, nextTimeout)
}

// TestChallenge_StockReservationLeak_OnTransitionFailure tests whether an inventory reservation
// is leaked if the database fails while transitioning from RESERVING_STOCK to STOCK_RESERVED.
func TestChallenge_StockReservationLeak_OnTransitionFailure(t *testing.T) {
	ctx := context.Background()
	baseRepo := newMockRepository()
	failRepo := &failTransitionRepository{
		mockRepository: baseRepo,
		failFrom:       StateReservingStock,
		failTo:         StateStockReserved,
	}

	resID := uuid.New()
	inv := &mockInventoryCommander{
		reserveFunc: func(ctx context.Context, orderID uuid.UUID, items []ReservationItem) (*ReservationResult, error) {
			return &ReservationResult{ReservationID: resID, Success: true}, nil
		},
	}
	ord := &mockOrderCommander{}
	pay := &mockPaymentCommander{}

	coord := NewCoordinator(failRepo, nil, inv, ord, pay, DefaultConfig(), slog.Default())

	orderID := uuid.New()
	saga, err := coord.StartSaga(ctx, SagaPayload{
		OrderID: orderID,
	})
	require.NoError(t, err)

	// Execute should fail because transition to STOCK_RESERVED fails
	err = coord.ExecuteSaga(ctx, saga.SagaID)
	require.Error(t, err)

	assert.Len(t, inv.reserveCalls, 1)
	assert.Len(t, inv.releaseCalls, 1, "Inventory reservation must be released when DB transition to STOCK_RESERVED failed")
	assert.Equal(t, resID, inv.releaseCalls[0])
}

// TestChallenge_PaidTimeout_WatchdogBehavior verifies whether the Watchdog
// handles sagas that time out while in the PAID state by releasing stock,
// refunding payment, and cancelling order.
func TestChallenge_PaidTimeout_WatchdogBehavior(t *testing.T) {
	ctx := context.Background()
	repo := newMockRepository()
	inv := &mockInventoryCommander{}
	ord := &mockOrderCommander{}
	pay := &mockPaymentCommander{}

	coord := NewCoordinator(repo, nil, inv, ord, pay, DefaultConfig(), slog.Default())
	wd := NewWatchdog(coord, repo, WatchdogConfig{PollInterval: 50 * time.Millisecond, BatchSize: 10}, slog.Default())

	orderID := uuid.New()
	sagaID := uuid.New()
	resID := uuid.New()
	payID := uuid.New()

	expiredSaga := &OrderSaga{
		SagaID:       sagaID,
		OrderID:      orderID,
		CurrentState: StatePaid,
		Payload: SagaPayload{
			OrderID:          orderID,
			ReservationID:    &resID,
			PaymentID:        &payID,
			TotalAmountMinor: 5000,
		},
		TimeoutAt: time.Now().Add(-10 * time.Second),
	}
	require.NoError(t, repo.CreateSaga(ctx, expiredSaga))

	repo.mu.Lock()
	repo.sagas[sagaID].CurrentState = StatePaid
	repo.sagas[sagaID].TimeoutAt = time.Now().Add(-10 * time.Second)
	repo.mu.Unlock()

	wd.ProcessExpiredSagas(ctx)

	updated, err := repo.GetSagaByID(ctx, sagaID)
	require.NoError(t, err)

	assert.Equal(t, StateFailed, updated.CurrentState)
	require.Len(t, inv.releaseCalls, 1, "ReleaseStock must be called when PAID saga times out")
	assert.Equal(t, resID, inv.releaseCalls[0])
	require.Len(t, pay.refundCalls, 1, "Refund must be called when PAID saga times out")
	assert.Equal(t, payID, pay.refundCalls[0])
	require.Len(t, ord.cancelCalls, 1, "CancelOrder must be called when PAID saga times out")
	assert.Equal(t, orderID, ord.cancelCalls[0])
}

// TestChallenge_CommitStockFailure_PostPayment verifies that when CommitStock fails
// after payment capture, ExecuteSaga returns an error, does NOT confirm the order,
// and does NOT transition the saga to CONFIRMED.
func TestChallenge_CommitStockFailure_PostPayment(t *testing.T) {
	ctx := context.Background()
	repo := newMockRepository()
	resID := uuid.New()
	inv := &mockInventoryCommander{
		reserveFunc: func(ctx context.Context, orderID uuid.UUID, items []ReservationItem) (*ReservationResult, error) {
			return &ReservationResult{ReservationID: resID, Success: true}, nil
		},
		commitFunc: func(ctx context.Context, reservationID uuid.UUID) error {
			return errors.New("transient commit error")
		},
	}
	ord := &mockOrderCommander{}
	pay := &mockPaymentCommander{}

	coord := NewCoordinator(repo, nil, inv, ord, pay, DefaultConfig(), slog.Default())

	orderID := uuid.New()
	saga, err := coord.StartSaga(ctx, SagaPayload{
		OrderID:          orderID,
		CustomerID:       uuid.New(),
		TotalAmountMinor: 5000,
		Currency:         "USD",
		Items:            []SagaItem{{SKU: "SKU-COMMIT-FAIL", Quantity: 1, UnitPriceMinor: 5000}},
	})
	require.NoError(t, err)

	err = coord.ExecuteSaga(ctx, saga.SagaID)
	require.Error(t, err, "ExecuteSaga must return error when commit stock fails")
	assert.Contains(t, err.Error(), "commit stock failed after payment")

	// Order must NOT be confirmed
	assert.Empty(t, ord.confirmCalls, "Order must NOT be confirmed if CommitStock failed")

	// Saga must NOT be in CONFIRMED state
	finalSaga, err := repo.GetSagaByID(ctx, saga.SagaID)
	require.NoError(t, err)
	assert.NotEqual(t, StateConfirmed, finalSaga.CurrentState, "Saga must NOT be marked CONFIRMED if CommitStock failed")
	assert.Equal(t, StatePaid, finalSaga.CurrentState)
}
