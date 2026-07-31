package saga

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// In-memory mock repository for unit testing
type mockRepository struct {
	mu    sync.Mutex
	sagas map[uuid.UUID]*OrderSaga
	logs  map[uuid.UUID][]OrderSagaLog
}

func newMockRepository() *mockRepository {
	return &mockRepository{
		sagas: make(map[uuid.UUID]*OrderSaga),
		logs:  make(map[uuid.UUID][]OrderSagaLog),
	}
}

func (m *mockRepository) CreateSaga(ctx context.Context, saga *OrderSaga) error {
	return m.CreateSagaTx(ctx, nil, saga)
}

func (m *mockRepository) CreateSagaTx(ctx context.Context, tx pgx.Tx, saga *OrderSaga) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, existing := range m.sagas {
		if existing.OrderID == saga.OrderID {
			return ErrSagaAlreadyExists
		}
	}

	if saga.SagaID == uuid.Nil {
		saga.SagaID = uuid.New()
	}
	if saga.CurrentState == "" {
		saga.CurrentState = StateStarted
	}
	if saga.TimeoutAt.IsZero() {
		saga.TimeoutAt = time.Now().Add(60 * time.Second)
	}
	saga.CreatedAt = time.Now()
	saga.UpdatedAt = time.Now()

	cp := *saga
	m.sagas[saga.SagaID] = &cp

	initLog := OrderSagaLog{
		ID:        int64(len(m.logs[saga.SagaID]) + 1),
		SagaID:    saga.SagaID,
		FromState: "",
		ToState:   saga.CurrentState,
		EventType: "SagaInitialized",
		EventID:   uuid.New().String(),
		CreatedAt: time.Now(),
	}
	m.logs[saga.SagaID] = append(m.logs[saga.SagaID], initLog)
	return nil
}

func (m *mockRepository) GetSagaByID(ctx context.Context, sagaID uuid.UUID) (*OrderSaga, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sagas[sagaID]
	if !ok {
		return nil, ErrSagaNotFound
	}
	cp := *s
	return &cp, nil
}

func (m *mockRepository) GetSagaByOrderID(ctx context.Context, orderID uuid.UUID) (*OrderSaga, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, s := range m.sagas {
		if s.OrderID == orderID {
			cp := *s
			return &cp, nil
		}
	}
	return nil, ErrSagaNotFound
}

func (m *mockRepository) GetSagaForUpdate(ctx context.Context, tx pgx.Tx, sagaID uuid.UUID) (*OrderSaga, error) {
	return m.GetSagaByID(ctx, sagaID)
}

func (m *mockRepository) GetSagaByOrderIDForUpdate(ctx context.Context, tx pgx.Tx, orderID uuid.UUID) (*OrderSaga, error) {
	return m.GetSagaByOrderID(ctx, orderID)
}

func (m *mockRepository) TransitionState(
	ctx context.Context,
	tx pgx.Tx,
	sagaID uuid.UUID,
	fromState, toState SagaState,
	payload SagaPayload,
	nextTimeout time.Time,
) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sagas[sagaID]
	if !ok {
		return ErrSagaNotFound
	}

	if IsTerminal(s.CurrentState) {
		return ErrSagaAlreadyTerminal
	}

	if !CanTransition(fromState, toState) {
		return fmt.Errorf("%w: cannot transition from %s to %s", ErrInvalidStateTransition, fromState, toState)
	}

	if s.CurrentState != fromState {
		return fmt.Errorf("%w: state changed from %s to %s", ErrOptimisticLockConflict, fromState, s.CurrentState)
	}

	s.CurrentState = toState
	s.Payload = payload
	s.TimeoutAt = nextTimeout
	s.UpdatedAt = time.Now()
	return nil
}

func (m *mockRepository) AppendLog(ctx context.Context, tx pgx.Tx, log *OrderSagaLog) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	log.ID = int64(len(m.logs[log.SagaID]) + 1)
	log.CreatedAt = time.Now()
	m.logs[log.SagaID] = append(m.logs[log.SagaID], *log)
	return nil
}

func (m *mockRepository) GetLogs(ctx context.Context, sagaID uuid.UUID) ([]OrderSagaLog, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	logs, ok := m.logs[sagaID]
	if !ok {
		return nil, nil
	}
	res := make([]OrderSagaLog, len(logs))
	copy(res, logs)
	return res, nil
}

