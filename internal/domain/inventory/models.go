package inventory

import (
	"time"

	"github.com/google/uuid"
)

type ReservationStatus string

const (
	ReservationStatusPending   ReservationStatus = "PENDING"
	ReservationStatusCommitted ReservationStatus = "COMMITTED"
	ReservationStatusReleased  ReservationStatus = "RELEASED"
)

// Item represents the physical stock levels for a given SKU.
type Item struct {
	SKU       string    `json:"sku"`
	OnHand    int       `json:"on_hand"`
	Reserved  int       `json:"reserved"`
	Available int       `json:"available"` // Computed: on_hand - reserved
	Version   int64     `json:"version"`
	UpdatedAt time.Time `json:"updated_at"`
}

// StockItemRequest specifies a single SKU and quantity to reserve.
type StockItemRequest struct {
	SKU      string `json:"sku"`
	Quantity int    `json:"quantity"`
}

// ReserveStockRequest specifies the full multi-SKU reservation payload.
type ReserveStockRequest struct {
	ReservationID uuid.UUID          `json:"reservation_id"`
	OrderID       uuid.UUID          `json:"order_id"`
	Items         []StockItemRequest `json:"items"`
	TTLSeconds    int                `json:"ttl_seconds"` // Default 900s (15m)
}

// ReserveStockResult represents the outcome of a successful stock reservation.
type ReserveStockResult struct {
	ReservationID uuid.UUID          `json:"reservation_id"`
	OrderID       uuid.UUID          `json:"order_id"`
	Status        ReservationStatus  `json:"status"`
	ExpiresAt     time.Time          `json:"expires_at"`
	Items         []StockItemRequest `json:"items"`
}

// ReservationItem represents a single SKU line item in a reservation record.
type ReservationItem struct {
	ID            uuid.UUID `json:"id"`
	ReservationID uuid.UUID `json:"reservation_id"`
	SKU           string    `json:"sku"`
	Quantity      int       `json:"quantity"`
	CreatedAt     time.Time `json:"created_at"`
}

// Reservation represents an aggregate stock reservation record.
type Reservation struct {
	ID        uuid.UUID         `json:"id"`
	OrderID   uuid.UUID         `json:"order_id"`
	Status    ReservationStatus `json:"status"`
	ExpiresAt time.Time         `json:"expires_at"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
	Items     []ReservationItem `json:"items,omitempty"`
}

// Type aliases for naming compatibility.
type StockReservation = Reservation
type StockReservationItem = ReservationItem

// ReplenishStockRequest represents request body for restocking/replenishment.
type ReplenishStockRequest struct {
	Quantity    int    `json:"quantity"`
	ReferenceID string `json:"reference_id,omitempty"`
}

// ReleaseStockRequest represents optional request body for releasing stock.
type ReleaseStockRequest struct {
	Reason string `json:"reason,omitempty"`
}

// StatusResponse represents standard response for status mutation endpoints.
type StatusResponse struct {
	Status ReservationStatus `json:"status"`
}
