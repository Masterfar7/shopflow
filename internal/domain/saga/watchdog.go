package saga

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// WatchdogConfig holds configuration parameters for the saga timeout watchdog.
type WatchdogConfig struct {
	PollInterval time.Duration
	BatchSize    int
}

// DefaultWatchdogConfig returns production defaults for WatchdogConfig.
func DefaultWatchdogConfig() WatchdogConfig {
	return WatchdogConfig{
		PollInterval: 5 * time.Second,
		BatchSize:    20,
	}
}

// Watchdog periodically polls for timed-out sagas and triggers automated failure/compensation.
type Watchdog struct {
	coordinator *Coordinator
	repo        Repository
	cfg         WatchdogConfig
	logger      *slog.Logger

	cancel context.CancelFunc
	wg     sync.WaitGroup
	mu     sync.Mutex
	active bool
}

// NewWatchdog creates a new Watchdog instance.
func NewWatchdog(coordinator *Coordinator, repo Repository, cfg WatchdogConfig, logger *slog.Logger) *Watchdog {
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 5 * time.Second
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 20
	}

	return &Watchdog{
		coordinator: coordinator,
		repo:        repo,
		cfg:         cfg,
		logger:      logger,
	}
}

// Start launches the background watchdog loop.
func (w *Watchdog) Start(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.active {
		return fmt.Errorf("saga watchdog is already running")
	}

	runCtx, cancel := context.WithCancel(ctx)
	w.cancel = cancel
	w.active = true

	w.wg.Add(1)
	go w.run(runCtx)

	w.logger.InfoContext(ctx, "saga timeout watchdog started", "poll_interval", w.cfg.PollInterval)
	return nil
}

// Stop gracefully signals the watchdog loop to terminate and waits for completion.
func (w *Watchdog) Stop(ctx context.Context) error {
	w.mu.Lock()
	if !w.active {
		w.mu.Unlock()
		return nil
	}
	w.cancel()
	w.active = false
	w.mu.Unlock()

	done := make(chan struct{})
	go func() {
		w.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		w.logger.InfoContext(ctx, "saga timeout watchdog stopped cleanly")
		return nil
	case <-ctx.Done():
		return fmt.Errorf("saga watchdog stop timed out: %w", ctx.Err())
	}
}

// IsActive reports whether the watchdog loop is currently running.
func (w *Watchdog) IsActive() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.active
}

func (w *Watchdog) run(ctx context.Context) {
	defer w.wg.Done()
	ticker := time.NewTicker(w.cfg.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.ProcessExpiredSagas(ctx)
		}
	}
}

// ProcessExpiredSagas scans and processes all currently expired sagas.
func (w *Watchdog) ProcessExpiredSagas(ctx context.Context) {
	sagas, err := w.repo.FindTimedOutSagas(ctx, w.cfg.BatchSize)
	if err != nil {
		w.logger.ErrorContext(ctx, "failed to query timed out sagas", "error", err)
		return
	}

	for _, s := range sagas {
		if ctx.Err() != nil {
			return
		}

		w.logger.WarnContext(ctx, "handling timed out saga", "saga_id", s.SagaID, "state", s.CurrentState)

		switch s.CurrentState {
		case StateStarted, StatePending:
			// Timed out before starting stock reservation
			_ = w.coordinator.handleStockReservationFailure(ctx, s.SagaID, "timeout_in_started_state")

		case StateReservingStock:
			// Timed out waiting for stock reservation
			_ = w.coordinator.handleStockReservationFailure(ctx, s.SagaID, "timeout_waiting_for_stock")

		case StateStockReserved:
			// Timed out waiting for payment -> Trigger compensation
			_ = w.coordinator.Compensate(ctx, s.SagaID, "timeout_waiting_for_payment")

		case StatePaying:
			// Timed out waiting for payment -> Trigger compensation
			_ = w.coordinator.Compensate(ctx, s.SagaID, "timeout_waiting_for_payment")

		case StatePaid:
			// Timed out after payment captured -> Trigger compensation
			_ = w.coordinator.Compensate(ctx, s.SagaID, "timeout_after_payment_captured")

		case StateCompensating:
			// Retry compensation if prior attempt failed
			_ = w.coordinator.Compensate(ctx, s.SagaID, "retry_compensation_timeout")

		default:
			w.logger.WarnContext(ctx, "unexpected state for timed out saga", "saga_id", s.SagaID, "state", s.CurrentState)
		}
	}
}