func (m *mockRepository) FindTimedOutSagas(ctx context.Context, limit int) ([]*OrderSaga, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	var res []*OrderSaga
	for _, s := range m.sagas {
		if !IsTerminal(s.CurrentState) && !s.TimeoutAt.IsZero() && (s.TimeoutAt.Before(now) || s.TimeoutAt.Equal(now)) {
			cp := *s
			res = append(res, &cp)
			if limit > 0 && len(res) >= limit {
				break
			}
		}
	}
	return res, nil
}

func (m *mockRepository) FindTimedOutSagasTx(ctx context.Context, tx pgx.Tx, limit int) ([]*OrderSaga, error) {
	return m.FindTimedOutSagas(ctx, limit)
}

// Mock Commanders
type mockInventoryCommander struct {
	reserveFunc func(ctx context.Context, orderID uuid.UUID, items []ReservationItem) (*ReservationResult, error)
	releaseFunc func(ctx context.Context, reservationID uuid.UUID, reason string) error
	commitFunc  func(ctx context.Context, reservationID uuid.UUID) error

	reserveCalls []uuid.UUID
	releaseCalls []uuid.UUID
	commitCalls  []uuid.UUID
}

func (m *mockInventoryCommander) ReserveStock(ctx context.Context, orderID uuid.UUID, items []ReservationItem) (*ReservationResult, error) {
	m.reserveCalls = append(m.reserveCalls, orderID)
	if m.reserveFunc != nil {
		return m.reserveFunc(ctx, orderID, items)
	}
	return &ReservationResult{ReservationID: uuid.New(), Success: true}, nil
}

func (m *mockInventoryCommander) ReleaseStock(ctx context.Context, reservationID uuid.UUID, reason string) error {
	m.releaseCalls = append(m.releaseCalls, reservationID)
	if m.releaseFunc != nil {
		return m.releaseFunc(ctx, reservationID, reason)
	}
	return nil
}

func (m *mockInventoryCommander) CommitStock(ctx context.Context, reservationID uuid.UUID) error {
	m.commitCalls = append(m.commitCalls, reservationID)
	if m.commitFunc != nil {
		return m.commitFunc(ctx, reservationID)
	}
	return nil
}

type mockOrderCommander struct {
	updateStatusFunc func(ctx context.Context, orderID uuid.UUID, status string) error
	cancelFunc       func(ctx context.Context, orderID uuid.UUID, reason string) error
	confirmFunc      func(ctx context.Context, orderID uuid.UUID) error

	cancelCalls  []uuid.UUID
	confirmCalls []uuid.UUID
}

func (m *mockOrderCommander) UpdateStatus(ctx context.Context, orderID uuid.UUID, status string) error {
	if m.updateStatusFunc != nil {
		return m.updateStatusFunc(ctx, orderID, status)
	}
	return nil
}

func (m *mockOrderCommander) CancelOrder(ctx context.Context, orderID uuid.UUID, reason string) error {
	m.cancelCalls = append(m.cancelCalls, orderID)
	if m.cancelFunc != nil {
		return m.cancelFunc(ctx, orderID, reason)
	}
	return nil
}

func (m *mockOrderCommander) ConfirmOrder(ctx context.Context, orderID uuid.UUID) error {
	m.confirmCalls = append(m.confirmCalls, orderID)
	if m.confirmFunc != nil {
		return m.confirmFunc(ctx, orderID)
	}
	return nil
}

type mockPaymentCommander struct {
	authFunc   func(ctx context.Context, orderID, customerID uuid.UUID, amountMinor int64, currency, token, idemKey string) (uuid.UUID, error)
	refundFunc func(ctx context.Context, paymentID, orderID uuid.UUID, amountMinor int64, reason, idemKey string) error

	authCalls   []uuid.UUID
	refundCalls []uuid.UUID
}

func (m *mockPaymentCommander) AuthorizeAndCapture(
	ctx context.Context,
	orderID, customerID uuid.UUID,
	amountMinor int64,
	currency, token, idemKey string,
) (uuid.UUID, error) {
	m.authCalls = append(m.authCalls, orderID)
	if m.authFunc != nil {
		return m.authFunc(ctx, orderID, customerID, amountMinor, currency, token, idemKey)
	}
	return uuid.New(), nil
}

func (m *mockPaymentCommander) Refund(
	ctx context.Context,
	paymentID, orderID uuid.UUID,
	amountMinor int64,
	reason, idemKey string,
) error {
	m.refundCalls = append(m.refundCalls, paymentID)
	if m.refundFunc != nil {
		return m.refundFunc(ctx, paymentID, orderID, amountMinor, reason, idemKey)
	}
	return nil
}

