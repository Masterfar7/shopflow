// internal/domain/cart/errors.go
package cart

import "errors"

var (
	ErrCartNotFound           = errors.New("cart not found")
	ErrCartNotActive          = errors.New("cart is not active")
	ErrItemNotFound           = errors.New("item not found in cart")
	ErrInvalidQuantity        = errors.New("quantity must be greater than zero")
	ErrInvalidPrice           = errors.New("price cannot be negative")
	ErrArithmeticOverflow     = errors.New("monetary arithmetic integer overflow")
	ErrOptimisticLockConflict = errors.New("optimistic lock conflict: cart modified by another request")
	ErrUnauthorized           = errors.New("unauthorized: missing or invalid customer authentication")
	ErrForbidden              = errors.New("forbidden: customer does not own this cart")
	ErrProductUnavailable     = errors.New("product is unavailable or does not exist")
	ErrCurrencyMismatch       = errors.New("currency mismatch")
	ErrInvalidVersion         = errors.New("invalid or missing expected version")
)
