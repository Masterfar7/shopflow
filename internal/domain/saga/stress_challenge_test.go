package saga

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

// -----------------------------------------------------------------------------
// 1. Empirical Stress Test: StateStockReserved Timeout Compensation
// -----------------------------------------------------------------------------

func TestEmpirical_StateStockReserved_TimeoutCompensation_FullMatrix(t *testing.T) {
	ctx := context.Background()

	t.Run("Clean timeout triggers stock release and order cancellation", func(t *testing.T) {
		repo := newMockRepository()
		inv := &mockInventoryCommander{}
		ord := &mockOrderCommander{}
		pay := &mockPaymentCommander{}

		coord := NewCoordinator(repo, nil, inv, ord, pay, DefaultConfig(), slog.Default())
		wd := NewWatchdog(coord, repo, WatchdogConfig{PollInterval: 10 * time.Millisecond, BatchSize: 10}, slog.Default())

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
				Items:         []SagaItem{{SKU: "SKU-STK-TO", Quantity: 3, UnitPriceMinor: 1500}},
			},
			TimeoutAt: time.Now().Add(-10 * time.Second),
		}
		require.NoError(t, repo.CreateSaga(ctx, expiredSaga))
		repo.mu.Lock()
		repo.sagas[sagaID].CurrentState = StateStockReserved
		repo.sagas[sagaID].TimeoutAt = time.Now().Add(-10 * time.Second)
		repo.mu.Unlock()

		wd.ProcessExpiredSagas(ctx)

		s, err := repo.GetSagaByID(ctx, sagaID)
		require.NoError(t, err)
		assert.Equal(t, StateFailed, s.CurrentState)
		require.Len(t, inv.releaseCalls, 1, "Must release stock")
		assert.Equal(t, resID, inv.releaseCalls[0])
		require.Len(t, ord.cancelCalls, 1, "Must cancel order")
		assert.Equal(t, orderID, ord.cancelCalls[0])
		assert.Empty(t, pay.refundCalls, "No refund should happen since payment was not made")
	})

	t.Run("ReleaseStock transient failure then Watchdog recovery on next tick", func(t *testing.T) {
		repo := newMockRepository()
		var releaseAttempts int32
		resID := uuid.New()
		orderID := uuid.New()
		sagaID := uuid.New()

		inv := &mockInventoryCommander{
			releaseFunc: func(ctx context.Context, reservationID uuid.UUID, reason string) error {
				if atomic.AddInt32(&releaseAttempts, 1) == 1 {
					return errors.New("transient network timeout to inventory service")
				}
				return nil
			},
		}
		ord := &mockOrderCommander{}
		pay := &mockPaymentCommander{}

		coord := NewCoordinator(repo, nil, inv, ord, pay, DefaultConfig(), slog.Default())
		wd := NewWatchdog(coord, repo, WatchdogConfig{PollInterval: 10 * time.Millisecond, BatchSize: 10}, slog.Default())

		expiredSaga := &OrderSaga{
			SagaID:       sagaID,
			OrderID:      orderID,
			CurrentState: StateStockReserved,
			Payload: SagaPayload{
				OrderID:       orderID,
				ReservationID: &resID,
			},
			TimeoutAt: time.Now().Add(-5 * time.Second),
		}
		require.NoError(t, repo.CreateSaga(ctx, expiredSaga))
		repo.mu.Lock()
		repo.sagas[sagaID].CurrentState = StateStockReserved
		repo.sagas[sagaID].TimeoutAt = time.Now().Add(-5 * time.Second)
		repo.mu.Unlock()

		// First tick: Compensate fails at release stock -> stays in COMPENSATING
		wd.ProcessExpiredSagas(ctx)
		s1, err := repo.GetSagaByID(ctx, sagaID)
		require.NoError(t, err)
		assert.Equal(t, StateCompensating, s1.CurrentState)
		assert.Equal(t, int32(1), atomic.LoadInt32(&releaseAttempts))
		assert.Empty(t, ord.cancelCalls, "Order cancel must not be called if release stock failed")

		// Simulate timeout elapsed in COMPENSATING state
		repo.mu.Lock()
		repo.sagas[sagaID].TimeoutAt = time.Now().Add(-1 * time.Second)
		repo.mu.Unlock()

		// Second tick: Watchdog retries compensation -> succeeds
		wd.ProcessExpiredSagas(ctx)
		s2, err := repo.GetSagaByID(ctx, sagaID)
		require.NoError(t, err)
		assert.Equal(t, StateFailed, s2.CurrentState)
		assert.Equal(t, int32(2), atomic.LoadInt32(&releaseAttempts))
		assert.Len(t, ord.cancelCalls, 1)
	})

	t.Run("Batch expiration of 20 StateStockReserved sagas simultaneously", func(t *testing.T) {
		repo := newMockRepository()
		var releasedCount int32
		var cancelledCount int32

		inv := &mockInventoryCommander{
			releaseFunc: func(ctx context.Context, reservationID uuid.UUID, reason string) error {
				atomic.AddInt32(&releasedCount, 1)
				return nil
			},
		}
		ord := &mockOrderCommander{
			cancelFunc: func(ctx context.Context, orderID uuid.UUID, reason string) error {
				atomic.AddInt32(&cancelledCount, 1)
				return nil
			},
		}
		pay := &mockPaymentCommander{}

		coord := NewCoordinator(repo, nil, inv, ord, pay, DefaultConfig(), slog.Default())
		wd := NewWatchdog(coord, repo, WatchdogConfig{PollInterval: 10 * time.Millisecond, BatchSize: 50}, slog.Default())

		const batch = 20
		sagaIDs := make([]uuid.UUID, batch)
		for i := 0; i < batch; i++ {
			sID := uuid.New()
			oID := uuid.New()
			rID := uuid.New()
			sagaIDs[i] = sID
			s := &OrderSaga{
				SagaID:       sID,
				OrderID:      oID,
				CurrentState: StateStockReserved,
				Payload: SagaPayload{
					OrderID:       oID,
					ReservationID: &rID,
				},
				TimeoutAt: time.Now().Add(-5 * time.Second),
			}
			require.NoError(t, repo.CreateSaga(ctx, s))
			repo.mu.Lock()
			repo.sagas[sID].CurrentState = StateStockReserved
			repo.sagas[sID].TimeoutAt = time.Now().Add(-5 * time.Second)
			repo.mu.Unlock()
		}

		wd.ProcessExpiredSagas(ctx)

		assert.Equal(t, int32(batch), atomic.LoadInt32(&releasedCount), "All 20 reservations must be released")
		assert.Equal(t, int32(batch), atomic.LoadInt32(&cancelledCount), "All 20 orders must be cancelled")

		for _, sID := range sagaIDs {
			s, err := repo.GetSagaByID(ctx, sID)
			require.NoError(t, err)
			assert.Equal(t, StateFailed, s.CurrentState)
		}
	})
}

