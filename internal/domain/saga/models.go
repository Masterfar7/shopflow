package saga

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// SagaState represents the finite state machine states of an Order Saga.
type SagaState string

const (
	StateStarted           SagaState = "STARTED"
	StatePending           SagaState = "PENDING"
	StateReservingStock    SagaState = "RESERVING_STOCK"
	StateStockReserved     SagaState = "STOCK_RESERVED"
	StatePaying            SagaState = "PAYING"
	StatePaid              SagaState = "PAID"
	StateConfirmed         SagaState = "CONFIRMED"
	StateCompensating      SagaState = "COMPENSATING"
	StateFailed            SagaState = "FAILED"
	StateFailedCompensated SagaState = "FAILED_COMPENSATED"
)

// SagaItem represents an individual line item snapshot tracked by the saga.
type SagaItem struct {
	SKU            string `json:"sku"`
	Quantity       int    `json:"quantity"`
	UnitPriceMinor int64  `json:"unit_price_minor"`
}

// SagaPayload captures the contextual data persisted in order_sagas.payload JSONB.
type SagaPayload struct {
	OrderID          uuid.UUID  `json:"order_id"`
	CustomerID       uuid.UUID  `json:"customer_id"`
	TotalAmountMinor int64      `json:"total_amount_minor"`
	Currency         string     `json:"currency"`
	Items            []SagaItem `json:"items"`
	ReservationID    *uuid.UUID `json:"reservation_id,omitempty"`
	PaymentID        *uuid.UUID `json:"payment_id,omitempty"`
	PaymentToken     string     `json:"payment_token,omitempty"`
	FailureReason    string     `json:"failure_reason,omitempty"`
	CompensatingStep string     `json:"compensating_step,omitempty"`
}

// OrderSaga maps 1:1 to the order_sagas database table.
type OrderSaga struct {
	SagaID       uuid.UUID   `json:"saga_id"`
	OrderID      uuid.UUID   `json:"order_id"`
	CurrentState SagaState   `json:"current_state"`
	Payload      SagaPayload `json:"payload"`
	RetryCount   int         `json:"retry_count"`
	TimeoutAt    time.Time   `json:"timeout_at"`
	CreatedAt    time.Time   `json:"created_at"`
	UpdatedAt    time.Time   `json:"updated_at"`
}

// OrderSagaLog maps 1:1 to the order_saga_logs database table.
type OrderSagaLog struct {
	ID          int64     `json:"id"`
	SagaID      uuid.UUID `json:"saga_id"`
	FromState   SagaState `json:"from_state"`
	ToState     SagaState `json:"to_state"`
	EventType   string    `json:"event_type"`
	EventID     string    `json:"event_id"`
	ErrorDetail *string   `json:"error_detail,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

// Marshal serializes the SagaPayload to raw JSON bytes.
func (p *SagaPayload) Marshal() ([]byte, error) {
	return json.Marshal(p)
}

// UnmarshalPayload deserializes raw JSON bytes into a SagaPayload.
func UnmarshalPayload(data []byte) (*SagaPayload, error) {
	var p SagaPayload
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, err
	}
	return &p, nil
}
