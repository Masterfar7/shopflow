package order

import (
	"math"
	"time"

	"github.com/google/uuid"
)

// OrderStatus defines the state machine lifecycle for an Order.
type OrderStatus string

const (
	StatusPending        OrderStatus = "PENDING"
	StatusReservingStock OrderStatus = "RESERVING_STOCK"
	StatusStockReserved  OrderStatus = "STOCK_RESERVED"
	StatusPaying         OrderStatus = "PAYING"
	StatusPaid           OrderStatus = "PAID"
	StatusConfirmed      OrderStatus = "CONFIRMED"
	StatusCancelled      OrderStatus = "CANCELLED"
	StatusRefunded       OrderStatus = "REFUNDED"
)

// IsTerminal returns true if the order status is a final state that cannot be transitioned out of.
func (s OrderStatus) IsTerminal() bool {
	switch s {
	case StatusConfirmed, StatusCancelled, StatusRefunded:
		return true
	default:
		return false
	}
}

// CanTransitionTo checks if transitioning from current status to next status is permitted.
func (s OrderStatus) CanTransitionTo(next OrderStatus) bool {
	if s == next {
		return true // Idempotent no-op
	}
	if s.IsTerminal() {
		return false // Terminal State Protection: cannot transition out of terminal state
	}

	switch s {
	case StatusPending:
		return next == StatusReservingStock || next == StatusCancelled
	case StatusReservingStock:
		return next == StatusStockReserved || next == StatusCancelled
	case StatusStockReserved:
		return next == StatusPaying || next == StatusCancelled
	case StatusPaying:
		return next == StatusPaid || next == StatusCancelled
	case StatusPaid:
		return next == StatusConfirmed || next == StatusRefunded
	default:
		return false
	}
}

// IdempotencyStatus tracks the execution lifecycle of an idempotency key.
type IdempotencyStatus string

const (
	IdempotencyStatusProcessing IdempotencyStatus = "PROCESSING"
	IdempotencyStatusCompleted  IdempotencyStatus = "COMPLETED"
	IdempotencyStatusFailed     IdempotencyStatus = "FAILED"
)

// OrderItem models an immutable line item snapshot within an order.
type OrderItem struct {
	ID             uuid.UUID `json:"id"`
	OrderID        uuid.UUID `json:"order_id"`
	SKU            string    `json:"sku"`
	TitleSnapshot  string    `json:"title_snapshot"`
	UnitPriceMinor int64     `json:"unit_price_minor"`
	Quantity       int       `json:"quantity"`
	SubtotalMinor  int64     `json:"subtotal_minor"`
	CreatedAt      time.Time `json:"created_at"`
}

// Order represents the Order Aggregate Root.
type Order struct {
	ID               uuid.UUID   `json:"id"`
	UserID           uuid.UUID   `json:"user_id"`
	IdempotencyKey   string      `json:"idempotency_key"`
	Status           OrderStatus `json:"status"`
	TotalAmountMinor int64       `json:"total_amount_minor"`
	Currency         string      `json:"currency"`
	Version          int64       `json:"version"`
	Items            []OrderItem `json:"items"`
	CreatedAt        time.Time   `json:"created_at"`
	UpdatedAt        time.Time   `json:"updated_at"`
}

// CalculateTotals computes line subtotals and order total with strict int64 overflow protection.
func (o *Order) CalculateTotals() error {
	if len(o.Items) == 0 {
		return ErrEmptyOrderItems
	}
	if len(o.Items) > 100 {
		return ErrMaxItemsExceeded
	}

	var grandTotal int64 = 0
	for i := range o.Items {
		item := &o.Items[i]
		if item.Quantity <= 0 {
			return ErrInvalidQuantity
		}
		if item.UnitPriceMinor <= 0 {
			return ErrInvalidPrice
		}

		qty64 := int64(item.Quantity)
		if item.UnitPriceMinor > math.MaxInt64/qty64 {
			return ErrArithmeticOverflow
		}
		subtotal := item.UnitPriceMinor * qty64
		item.SubtotalMinor = subtotal

		if subtotal > 0 && grandTotal > math.MaxInt64-subtotal {
			return ErrArithmeticOverflow
		}
		grandTotal += subtotal
	}

	o.TotalAmountMinor = grandTotal
	return nil
}

// IdempotencyKeyRecord models a persisted row in the idempotency_keys table.
type IdempotencyKeyRecord struct {
	Key          string            `json:"key"`
	UserID       uuid.UUID         `json:"user_id"`
	RequestHash  string            `json:"request_hash"`
	Status       IdempotencyStatus `json:"status"`
	ResponseCode *int              `json:"response_code,omitempty"`
	ResponseBody []byte            `json:"response_body,omitempty"`
	OrderID      *uuid.UUID        `json:"order_id,omitempty"`
	CreatedAt    time.Time         `json:"created_at"`
	ExpiresAt    time.Time         `json:"expires_at"`
}

// Request and Response DTOs

type OrderItemRequest struct {
	SKU      string `json:"sku"`
	Quantity int    `json:"quantity"`
}

type CreateOrderRequest struct {
	Items    []OrderItemRequest `json:"items"`
	Currency string             `json:"currency"`
}

type CancelOrderRequest struct {
	Reason string `json:"reason"`
}

type CancelOrderResponse struct {
	OrderID uuid.UUID `json:"order_id"`
	Status  string    `json:"status"`
	Message string    `json:"message"`
}

type ListOrdersParams struct {
	UserID uuid.UUID
	Status *OrderStatus
	Limit  int
	Cursor string // base64-encoded "created_at,id"
}

type OrderListResponse struct {
	Items      []Order `json:"items"`
	NextCursor string  `json:"next_cursor,omitempty"`
	HasMore    bool    `json:"has_more"`
}
