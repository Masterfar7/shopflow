package notification

import (
	"time"

	"github.com/google/uuid"
)

// LineItemPayload represents an itemized line within an order event.
type LineItemPayload struct {
	SKU            string `json:"sku"`
	Title          string `json:"title"`
	UnitPriceMinor int64  `json:"unit_price_minor"`
	Quantity       int    `json:"quantity"`
	SubtotalMinor  int64  `json:"subtotal_minor"`
}

// OrderConfirmedPayload represents the event payload emitted upon order confirmation.
type OrderConfirmedPayload struct {
	OrderID          uuid.UUID         `json:"order_id"`
	CustomerID       string            `json:"customer_id,omitempty"`
	CustomerEmail    string            `json:"customer_email"`
	TotalAmountMinor int64             `json:"total_amount_minor"`
	Currency         string            `json:"currency"`
	LineItems        []LineItemPayload `json:"line_items"`
	ConfirmedAt      time.Time         `json:"confirmed_at"`
}

// OrderCancelledPayload represents the event payload emitted upon order cancellation.
type OrderCancelledPayload struct {
	OrderID          uuid.UUID         `json:"order_id"`
	CustomerID       string            `json:"customer_id,omitempty"`
	CustomerEmail    string            `json:"customer_email"`
	TotalAmountMinor int64             `json:"total_amount_minor"`
	Currency         string            `json:"currency"`
	Reason           string            `json:"reason"`
	LineItems        []LineItemPayload `json:"line_items,omitempty"`
	CancelledAt      time.Time         `json:"cancelled_at"`
}
