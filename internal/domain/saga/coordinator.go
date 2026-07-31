package saga

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"shopflow/internal/platform/database"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Config configures timeout intervals and behavior for the Saga Coordinator.
type Config struct {
	StockTimeout             time.Duration
	PaymentTimeout           time.Duration
	ConfirmTimeout           time.Duration
	CompensatedTerminalState SagaState
}

// DefaultConfig returns production defaults for Config.
func DefaultConfig() Config {
	return Config{
		StockTimeout:             30 * time.Second,
		PaymentTimeout:           60 * time.Second,
		ConfirmTimeout:           30 * time.Second,
		CompensatedTerminalState: StateFailed,
	}
}

// Coordinator drives order saga execution, FSM transitions, and compensation workflows.
type Coordinator struct {
	repo      Repository
	pool      *pgxpool.Pool
	inventory InventoryCommander
	orders    OrderCommander
	payments  PaymentCommander
	cfg       Config
	logger    *slog.Logger
}

// SagaCoordinator is a type alias for Coordinator.
type SagaCoordinator = Coordinator

// NewCoordinator creates a new Coordinator instance.
func NewCoordinator(
	repo Repository,
	pool *pgxpool.Pool,
	inventory InventoryCommander,
	orders OrderCommander,
	payments PaymentCommander,
	cfg Config,
	logger *slog.Logger,
) *Coordinator {
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.StockTimeout <= 0 {
		cfg.StockTimeout = 30 * time.Second
	}
	if cfg.PaymentTimeout <= 0 {
		cfg.PaymentTimeout = 60 * time.Second
	}
	if cfg.ConfirmTimeout <= 0 {
		cfg.ConfirmTimeout = 30 * time.Second
	}
	if cfg.CompensatedTerminalState == "" {
		cfg.CompensatedTerminalState = StateFailed
	}

	return &Coordinator{
		repo:      repo,
		pool:      pool,
		inventory: inventory,
		orders:    orders,
		payments:  payments,
		cfg:       cfg,
		logger:    logger,
	}
}

// NewSagaCoordinator is an alias constructor for NewCoordinator.
func NewSagaCoordinator(
	repo Repository,
	pool *pgxpool.Pool,
	inventory InventoryCommander,
	orders OrderCommander,
	payments PaymentCommander,
	cfg Config,
	logger *slog.Logger,
) *Coordinator {
	return NewCoordinator(repo, pool, inventory, orders, payments, cfg, logger)
}

// StartSaga initializes a persistent saga for a newly created order.
func (c *Coordinator) StartSaga(ctx context.Context, payload SagaPayload) (*OrderSaga, error) {
	saga := &OrderSaga{
		SagaID:       uuid.New(),
		OrderID:      payload.OrderID,
		CurrentState: StateStarted,
		Payload:      payload,
		RetryCount:   0,
		TimeoutAt:    time.Now().Add(c.cfg.StockTimeout),
	}

	if err := c.repo.CreateSaga(ctx, saga); err != nil {
		return nil, fmt.Errorf("start saga: %w", err)
	}

	return saga, nil
}

