package inventory

import (
	"errors"
	"fmt"
	"strings"
)

var (
	ErrSKUNotFound                 = errors.New("sku not found in inventory")
	ErrInvalidQuantity             = errors.New("quantity must be greater than zero")
	ErrEmptySKU                    = errors.New("sku cannot be empty")
	ErrEmptyItems                  = errors.New("reservation request must contain at least one item")
	ErrInvalidOrderID              = errors.New("order_id must be a valid non-empty UUID")
	ErrReservationNotFound         = errors.New("reservation not found")
	ErrReservationAlreadyCommitted = errors.New("reservation has already been committed")
	ErrReservationAlreadyReleased  = errors.New("reservation has already been released")
	ErrReservationAlreadyProcessed = errors.New("reservation has already been processed")
	ErrReservationExpired          = errors.New("reservation has expired")
	ErrDuplicateReservationID      = errors.New("reservation with this id or order_id already exists")
	ErrStockConservationViolation  = errors.New("stock conservation invariant violation")
	ErrDeadlockAvoidanceViolation  = errors.New("deadlock avoidance violation: skus not sorted ascending")
)

type StockShortage struct {
	Requested int `json:"requested"`
	Available int `json:"available"`
	OnHand    int `json:"on_hand"`
	Reserved  int `json:"reserved"`
}

// ErrInsufficientStock is a rich domain error detailing which SKUs failed reservation.
type ErrInsufficientStock struct {
	FailedSKUs []string                 `json:"failed_skus"`
	Details    map[string]StockShortage `json:"details"`
}

func (e *ErrInsufficientStock) Error() string {
	return fmt.Sprintf("insufficient stock for skus: [%s]", strings.Join(e.FailedSKUs, ", "))
}