func TestCoordinator_HappyPath_FullLifecycle(t *testing.T) {
	ctx := context.Background()
	repo := newMockRepository()
	inv := &mockInventoryCommander{}
	ord := &mockOrderCommander{}
	pay := &mockPaymentCommander{}

	coord := NewCoordinator(repo, nil, inv, ord, pay, DefaultConfig(), slog.Default())

	orderID := uuid.New()
	customerID := uuid.New()
	payload := SagaPayload{
		OrderID:          orderID,
		CustomerID:       customerID,
		TotalAmountMinor: 4999,
		Currency:         "USD",
		Items: []SagaItem{
			{SKU: "SKU-TEST-1", Quantity: 2, UnitPriceMinor: 2000},
			{SKU: "SKU-TEST-2", Quantity: 1, UnitPriceMinor: 999},
		},
		PaymentToken: "tok_valid",
	}

	saga, err := coord.StartSaga(ctx, payload)
	require.NoError(t, err)
	assert.Equal(t, StateStarted, saga.CurrentState)

	err = coord.ExecuteSaga(ctx, saga.SagaID)
	require.NoError(t, err)

	// Verify final state
	finalSaga, err := repo.GetSagaByID(ctx, saga.SagaID)
	require.NoError(t, err)
	assert.Equal(t, StateConfirmed, finalSaga.CurrentState)
	assert.NotNil(t, finalSaga.Payload.ReservationID)
	assert.NotNil(t, finalSaga.Payload.PaymentID)

	// Verify commander calls
	assert.Len(t, inv.reserveCalls, 1)
	assert.Len(t, inv.commitCalls, 1)
	assert.Empty(t, inv.releaseCalls, "ReleaseStock should not be called on happy path")
	assert.Len(t, ord.confirmCalls, 1)
	assert.Empty(t, ord.cancelCalls)
	assert.Len(t, pay.authCalls, 1)

	// Verify audit logs
	logs, err := repo.GetLogs(ctx, saga.SagaID)
	require.NoError(t, err)
	// Initialized + Started->Reserving + Reserving->Reserved + Reserved->Paying + Paying->Paid + Paid->Confirmed = 6 logs
	assert.GreaterOrEqual(t, len(logs), 6)
	assert.Equal(t, StateConfirmed, logs[len(logs)-1].ToState)
}

func TestCoordinator_StockReservationFailure_ZeroCompensation(t *testing.T) {
	ctx := context.Background()
	repo := newMockRepository()
	inv := &mockInventoryCommander{
		reserveFunc: func(ctx context.Context, orderID uuid.UUID, items []ReservationItem) (*ReservationResult, error) {
			return &ReservationResult{
				Success:      false,
				FailedSKUs:   []string{"SKU-OOS"},
				ErrorMessage: "out of stock for SKU-OOS",
			}, errors.New("out of stock")
		},
	}
	ord := &mockOrderCommander{}
	pay := &mockPaymentCommander{}

	coord := NewCoordinator(repo, nil, inv, ord, pay, DefaultConfig(), slog.Default())

	orderID := uuid.New()
	payload := SagaPayload{
		OrderID:          orderID,
		CustomerID:       uuid.New(),
		TotalAmountMinor: 1000,
		Currency:         "USD",
		Items: []SagaItem{
			{SKU: "SKU-OOS", Quantity: 5, UnitPriceMinor: 200},
		},
	}

	saga, err := coord.StartSaga(ctx, payload)
	require.NoError(t, err)

	err = coord.ExecuteSaga(ctx, saga.SagaID)
	// ExecuteSaga returns stock reservation error
	assert.Error(t, err)

	finalSaga, err := repo.GetSagaByID(ctx, saga.SagaID)
	require.NoError(t, err)
	assert.Equal(t, StateFailed, finalSaga.CurrentState)

	// Invariant: Since stock reservation failed, ReleaseStock must NOT be called (zero stock leak)
	assert.Empty(t, inv.releaseCalls, "ReleaseStock must not be called when stock reservation failed")
	assert.Empty(t, inv.commitCalls)
	assert.Len(t, ord.cancelCalls, 1, "Order must be cancelled on stock failure")
	assert.Empty(t, pay.authCalls, "Payment must not be attempted when stock reservation failed")
}