// -----------------------------------------------------------------------------
// 2. Empirical Stress Test: StatePaid Timeout Compensation
// -----------------------------------------------------------------------------

func TestEmpirical_StatePaid_TimeoutCompensation_FullMatrix(t *testing.T) {
	ctx := context.Background()

	t.Run("Clean timeout in StatePaid refunds payment, releases stock, and cancels order", func(t *testing.T) {
		repo := newMockRepository()
		inv := &mockInventoryCommander{}
		ord := &mockOrderCommander{}
		pay := &mockPaymentCommander{}

		coord := NewCoordinator(repo, nil, inv, ord, pay, DefaultConfig(), slog.Default())
		wd := NewWatchdog(coord, repo, WatchdogConfig{PollInterval: 10 * time.Millisecond, BatchSize: 10}, slog.Default())

		orderID := uuid.New()
		sagaID := uuid.New()
		resID := uuid.New()
		payID := uuid.New()
		const amount = int64(14999)

		expiredSaga := &OrderSaga{
			SagaID:       sagaID,
			OrderID:      orderID,
			CurrentState: StatePaid,
			Payload: SagaPayload{
				OrderID:          orderID,
				ReservationID:    &resID,
				PaymentID:        &payID,
				TotalAmountMinor: amount,
				Currency:         "USD",
			},
			TimeoutAt: time.Now().Add(-10 * time.Second),
		}
		require.NoError(t, repo.CreateSaga(ctx, expiredSaga))
		repo.mu.Lock()
		repo.sagas[sagaID].CurrentState = StatePaid
		repo.sagas[sagaID].TimeoutAt = time.Now().Add(-10 * time.Second)
		repo.mu.Unlock()

		wd.ProcessExpiredSagas(ctx)

		s, err := repo.GetSagaByID(ctx, sagaID)
		require.NoError(t, err)
		assert.Equal(t, StateFailed, s.CurrentState)

		require.Len(t, inv.releaseCalls, 1, "Must release stock")
		assert.Equal(t, resID, inv.releaseCalls[0])

		require.Len(t, pay.refundCalls, 1, "Must refund payment")
		assert.Equal(t, payID, pay.refundCalls[0])

		require.Len(t, ord.cancelCalls, 1, "Must cancel order")
		assert.Equal(t, orderID, ord.cancelCalls[0])
	})

	t.Run("Refund failure keeps saga in COMPENSATING and retries successfully on next tick", func(t *testing.T) {
		repo := newMockRepository()
		var refundAttempts int32
		resID := uuid.New()
		payID := uuid.New()
		orderID := uuid.New()
		sagaID := uuid.New()

		inv := &mockInventoryCommander{}
		ord := &mockOrderCommander{}
		pay := &mockPaymentCommander{
			refundFunc: func(ctx context.Context, paymentID, orderID uuid.UUID, amountMinor int64, reason, idemKey string) error {
				if atomic.AddInt32(&refundAttempts, 1) == 1 {
					return errors.New("gateway unavailable for refund")
				}
				return nil
			},
		}

		coord := NewCoordinator(repo, nil, inv, ord, pay, DefaultConfig(), slog.Default())
		wd := NewWatchdog(coord, repo, WatchdogConfig{PollInterval: 10 * time.Millisecond, BatchSize: 10}, slog.Default())

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
			TimeoutAt: time.Now().Add(-5 * time.Second),
		}
		require.NoError(t, repo.CreateSaga(ctx, expiredSaga))
		repo.mu.Lock()
		repo.sagas[sagaID].CurrentState = StatePaid
		repo.sagas[sagaID].TimeoutAt = time.Now().Add(-5 * time.Second)
		repo.mu.Unlock()

		// Tick 1: Refund fails
		wd.ProcessExpiredSagas(ctx)
		s1, err := repo.GetSagaByID(ctx, sagaID)
		require.NoError(t, err)
		assert.Equal(t, StateCompensating, s1.CurrentState)
		assert.Equal(t, int32(1), atomic.LoadInt32(&refundAttempts))
		assert.Empty(t, ord.cancelCalls)

		// Simulate timeout elapsed
		repo.mu.Lock()
		repo.sagas[sagaID].TimeoutAt = time.Now().Add(-1 * time.Second)
		repo.mu.Unlock()

		// Tick 2: Retries and completes
		wd.ProcessExpiredSagas(ctx)
		s2, err := repo.GetSagaByID(ctx, sagaID)
		require.NoError(t, err)
		assert.Equal(t, StateFailed, s2.CurrentState)
		assert.Equal(t, int32(2), atomic.LoadInt32(&refundAttempts))
		assert.Len(t, ord.cancelCalls, 1)
	})
}

