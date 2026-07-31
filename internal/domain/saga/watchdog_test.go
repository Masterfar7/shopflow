package saga

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

func TestWatchdog_StockReservationTimeout(t *testing.T) {
	ctx := context.Background()
	repo := newMockRepository()
	inv := &mockInventoryCommander{}
	ord := &mockOrderCommander{}
	pay := &mockPaymentCommander{}

	coord := NewCoordinator(repo, nil, inv, ord, pay, DefaultConfig(), slog.Default())
	wd := NewWatchdog(coord, repo, WatchdogConfig{PollInterval: 50 * time.Millisecond, BatchSize: 10}, slog.Default())

	orderID := uuid.New()
	sagaID := uuid.New()
	expiredSaga := &OrderSaga{
		SagaID:       sagaID,
		OrderID:      orderID,
		CurrentState: StateReservingStock,
		Payload: SagaPayload{
			OrderID: orderID,
			Items: []SagaItem{
				{SKU: "SKU-EXP-1", Quantity: 2},
			},
		},
		TimeoutAt: time.Now().Add(-5 * time.Second),
	}
	require.NoError(t, repo.CreateSaga(ctx, expiredSaga))

	// Manually force current_state to RESERVING_STOCK and timeout in the past in mock
	repo.mu.Lock()
	repo.sagas[sagaID].CurrentState = StateReservingStock
	repo.sagas[sagaID].TimeoutAt = time.Now().Add(-5 * time.Second)
	repo.mu.Unlock()

	// Process expired sagas
	wd.ProcessExpiredSagas(ctx)

	// Order cancelled, saga marked failed, no stock release because no reservation was completed
	require.Len(t, ord.cancelCalls, 1)
	assert.Equal(t, orderID, ord.cancelCalls[0])
	assert.Empty(t, inv.releaseCalls)

	updated, err := repo.GetSagaByID(ctx, sagaID)
	require.NoError(t, err)
	assert.Equal(t, StateFailed, updated.CurrentState)
}

func TestWatchdog_PaymentTimeout(t *testing.T) {
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
		CurrentState: StatePaying,
		Payload: SagaPayload{
			OrderID:       orderID,
			ReservationID: &resID,
		},
		TimeoutAt: time.Now().Add(-10 * time.Second),
	}
	require.NoError(t, repo.CreateSaga(ctx, expiredSaga))

	repo.mu.Lock()
	repo.sagas[sagaID].CurrentState = StatePaying
	repo.sagas[sagaID].TimeoutAt = time.Now().Add(-10 * time.Second)
	repo.mu.Unlock()

	wd.ProcessExpiredSagas(ctx)

	// Compensation triggered: ReleaseStock called and CancelOrder called
	require.Len(t, inv.releaseCalls, 1)
	assert.Equal(t, resID, inv.releaseCalls[0])
	require.Len(t, ord.cancelCalls, 1)
	assert.Equal(t, orderID, ord.cancelCalls[0])

	updated, err := repo.GetSagaByID(ctx, sagaID)
	require.NoError(t, err)
	assert.Equal(t, StateFailed, updated.CurrentState)
}

func TestWatchdog_GracefulShutdown_NoGoroutineLeaks(t *testing.T) {
	defer goleak.VerifyNone(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	repo := newMockRepository()
	inv := &mockInventoryCommander{}
	ord := &mockOrderCommander{}
	pay := &mockPaymentCommander{}

	coord := NewCoordinator(repo, nil, inv, ord, pay, DefaultConfig(), slog.Default())
	wd := NewWatchdog(coord, repo, WatchdogConfig{PollInterval: 10 * time.Millisecond, BatchSize: 5}, slog.Default())

	err := wd.Start(ctx)
	require.NoError(t, err)
	assert.True(t, wd.IsActive())

	// Allow loop to tick briefly
	time.Sleep(30 * time.Millisecond)

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer stopCancel()

	err = wd.Stop(stopCtx)
	require.NoError(t, err)
	assert.False(t, wd.IsActive())
}

func TestWatchdog_DoubleStartAndStop(t *testing.T) {
	ctx := context.Background()
	repo := newMockRepository()
	coord := NewCoordinator(repo, nil, &mockInventoryCommander{}, &mockOrderCommander{}, &mockPaymentCommander{}, DefaultConfig(), slog.Default())
	wd := NewWatchdog(coord, repo, WatchdogConfig{PollInterval: 50 * time.Millisecond, BatchSize: 5}, slog.Default())

	require.NoError(t, wd.Start(ctx))
	assert.Error(t, wd.Start(ctx), "double start should return error")

	require.NoError(t, wd.Stop(ctx))
	assert.NoError(t, wd.Stop(ctx), "double stop should be idempotent and return nil")
}

func TestWatchdog_StockReservedTimeout(t *testing.T) {
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

	require.Len(t, inv.releaseCalls, 1)
	assert.Equal(t, resID, inv.releaseCalls[0])
	require.Len(t, ord.cancelCalls, 1)
	assert.Equal(t, orderID, ord.cancelCalls[0])

	updated, err := repo.GetSagaByID(ctx, sagaID)
	require.NoError(t, err)
	assert.Equal(t, StateFailed, updated.CurrentState)
}

func TestWatchdog_PaidTimeout(t *testing.T) {
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
			TotalAmountMinor: 4999,
		},
		TimeoutAt: time.Now().Add(-10 * time.Second),
	}
	require.NoError(t, repo.CreateSaga(ctx, expiredSaga))

	repo.mu.Lock()
	repo.sagas[sagaID].CurrentState = StatePaid
	repo.sagas[sagaID].TimeoutAt = time.Now().Add(-10 * time.Second)
	repo.mu.Unlock()

	wd.ProcessExpiredSagas(ctx)

	require.Len(t, inv.releaseCalls, 1)
	assert.Equal(t, resID, inv.releaseCalls[0])
	require.Len(t, pay.refundCalls, 1)
	assert.Equal(t, payID, pay.refundCalls[0])
	require.Len(t, ord.cancelCalls, 1)
	assert.Equal(t, orderID, ord.cancelCalls[0])

	updated, err := repo.GetSagaByID(ctx, sagaID)
	require.NoError(t, err)
	assert.Equal(t, StateFailed, updated.CurrentState)
}
