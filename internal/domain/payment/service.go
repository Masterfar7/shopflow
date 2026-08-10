package payment

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Service provides domain operations and simulation logic for payments.
type Service struct {
	repo   Repository
	logger *slog.Logger
}

// NewService constructs a new payment Service.
func NewService(repo Repository, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		repo:   repo,
		logger: logger,
	}
}

// AuthorizeAndCapture simulates authorization and capture of a payment intent.
// It enforces idempotency via (user_id, idempotency_key), zero float money (int64 minor units),
// and deterministic behavior driven by test simulation triggers.
func (s *Service) AuthorizeAndCapture(
	ctx context.Context,
	orderID, userID uuid.UUID,
	amountMinor int64,
	currency, triggerOrToken, idempotencyKey string,
) (*Payment, error) {
	if amountMinor <= 0 {
		return nil, ErrInvalidPaymentAmount
	}
	if currency == "" {
		currency = "USD"
	}
	if idempotencyKey == "" {
		idempotencyKey = uuid.New().String()
	}

	// 1. Check idempotency: Return existing record if already processed
	existing, err := s.repo.GetPaymentByIdempotency(ctx, userID, idempotencyKey)
	if err == nil && existing != nil {
		s.logger.InfoContext(ctx, "idempotent payment replay detected",
			"payment_id", existing.ID,
			"order_id", orderID,
			"user_id", userID,
			"idempotency_key", idempotencyKey,
			"status", existing.Status,
		)

		// Validate payload consistency
		if existing.OrderID != orderID || existing.AmountMinor != amountMinor {
			return nil, ErrIdempotencyConflict
		}

		if existing.Status == PaymentStatusSuccess || existing.Status == PaymentStatusRefunded {
			return existing, nil
		}
		if existing.Status == PaymentStatusFailed {
			return existing, ErrPaymentDeclined
		}
		return existing, nil
	}

	// 2. Evaluate simulation trigger
	tokenLower := strings.ToLower(strings.TrimSpace(triggerOrToken))
	isDecline := tokenLower == "fail" ||
		tokenLower == "decline" ||
		tokenLower == "force_failure" ||
		tokenLower == "sim_tok_decline" ||
		tokenLower == "decline_insufficient_funds" ||
		tokenLower == "decline_fraud" ||
		tokenLower == "insufficient_funds" ||
		tokenLower == "fraud"

	isTimeout := tokenLower == "timeout" ||
		tokenLower == "timeout_latency"

	isNetworkError := tokenLower == "network_error"

	paymentID := uuid.New()
	now := time.Now().UTC()

	var status PaymentStatus
	var errorCode *string
	var failureReason *string
	var returnErr error

	if isDecline {
		status = PaymentStatusFailed
		code := "PAYMENT_DECLINED"
		reason := "Payment was declined by the simulated card issuer"
		errorCode = &code
		failureReason = &reason
		returnErr = ErrPaymentDeclined
	} else if isTimeout {
		status = PaymentStatusFailed
		code := "GATEWAY_TIMEOUT"
		reason := "Simulated payment gateway timeout"
		errorCode = &code
		failureReason = &reason
		returnErr = ErrPaymentTimeout
	} else if isNetworkError {
		status = PaymentStatusFailed
		code := "NETWORK_ERROR"
		reason := "Simulated network failure communicating with gateway"
		errorCode = &code
		failureReason = &reason
		returnErr = ErrPaymentDeclined
	} else {
		// Successful payment
		status = PaymentStatusSuccess
	}

	p := &Payment{
		ID:             paymentID,
		OrderID:        orderID,
		UserID:         userID,
		IdempotencyKey: idempotencyKey,
		AmountMinor:    amountMinor,
		Currency:       currency,
		Provider:       "SIMULATED",
		Status:         status,
		ErrorCode:      errorCode,
		FailureReason:  failureReason,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	if saveErr := s.repo.CreatePayment(ctx, p); saveErr != nil {
		if errors.Is(saveErr, ErrIdempotencyConflict) {
			existing, fetchErr := s.repo.GetPaymentByIdempotency(ctx, userID, idempotencyKey)
			if fetchErr == nil && existing != nil {
				if existing.OrderID != orderID || existing.AmountMinor != amountMinor {
					return nil, ErrIdempotencyConflict
				}
				if existing.Status == PaymentStatusSuccess || existing.Status == PaymentStatusRefunded {
					return existing, nil
				}
				if existing.Status == PaymentStatusFailed {
					return existing, ErrPaymentDeclined
				}
				return existing, nil
			}
		}
		s.logger.ErrorContext(ctx, "failed to persist payment record", "error", saveErr)
		return nil, saveErr
	}

	s.logger.InfoContext(ctx, "payment intent processed",
		"payment_id", p.ID,
		"order_id", p.OrderID,
		"status", p.Status,
		"amount_minor", p.AmountMinor,
	)

	return p, returnErr
}

// Refund executes a refund for a previously captured payment.
func (s *Service) Refund(
	ctx context.Context,
	paymentID, userID uuid.UUID,
	amountMinor int64,
	reason, idempotencyKey string,
) (*Refund, error) {
	if amountMinor <= 0 {
		return nil, ErrInvalidRefundAmount
	}

	payment, err := s.repo.GetPaymentByID(ctx, paymentID)
	if err != nil {
		return nil, ErrPaymentNotFound
	}

	// Terminal state and status guards
	if payment.Status == PaymentStatusRefunded {
		return nil, ErrPaymentAlreadyRefunded
	}
	if payment.Status != PaymentStatusSuccess {
		return nil, ErrPaymentCannotBeRefunded
	}
	if amountMinor > payment.AmountMinor {
		return nil, fmt.Errorf("%w: refund amount %d exceeds captured amount %d", ErrInvalidRefundAmount, amountMinor, payment.AmountMinor)
	}

	if err := s.repo.TransitionPaymentStatus(ctx, paymentID, PaymentStatusSuccess, PaymentStatusRefunded); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	refund := &Refund{
		ID:          uuid.New(),
		PaymentID:   paymentID,
		OrderID:     payment.OrderID,
		AmountMinor: amountMinor,
		Reason:      reason,
		Status:      RefundStatusSuccess,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	if err := s.repo.CreateRefund(ctx, refund); err != nil {
		return nil, fmt.Errorf("failed to save refund record: %w", err)
	}

	s.logger.InfoContext(ctx, "payment refund executed successfully",
		"refund_id", refund.ID,
		"payment_id", paymentID,
		"amount_minor", amountMinor,
	)

	return refund, nil
}

// GetPaymentByID retrieves a payment by its unique ID.
func (s *Service) GetPaymentByID(ctx context.Context, id uuid.UUID) (*Payment, error) {
	return s.repo.GetPaymentByID(ctx, id)
}