// -----------------------------------------------------------------------------
// 3. Empirical Stress Test: State Transition Failure After Stock Reservation
// -----------------------------------------------------------------------------

func TestEmpirical_StockReservationCleanRelease_OnTransitionFailure(t *testing.T) {
	ctx := context.Background()

	t.Run("ReleaseStock called with exact reservation ID and reason when transition fails", func(t *testing.T) {
		baseRepo := newMockRepository()
		failRepo := &failTransitionRepository{
			mockRepository: baseRepo,
			failFrom:       StateReservingStock,
			failTo:         StateStockReserved,
		}

		resID := uuid.New()
		var capturedReason string
		inv := &mockInventoryCommander{
			reserveFunc: func(ctx context.Context, orderID uuid.UUID, items []ReservationItem) (*ReservationResult, error) {
				return &ReservationResult{ReservationID: resID, Success: true}, nil
			},
			releaseFunc: func(ctx context.Context, reservationID uuid.UUID, reason string) error {
				capturedReason = reason
				return nil
			},
		}
		ord := &mockOrderCommander{}
		pay := &mockPaymentCommander{}

		coord := NewCoordinator(failRepo, nil, inv, ord, pay, DefaultConfig(), slog.Default())

		orderID := uuid.New()
		saga, err := coord.StartSaga(ctx, SagaPayload{OrderID: orderID})
		require.NoError(t, err)

		err = coord.ExecuteSaga(ctx, saga.SagaID)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "injected database error")

		require.Len(t, inv.reserveCalls, 1)
		require.Len(t, inv.releaseCalls, 1, "Must cleanly release stock")
		assert.Equal(t, resID, inv.releaseCalls[0])
		assert.Equal(t, "transition_failed", capturedReason)
		assert.Empty(t, pay.authCalls, "Payment must not be attempted")
	})

	t.Run("ReleaseStock fails after transition error does not mask original error", func(t *testing.T) {
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
			releaseFunc: func(ctx context.Context, reservationID uuid.UUID, reason string) error {
				return errors.New("inventory connection dropped")
			},
		}

		coord := NewCoordinator(failRepo, nil, inv, &mockOrderCommander{}, &mockPaymentCommander{}, DefaultConfig(), slog.Default())

		saga, err := coord.StartSaga(ctx, SagaPayload{OrderID: uuid.New()})
		require.NoError(t, err)

		err = coord.ExecuteSaga(ctx, saga.SagaID)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "injected database error during state transition")
	})
}

