package payment

import (
	"context"

	"github.com/google/uuid"
)

// SagaAdapter adapts payment.Service to satisfy the saga.PaymentCommander interface.
type SagaAdapter struct {
	svc *Service
}

// NewSagaAdapter constructs a new SagaAdapter for saga orchestration.
func NewSagaAdapter(svc *Service) *SagaAdapter {
	return &SagaAdapter{svc: svc}
}

// AuthorizeAndCapture routes payment authorization from the saga coordinator to the payment service.
func (a *SagaAdapter) AuthorizeAndCapture(
	ctx context.Context,
	orderID uuid.UUID,
	customerID uuid.UUID,
	amountMinor int64,
	currency string,
	paymentToken string,
	idempotencyKey string,
) (uuid.UUID, error) {
	p, err := a.svc.AuthorizeAndCapture(ctx, orderID, customerID, amountMinor, currency, paymentToken, idempotencyKey)
	if err != nil {
		if p != nil {
			return p.ID, err
		}
		return uuid.Nil, err
	}
	return p.ID, nil
}

// Refund routes refund requests from the saga coordinator to the payment service.
func (a *SagaAdapter) Refund(
	ctx context.Context,
	paymentID uuid.UUID,
	orderID uuid.UUID,
	amountMinor int64,
	reason string,
	idempotencyKey string,
) error {
	if paymentID == uuid.Nil {
		return nil
	}
	_, err := a.svc.Refund(ctx, paymentID, uuid.Nil, amountMinor, reason, idempotencyKey)
	return err
}
