package payment

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// PaymentStatus represents the lifecycle state of a payment.
type PaymentStatus string

const (
	PaymentStatusInitiated  PaymentStatus = "INITIATED"
	PaymentStatusProcessing PaymentStatus = "PROCESSING"
	PaymentStatusSuccess    PaymentStatus = "SUCCESS"
	PaymentStatusFailed     PaymentStatus = "FAILED"
	PaymentStatusRefunded   PaymentStatus = "REFUNDED"
)

// RefundStatus represents the state of a refund operation.
type RefundStatus string

const (
	RefundStatusInitiated RefundStatus = "INITIATED"
	RefundStatusSuccess   RefundStatus = "SUCCESS"
	RefundStatusFailed    RefundStatus = "FAILED"
)

// Payment represents a payment record in the payments table.
// Invariant: amount_minor is strictly an int64 integer in minor currency units (zero floats).
type Payment struct {
	ID             uuid.UUID     `json:"id"`
	OrderID        uuid.UUID     `json:"order_id"`
	UserID         uuid.UUID     `json:"user_id"`
	IdempotencyKey string        `json:"idempotency_key"`
	AmountMinor    int64         `json:"amount_minor"`
	Currency       string        `json:"currency"`
	Provider       string        `json:"provider"`
	Status         PaymentStatus `json:"status"`
	ErrorCode      *string       `json:"error_code,omitempty"`
	FailureReason  *string       `json:"failure_reason,omitempty"`
	CreatedAt      time.Time     `json:"created_at"`
	UpdatedAt      time.Time     `json:"updated_at"`
}

// Refund represents a payment refund record in the payment_refunds table.
// Invariant: amount_minor is strictly an int64 integer in minor currency units (zero floats).
type Refund struct {
	ID          uuid.UUID    `json:"id"`
	PaymentID   uuid.UUID    `json:"payment_id"`
	OrderID     uuid.UUID    `json:"order_id"`
	AmountMinor int64        `json:"amount_minor"`
	Reason      string       `json:"reason"`
	Status      RefundStatus `json:"status"`
	ErrorCode   *string      `json:"error_code,omitempty"`
	CreatedAt   time.Time    `json:"created_at"`
	UpdatedAt   time.Time    `json:"updated_at"`
}

// SimulatePaymentRequest represents an HTTP request to simulate payment authorization/capture.
// Flexible unmarshaling supports both direct minor units and nested OpenAPI Money objects.
type SimulatePaymentRequest struct {
	OrderID     uuid.UUID `json:"order_id"`
	AmountMinor int64     `json:"amount_minor"`
	Currency    string    `json:"currency"`
	Trigger     string    `json:"trigger"` // e.g. FORCE_SUCCESS, FORCE_FAILURE, fail, decline, timeout
}

type rawMoney struct {
	Amount   int64  `json:"amount"`
	Currency string `json:"currency"`
}

// UnmarshalJSON supports both flat (AmountMinor) and nested (Amount Money) schemas.
func (r *SimulatePaymentRequest) UnmarshalJSON(data []byte) error {
	var aux struct {
		OrderID     *uuid.UUID `json:"order_id"`
		AmountMinor *int64     `json:"amount_minor"`
		Currency    string     `json:"currency"`
		Trigger     string     `json:"trigger"`
		Scenario    string     `json:"scenario"`
		Amount      *rawMoney  `json:"amount"`
	}

	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}

	if aux.OrderID != nil {
		r.OrderID = *aux.OrderID
	}

	if aux.AmountMinor != nil {
		r.AmountMinor = *aux.AmountMinor
	} else if aux.Amount != nil {
		r.AmountMinor = aux.Amount.Amount
		if aux.Amount.Currency != "" {
			r.Currency = aux.Amount.Currency
		}
	}

	if aux.Currency != "" {
		r.Currency = aux.Currency
	}
	if r.Currency == "" {
		r.Currency = "USD"
	}

	if aux.Trigger != "" {
		r.Trigger = aux.Trigger
	} else if aux.Scenario != "" {
		r.Trigger = aux.Scenario
	}

	return nil
}

