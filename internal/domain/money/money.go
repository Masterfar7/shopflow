// internal/domain/money/money.go
package money

import (
	"errors"
	"fmt"
	"math"
	"regexp"
)

var currencyRegex = regexp.MustCompile(`^[A-Z]{3}$`)

var (
	ErrNegativeAmount     = errors.New("monetary amount cannot be negative")
	ErrInvalidCurrency    = errors.New("currency must be 3-letter uppercase ISO-4217 code")
	ErrCurrencyMismatch   = errors.New("cannot operate on mismatched currencies")
	ErrArithmeticOverflow = errors.New("monetary arithmetic integer overflow")
	ErrNegativeMultiplier = errors.New("multiplication quantity cannot be negative")
)

// Money represents a monetary value in 64-bit integer minor currency units (e.g. cents).
// Floating-point representations are strictly prohibited across all domain operations.
type Money struct {
	Amount   int64  `json:"amount"`   // minor currency units (e.g. 1999 for $19.99)
	Currency string `json:"currency"` // ISO-4217 3-letter uppercase code, e.g. "USD"
}

// New creates a new validated Money value object.
func New(amount int64, currency string) (Money, error) {
	if amount < 0 {
		return Money{}, ErrNegativeAmount
	}
	if !currencyRegex.MatchString(currency) {
		return Money{}, ErrInvalidCurrency
	}
	return Money{Amount: amount, Currency: currency}, nil
}

// Add adds another Money amount of the same currency with overflow protection.
func (m Money) Add(other Money) (Money, error) {
	if m.Currency != other.Currency {
		return Money{}, ErrCurrencyMismatch
	}
	if other.Amount > 0 && m.Amount > math.MaxInt64-other.Amount {
		return Money{}, ErrArithmeticOverflow
	}
	return Money{Amount: m.Amount + other.Amount, Currency: m.Currency}, nil
}

// Sub subtracts another Money amount of the same currency. Returns ErrNegativeAmount if result would be negative.
func (m Money) Sub(other Money) (Money, error) {
	if m.Currency != other.Currency {
		return Money{}, ErrCurrencyMismatch
	}
	if m.Amount < other.Amount {
		return Money{}, ErrNegativeAmount
	}
	return Money{Amount: m.Amount - other.Amount, Currency: m.Currency}, nil
}

// Multiply multiplies the monetary amount by an integer quantity with overflow protection.
func (m Money) Multiply(quantity int64) (Money, error) {
	if quantity < 0 {
		return Money{}, ErrNegativeMultiplier
	}
	if quantity == 0 || m.Amount == 0 {
		return Money{Amount: 0, Currency: m.Currency}, nil
	}
	if m.Amount > math.MaxInt64/quantity {
		return Money{}, ErrArithmeticOverflow
	}
	return Money{Amount: m.Amount * quantity, Currency: m.Currency}, nil
}

// Format formats the monetary value as currency code followed by decimal major.minor units.
func (m Money) Format() string {
	major := m.Amount / 100
	minor := m.Amount % 100
	return fmt.Sprintf("%s %d.%02d", m.Currency, major, minor)
}