// ExecuteSaga runs the full synchronous orchestration pipeline.
func (c *Coordinator) ExecuteSaga(ctx context.Context, sagaID uuid.UUID) error {
	saga, err := c.repo.GetSagaByID(ctx, sagaID)
	if err != nil {
		return fmt.Errorf("get saga for execution: %w", err)
	}

	if IsTerminal(saga.CurrentState) {
		return ErrSagaAlreadyTerminal
	}

	if saga.CurrentState != StateStarted && saga.CurrentState != StatePending {
		if saga.CurrentState == StateReservingStock {
			return fmt.Errorf("saga %s is currently reserving stock by another process", sagaID)
		}
		return fmt.Errorf("saga %s is in state %s and cannot be executed directly", sagaID, saga.CurrentState)
	}

	// Step 1: Transition to RESERVING_STOCK if currently STARTED or PENDING
	if saga.CurrentState == StateStarted || saga.CurrentState == StatePending {
		if err := c.transitionAndLog(ctx, sagaID, saga.CurrentState, StateReservingStock, "StockReservationStarted", nil, c.cfg.StockTimeout, nil); err != nil {
			return err
		}
	}

	// Refresh saga state
	saga, err = c.repo.GetSagaByID(ctx, sagaID)
	if err != nil {
		return err
	}

	// Prepare inventory reservation items
	items := make([]ReservationItem, len(saga.Payload.Items))
	for i, it := range saga.Payload.Items {
		items[i] = ReservationItem{SKU: it.SKU, Quantity: it.Quantity}
	}

	// Call Inventory Commander
	resResult, err := c.inventory.ReserveStock(ctx, saga.OrderID, items)
	if err != nil || (resResult != nil && !resResult.Success) {
		reason := "insufficient_stock"
		if err != nil {
			reason = err.Error()
		} else if resResult != nil && resResult.ErrorMessage != "" {
			reason = resResult.ErrorMessage
		}
		c.logger.WarnContext(ctx, "stock reservation failed", "saga_id", sagaID, "reason", reason)

		// Compensation for Stock Failure: Transition directly to FAILED, cancel order.
		// No stock was reserved, so no inventory release required.
		return c.handleStockReservationFailure(ctx, sagaID, reason)
	}

	// Stock reservation succeeded: Record reservation_id
	saga.Payload.ReservationID = &resResult.ReservationID
	if err := c.transitionAndLog(ctx, sagaID, StateReservingStock, StateStockReserved, "StockReserved", nil, c.cfg.PaymentTimeout, &saga.Payload); err != nil {
		if relErr := c.inventory.ReleaseStock(ctx, resResult.ReservationID, "transition_failed"); relErr != nil {
			c.logger.ErrorContext(ctx, "failed to release stock after transition error", "reservation_id", resResult.ReservationID, "error", relErr)
		}
		return err
	}

	// Step 2: Transition to PAYING
	if err := c.transitionAndLog(ctx, sagaID, StateStockReserved, StatePaying, "PaymentInitiated", nil, c.cfg.PaymentTimeout, nil); err != nil {
		return err
	}

	// Call Payment Commander
	paymentID, err := c.payments.AuthorizeAndCapture(
		ctx,
		saga.OrderID,
		saga.Payload.CustomerID,
		saga.Payload.TotalAmountMinor,
		saga.Payload.Currency,
		saga.Payload.PaymentToken,
		saga.OrderID.String(), // Idempotency key = orderID
	)
	if err != nil {
		c.logger.WarnContext(ctx, "payment capture failed, initiating compensation", "saga_id", sagaID, "error", err)
		return c.Compensate(ctx, sagaID, fmt.Sprintf("payment_failed: %s", err.Error()))
	}

	saga.Payload.PaymentID = &paymentID
	if err := c.transitionAndLog(ctx, sagaID, StatePaying, StatePaid, "PaymentCaptured", nil, c.cfg.ConfirmTimeout, &saga.Payload); err != nil {
		return err
	}

	// Step 3: Commit Inventory and Confirm Order
	var commitErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt*10) * time.Millisecond)
		}
		commitErr = c.inventory.CommitStock(ctx, *saga.Payload.ReservationID)
		if commitErr == nil {
			break
		}
	}
	if commitErr != nil {
		c.logger.ErrorContext(ctx, "commit stock failed after payment", "saga_id", sagaID, "error", commitErr)
		return fmt.Errorf("commit stock failed after payment: %w", commitErr)
	}

	if err := c.orders.ConfirmOrder(ctx, saga.OrderID); err != nil {
		c.logger.ErrorContext(ctx, "order confirmation failed", "saga_id", sagaID, "error", err)
		return err
	}

	// Transition to terminal CONFIRMED
	return c.transitionAndLog(ctx, sagaID, StatePaid, StateConfirmed, "OrderConfirmed", nil, 0, nil)
}