// PaymentResponse represents the response payload for a payment operation.
type PaymentResponse struct {
	ID             uuid.UUID     `json:"id"`
	OrderID        uuid.UUID     `json:"order_id"`
	UserID         uuid.UUID     `json:"user_id"`
	IdempotencyKey string        `json:"idempotency_key"`
	AmountMinor    int64         `json:"amount_minor"`
	Currency       string        `json:"currency"`
	Provider       string        `json:"provider"`
	Status         PaymentStatus `json:"status"`
	ErrorCode      *string       `json:"error_code,omitempty"`
	FailureReason  *string       `json:"failure_reason,omitempty"`
	CreatedAt      time.Time     `json:"created_at"`
	UpdatedAt      time.Time     `json:"updated_at"`
}

// RefundPaymentRequest represents an HTTP request to refund a captured payment.
type RefundPaymentRequest struct {
	AmountMinor int64  `json:"amount_minor"`
	Reason      string `json:"reason"`
}

// UnmarshalJSON supports both flat and nested amount specifications.
func (r *RefundPaymentRequest) UnmarshalJSON(data []byte) error {
	var aux struct {
		AmountMinor *int64    `json:"amount_minor"`
		Amount      *rawMoney `json:"amount"`
		Reason      string    `json:"reason"`
	}

	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}

	if aux.AmountMinor != nil {
		r.AmountMinor = *aux.AmountMinor
	} else if aux.Amount != nil {
		r.AmountMinor = aux.Amount.Amount
	}

	r.Reason = aux.Reason
	return nil
}

// RefundResponse represents the response payload for a refund operation.
type RefundResponse struct {
	ID          uuid.UUID    `json:"id"`
	PaymentID   uuid.UUID    `json:"payment_id"`
	OrderID     uuid.UUID    `json:"order_id"`
	AmountMinor int64        `json:"amount_minor"`
	Reason      string       `json:"reason"`
	Status      RefundStatus `json:"status"`
	CreatedAt   time.Time    `json:"created_at"`
	UpdatedAt   time.Time    `json:"updated_at"`
}

// ToResponse converts Payment to PaymentResponse.
func (p *Payment) ToResponse() PaymentResponse {
	return PaymentResponse{
		ID:             p.ID,
		OrderID:        p.OrderID,
		UserID:         p.UserID,
		IdempotencyKey: p.IdempotencyKey,
		AmountMinor:    p.AmountMinor,
		Currency:       p.Currency,
		Provider:       p.Provider,
		Status:         p.Status,
		ErrorCode:      p.ErrorCode,
		FailureReason:  p.FailureReason,
		CreatedAt:      p.CreatedAt,
		UpdatedAt:      p.UpdatedAt,
	}
}

// ToResponse converts Refund to RefundResponse.
func (r *Refund) ToResponse() RefundResponse {
	return RefundResponse{
		ID:          r.ID,
		PaymentID:   r.PaymentID,
		OrderID:     r.OrderID,
		AmountMinor: r.AmountMinor,
		Reason:      r.Reason,
		Status:      r.Status,
		CreatedAt:   r.CreatedAt,
		UpdatedAt:   r.UpdatedAt,
	}
}

// Validate ensures payment invariants hold.
func (p *Payment) Validate() error {
	if p.ID == uuid.Nil {
		return fmt.Errorf("payment id cannot be nil")
	}
	if p.OrderID == uuid.Nil {
		return fmt.Errorf("order id cannot be nil")
	}
	if p.UserID == uuid.Nil {
		return fmt.Errorf("user id cannot be nil")
	}
	if p.AmountMinor <= 0 {
		return fmt.Errorf("amount minor must be strictly positive, got %d", p.AmountMinor)
	}
	if p.Currency == "" {
		return fmt.Errorf("currency cannot be empty")
	}
	return nil
}