// -----------------------------------------------------------------------------
// 4. Empirical Stress Test: Concurrent ExecuteSaga Invariance
// -----------------------------------------------------------------------------

func TestEmpirical_ConcurrentExecuteSaga_ExtremeLoad(t *testing.T) {
	defer goleak.VerifyNone(t)

	ctx := context.Background()
	repo := newMockRepository()

	var reserveCount int32
	var authCount int32
	var commitCount int32
	var confirmCount int32

	inv := &mockInventoryCommander{
		reserveFunc: func(ctx context.Context, orderID uuid.UUID, items []ReservationItem) (*ReservationResult, error) {
			// Small jitter to maximize race window
			time.Sleep(2 * time.Millisecond)
			atomic.AddInt32(&reserveCount, 1)
			return &ReservationResult{ReservationID: uuid.New(), Success: true}, nil
		},
		commitFunc: func(ctx context.Context, reservationID uuid.UUID) error {
			atomic.AddInt32(&commitCount, 1)
			return nil
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
			time.Sleep(2 * time.Millisecond)
			atomic.AddInt32(&authCount, 1)
			return uuid.New(), nil
		},
	}

	coord := NewCoordinator(repo, nil, inv, ord, pay, DefaultConfig(), slog.Default())

	orderID := uuid.New()
	saga, err := coord.StartSaga(ctx, SagaPayload{
		OrderID:          orderID,
		CustomerID:       uuid.New(),
		TotalAmountMinor: 25000,
		Currency:         "USD",
		Items:            []SagaItem{{SKU: "CONCURRENT-HEAVY", Quantity: 5, UnitPriceMinor: 5000}},
	})
	require.NoError(t, err)

	const concurrency = 100
	var wg sync.WaitGroup
	errs := make([]error, concurrency)

	for i := 0; i < concurrency; i++ {
		idx := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[idx] = coord.ExecuteSaga(ctx, saga.SagaID)
		}()
	}
	wg.Wait()

	finalSaga, err := repo.GetSagaByID(ctx, saga.SagaID)
	require.NoError(t, err)
	assert.Equal(t, StateConfirmed, finalSaga.CurrentState)

	// INVARIANT: Exactly 1 reservation, exactly 1 auth, exactly 1 commit, exactly 1 confirm
	assert.Equal(t, int32(1), atomic.LoadInt32(&reserveCount), "Stock reservation MUST occur exactly once across 100 concurrent attempts")
	assert.Equal(t, int32(1), atomic.LoadInt32(&authCount), "Payment authorization MUST occur exactly once")
	assert.Equal(t, int32(1), atomic.LoadInt32(&commitCount), "Stock commit MUST occur exactly once")
	assert.Equal(t, int32(1), atomic.LoadInt32(&confirmCount), "Order confirmation MUST occur exactly once")

	// Count successful executions (1 wins, 99 get rejection / optimistic lock)
	var successes int
	for _, e := range errs {
		if e == nil {
			successes++
		}
	}
	assert.Equal(t, 1, successes, "Exactly one goroutine must report success")
}