func TestCoordinator_PaymentFailure_AutomatedCompensation(t *testing.T) {
	ctx := context.Background()
	repo := newMockRepository()
	resID := uuid.New()
	inv := &mockInventoryCommander{
		reserveFunc: func(ctx context.Context, orderID uuid.UUID, items []ReservationItem) (*ReservationResult, error) {
			return &ReservationResult{ReservationID: resID, Success: true}, nil
		},
	}
	ord := &mockOrderCommander{}
	pay := &mockPaymentCommander{
		authFunc: func(ctx context.Context, orderID, customerID uuid.UUID, amountMinor int64, currency, token, idemKey string) (uuid.UUID, error) {
			return uuid.Nil, errors.New("card_declined: insufficient funds")
		},
	}

	coord := NewCoordinator(repo, nil, inv, ord, pay, DefaultConfig(), slog.Default())

	orderID := uuid.New()
	payload := SagaPayload{
		OrderID:          orderID,
		CustomerID:       uuid.New(),
		TotalAmountMinor: 3000,
		Currency:         "USD",
		Items: []SagaItem{
			{SKU: "SKU-1", Quantity: 1, UnitPriceMinor: 3000},
		},
		PaymentToken: "tok_declined",
	}

	saga, err := coord.StartSaga(ctx, payload)
	require.NoError(t, err)

	err = coord.ExecuteSaga(ctx, saga.SagaID)
	// ExecuteSaga handles payment failure via compensation and returns compensation result (or nil on success)
	require.NoError(t, err)

	finalSaga, err := repo.GetSagaByID(ctx, saga.SagaID)
	require.NoError(t, err)
	assert.Equal(t, StateFailed, finalSaga.CurrentState)

	// Compensation verification:
	// 1. ReleaseStock called with exact ReservationID
	require.Len(t, inv.releaseCalls, 1)
	assert.Equal(t, resID, inv.releaseCalls[0])
	assert.Empty(t, inv.commitCalls)

	// 2. CancelOrder called
	require.Len(t, ord.cancelCalls, 1)
	assert.Equal(t, orderID, ord.cancelCalls[0])

	// 3. ConfirmOrder never called
	assert.Empty(t, ord.confirmCalls)
}

func TestCoordinator_TerminalStateImmutable(t *testing.T) {
	ctx := context.Background()
	repo := newMockRepository()
	inv := &mockInventoryCommander{}
	ord := &mockOrderCommander{}
	pay := &mockPaymentCommander{}

	coord := NewCoordinator(repo, nil, inv, ord, pay, DefaultConfig(), slog.Default())

	saga, err := coord.StartSaga(ctx, SagaPayload{
		OrderID: uuid.New(),
	})
	require.NoError(t, err)

	// Execute to CONFIRMED
	err = coord.ExecuteSaga(ctx, saga.SagaID)
	require.NoError(t, err)

	// Try executing again -> ErrSagaAlreadyTerminal
	err = coord.ExecuteSaga(ctx, saga.SagaID)
	assert.ErrorIs(t, err, ErrSagaAlreadyTerminal)

	// Try compensating confirmed saga -> idempotent skip
	err = coord.Compensate(ctx, saga.SagaID, "late refund")
	assert.NoError(t, err)
}

func TestCoordinator_CompensationErrorRetry(t *testing.T) {
	ctx := context.Background()
	repo := newMockRepository()
	resID := uuid.New()
	attempts := 0
	inv := &mockInventoryCommander{
		reserveFunc: func(ctx context.Context, orderID uuid.UUID, items []ReservationItem) (*ReservationResult, error) {
			return &ReservationResult{ReservationID: resID, Success: true}, nil
		},
		releaseFunc: func(ctx context.Context, reservationID uuid.UUID, reason string) error {
			attempts++
			if attempts == 1 {
				return errors.New("transient db error")
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

	orderID := uuid.New()
	saga, err := coord.StartSaga(ctx, SagaPayload{
		OrderID: orderID,
	})
	require.NoError(t, err)

	// Execute should attempt compensation and fail on first ReleaseStock attempt
	err = coord.ExecuteSaga(ctx, saga.SagaID)
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrCompensationFailed)

	// Retry compensation
	err = coord.Compensate(ctx, saga.SagaID, "retry after transient failure")
	require.NoError(t, err)

	finalSaga, err := repo.GetSagaByID(ctx, saga.SagaID)
	require.NoError(t, err)
	assert.Equal(t, StateFailed, finalSaga.CurrentState)
}
