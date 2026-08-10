package payment

import "errors"

var (
	// ErrPaymentNotFound indicates the requested payment was not found.
	ErrPaymentNotFound = errors.New("payment not found")

	// ErrPaymentDeclined indicates the payment simulation trigger caused a decline.
	ErrPaymentDeclined = errors.New("payment declined")

	// ErrPaymentTimeout indicates the payment simulation triggered a network or gateway timeout.
	ErrPaymentTimeout = errors.New("payment gateway timeout")

	// ErrInvalidPaymentAmount indicates the requested payment amount is <= 0.
	ErrInvalidPaymentAmount = errors.New("invalid payment amount: must be strictly positive")

	// ErrInvalidRefundAmount indicates the refund amount is <= 0 or exceeds captured payment amount.
	ErrInvalidRefundAmount = errors.New("invalid refund amount: must be positive and cannot exceed captured amount")

	// ErrPaymentAlreadyRefunded indicates the payment has already entered terminal REFUNDED state.
	ErrPaymentAlreadyRefunded = errors.New("payment has already been refunded")

	// ErrPaymentCannotBeRefunded indicates the payment is not in SUCCESS state and cannot be refunded.
	ErrPaymentCannotBeRefunded = errors.New("only successfully captured payments can be refunded")

	// ErrIdempotencyConflict indicates an idempotency key was reused with conflicting parameters.
	ErrIdempotencyConflict = errors.New("idempotency key reused with conflicting payload parameters")
)