func TestEmpirical_ExecuteSaga_RejectionWhenAlreadyInFlight(t *testing.T) {
	ctx := context.Background()
	repo := newMockRepository()
	inv := &mockInventoryCommander{}
	ord := &mockOrderCommander{}
	pay := &mockPaymentCommander{}
	coord := NewCoordinator(repo, nil, inv, ord, pay, DefaultConfig(), slog.Default())

	states := []SagaState{
		StateReservingStock,
		StateStockReserved,
		StatePaying,
		StatePaid,
		StateCompensating,
	}

	for _, st := range states {
		t.Run(string(st), func(t *testing.T) {
			sID := uuid.New()
			oID := uuid.New()
			s := &OrderSaga{
				SagaID:       sID,
				OrderID:      oID,
				CurrentState: st,
			}
			require.NoError(t, repo.CreateSaga(ctx, s))
			repo.mu.Lock()
			repo.sagas[sID].CurrentState = st
			repo.mu.Unlock()

			err := coord.ExecuteSaga(ctx, sID)
			require.Error(t, err, "ExecuteSaga must be rejected when saga is in state %s", st)
			assert.Empty(t, inv.reserveCalls, "No stock reservation should occur")
		})
	}
}

// -----------------------------------------------------------------------------
// 5. Empirical Stress Test: Zero Goroutine Leaks on Watchdog Shutdown
// -----------------------------------------------------------------------------

func TestEmpirical_WatchdogShutdown_ZeroGoroutineLeaks_Heavy(t *testing.T) {
	defer goleak.VerifyNone(t)

	repo := newMockRepository()
	coord := NewCoordinator(repo, nil, &mockInventoryCommander{}, &mockOrderCommander{}, &mockPaymentCommander{}, DefaultConfig(), slog.Default())

	// 1. Rapid sequential Start/Stop cycles
	for i := 0; i < 25; i++ {
		wd := NewWatchdog(coord, repo, WatchdogConfig{PollInterval: 2 * time.Millisecond, BatchSize: 5}, slog.Default())
		ctx, cancel := context.WithCancel(context.Background())

		require.NoError(t, wd.Start(ctx))
		assert.True(t, wd.IsActive())

		time.Sleep(5 * time.Millisecond)

		stopCtx, stopCancel := context.WithTimeout(context.Background(), 1*time.Second)
		require.NoError(t, wd.Stop(stopCtx))
		assert.False(t, wd.IsActive())

		stopCancel()
		cancel()
	}

	// 2. Concurrent Start/Stop attempts
	wdConcurrent := NewWatchdog(coord, repo, WatchdogConfig{PollInterval: 5 * time.Millisecond, BatchSize: 5}, slog.Default())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, wdConcurrent.Start(ctx))

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			stopCtx, stopCancel := context.WithTimeout(context.Background(), 1*time.Second)
			defer stopCancel()
			_ = wdConcurrent.Stop(stopCtx)
		}()
	}
	wg.Wait()
	assert.False(t, wdConcurrent.IsActive())
}
