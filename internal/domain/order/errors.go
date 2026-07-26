package order

import "errors"

var (
	ErrOrderNotFound            = errors.New("order not found")
	ErrUnauthorized             = errors.New("unauthorized: missing or invalid user identification")
	ErrForbidden                = errors.New("forbidden: customer does not own this order")
	ErrMissingIdempotencyKey    = errors.New("missing idempotency-key header")
	ErrIdempotencyConflict      = errors.New("idempotency key conflict: payload mismatch")
	ErrConcurrentProcessing     = errors.New("concurrent request with same idempotency key in progress")
	ErrIdempotencyKeyProcessing = ErrConcurrentProcessing
	ErrEmptyOrderItems          = errors.New("order must contain at least one line item")
	ErrEmptyOrder               = ErrEmptyOrderItems
	ErrMaxItemsExceeded         = errors.New("order exceeds maximum allowed line items (100)")
	ErrInvalidQuantity          = errors.New("quantity must be greater than zero")
	ErrInvalidPrice             = errors.New("price must be greater than zero")
	ErrCurrencyMismatch         = errors.New("currency mismatch between order and product catalog")
	ErrProductNotFound          = errors.New("product SKU not found in catalog")
	ErrProductUnavailable       = errors.New("product SKU is inactive or unavailable")
	ErrArithmeticOverflow       = errors.New("monetary arithmetic integer overflow")
	ErrOptimisticLockConflict   = errors.New("optimistic lock conflict: order modified concurrently")
	ErrInvalidStateTransition   = errors.New("invalid order state transition")
	ErrOrderTerminal            = errors.New("order is in a terminal state and cannot be modified")
	ErrOrderAlreadyTerminal     = ErrOrderTerminal
	ErrEmptyCartCheckout        = errors.New("cannot checkout an empty cart")
)