// Compensate implements FEAT-SGA-03: Multi-step compensation workflow.
// On Payment Failure or Timeout:
// 1. Transition to COMPENSATING
// 2. Call Inventory ReleaseStock
// 3. Mark Order CANCELLED
// 4. Transition to terminal state (FAILED or FAILED_COMPENSATED)
func (c *Coordinator) Compensate(ctx context.Context, sagaID uuid.UUID, reason string) error {
	saga, err := c.repo.GetSagaByID(ctx, sagaID)
	if err != nil {
		return err
	}

	if IsTerminal(saga.CurrentState) {
		c.logger.InfoContext(ctx, "saga already in terminal state, skipping compensation", "saga_id", sagaID, "state", saga.CurrentState)
		return nil
	}

	// 1. Transition to COMPENSATING if not already there
	if saga.CurrentState != StateCompensating {
		errDetail := reason
		saga.Payload.FailureReason = reason
		saga.Payload.CompensatingStep = "releasing_stock"

		if err := c.transitionAndLog(ctx, sagaID, saga.CurrentState, StateCompensating, "CompensationStarted", &errDetail, 60*time.Second, &saga.Payload); err != nil {
			return fmt.Errorf("transition to compensating: %w", err)
		}
	}

	// 2. Release reserved stock if reservation exists
	if saga.Payload.ReservationID != nil {
		if relErr := c.inventory.ReleaseStock(ctx, *saga.Payload.ReservationID, reason); relErr != nil {
			c.logger.ErrorContext(ctx, "inventory release stock failed during compensation", "saga_id", sagaID, "reservation_id", *saga.Payload.ReservationID, "error", relErr)
			return fmt.Errorf("%w: inventory release failed: %v", ErrCompensationFailed, relErr)
		}
		c.logger.InfoContext(ctx, "inventory reservation released", "saga_id", sagaID, "reservation_id", *saga.Payload.ReservationID)
	}

	// Refund payment if payment was captured
	if saga.Payload.PaymentID != nil && c.payments != nil {
		if refundErr := c.payments.Refund(ctx, *saga.Payload.PaymentID, saga.OrderID, saga.Payload.TotalAmountMinor, reason, saga.OrderID.String()); refundErr != nil {
			c.logger.ErrorContext(ctx, "payment refund failed during compensation", "saga_id", sagaID, "payment_id", *saga.Payload.PaymentID, "error", refundErr)
			return fmt.Errorf("%w: payment refund failed: %v", ErrCompensationFailed, refundErr)
		}
		c.logger.InfoContext(ctx, "payment refunded", "saga_id", sagaID, "payment_id", *saga.Payload.PaymentID)
	}

	// 3. Mark Order CANCELLED
	if cancelErr := c.orders.CancelOrder(ctx, saga.OrderID, reason); cancelErr != nil {
		c.logger.ErrorContext(ctx, "order cancellation failed during compensation", "saga_id", sagaID, "order_id", saga.OrderID, "error", cancelErr)
		return fmt.Errorf("%w: order cancellation failed: %v", ErrCompensationFailed, cancelErr)
	}

	// 4. Transition to terminal state FAILED (or FAILED_COMPENSATED)
	compDoneDetail := "compensation completed successfully"
	targetState := c.cfg.CompensatedTerminalState
	if targetState == "" {
		targetState = StateFailed
	}
	return c.transitionAndLog(ctx, sagaID, StateCompensating, targetState, "CompensationCompleted", &compDoneDetail, 0, nil)
}

// handleStockReservationFailure handles failure when stock reservation cannot be fulfilled.
func (c *Coordinator) handleStockReservationFailure(ctx context.Context, sagaID uuid.UUID, reason string) error {
	saga, err := c.repo.GetSagaByID(ctx, sagaID)
	if err != nil {
		return err
	}

	if IsTerminal(saga.CurrentState) {
		return nil
	}

	// Cancel Order
	if err := c.orders.CancelOrder(ctx, saga.OrderID, reason); err != nil {
		c.logger.ErrorContext(ctx, "cancel order failed on stock reservation failure", "order_id", saga.OrderID, "error", err)
	}

	errDetail := reason
	saga.Payload.FailureReason = reason
	if err := c.transitionAndLog(ctx, sagaID, saga.CurrentState, StateFailed, "StockReservationFailed", &errDetail, 0, &saga.Payload); err != nil {
		return err
	}
	return fmt.Errorf("stock reservation failed: %s", reason)
}

func (c *Coordinator) transitionAndLog(
	ctx context.Context,
	sagaID uuid.UUID,
	from, to SagaState,
	eventType string,
	errDetail *string,
	timeout time.Duration,
	updatedPayload *SagaPayload,
) error {
	// If pool is provided, run atomically in a database transaction
	if c.pool != nil {
		return database.WithTx(ctx, c.pool, func(tx pgx.Tx) error {
			saga, err := c.repo.GetSagaForUpdate(ctx, tx, sagaID)
			if err != nil {
				return err
			}

			payload := saga.Payload
			if updatedPayload != nil {
				payload = *updatedPayload
			}

			var nextTimeout time.Time
			if timeout > 0 {
				nextTimeout = time.Now().Add(timeout)
			}

			if err := c.repo.TransitionState(ctx, tx, sagaID, from, to, payload, nextTimeout); err != nil {
				return err
			}

			log := &OrderSagaLog{
				SagaID:      sagaID,
				FromState:   from,
				ToState:     to,
				EventType:   eventType,
				EventID:     uuid.New().String(),
				ErrorDetail: errDetail,
			}
			return c.repo.AppendLog(ctx, tx, log)
		})
	}

	// Fallback path when pool is nil (e.g. mock repositories in unit tests)
	saga, err := c.repo.GetSagaByID(ctx, sagaID)
	if err != nil {
		return err
	}

	payload := saga.Payload
	if updatedPayload != nil {
		payload = *updatedPayload
	}

	var nextTimeout time.Time
	if timeout > 0 {
		nextTimeout = time.Now().Add(timeout)
	}

	if err := c.repo.TransitionState(ctx, nil, sagaID, from, to, payload, nextTimeout); err != nil {
		return err
	}

	log := &OrderSagaLog{
		SagaID:      sagaID,
		FromState:   from,
		ToState:     to,
		EventType:   eventType,
		EventID:     uuid.New().String(),
		ErrorDetail: errDetail,
	}
	return c.repo.AppendLog(ctx, nil, log)
}
