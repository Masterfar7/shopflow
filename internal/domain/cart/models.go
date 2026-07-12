// internal/domain/cart/models.go
package cart

import (
	"math"
	"time"

	"shopflow/internal/domain/money"

	"github.com/google/uuid"
)

type CartStatus string

const (
	CartStatusActive     CartStatus = "ACTIVE"
	CartStatusCheckedOut CartStatus = "CHECKED_OUT"
	CartStatusAbandoned  CartStatus = "ABANDONED"
)

type CartItem struct {
	ID             uuid.UUID   `json:"id"`
	CartID         uuid.UUID   `json:"cart_id"`
	SKU            string      `json:"sku"`
	Title          string      `json:"title"` // Enriched via CatalogReader
	UnitPriceMinor int64       `json:"-"`
	Quantity       int         `json:"quantity"`
	UnitPrice      money.Money `json:"unit_price"`
	LineTotal      money.Money `json:"line_total"`
	CreatedAt      time.Time   `json:"created_at"`
	UpdatedAt      time.Time   `json:"updated_at"`
}

type Cart struct {
	ID          uuid.UUID   `json:"cart_id"`
	UserID      uuid.UUID   `json:"customer_id"`
	Status      CartStatus  `json:"status"`
	Version     int64       `json:"version"`
	Currency    string      `json:"currency"`
	Items       []CartItem  `json:"items"`
	TotalAmount money.Money `json:"total_amount"`
	CreatedAt   time.Time   `json:"created_at"`
	UpdatedAt   time.Time   `json:"updated_at"`
}

// CalculateTotals computes line item totals and the aggregate cart total.
// Enforces bounds checks against math.MaxInt64 overflow, rejects negative numbers,
// and asserts matching currency across all items.
func (c *Cart) CalculateTotals(currency string) error {
	if currency == "" {
		if c.Currency != "" {
			currency = c.Currency
		} else {
			currency = "USD"
		}
	}
	c.Currency = currency
	var grandTotal int64 = 0

	for i := range c.Items {
		item := &c.Items[i]
		if item.Quantity <= 0 {
			return ErrInvalidQuantity
		}
		if item.UnitPriceMinor < 0 {
			return ErrInvalidPrice
		}

		// Assert matching currency across items
		if item.UnitPrice.Currency != "" && item.UnitPrice.Currency != currency {
			return ErrCurrencyMismatch
		}
		if item.LineTotal.Currency != "" && item.LineTotal.Currency != currency {
			return ErrCurrencyMismatch
		}

		// Multiplication overflow check: unit_price_minor * quantity
		qty64 := int64(item.Quantity)
		if qty64 > 0 && item.UnitPriceMinor > math.MaxInt64/qty64 {
			return ErrArithmeticOverflow
		}
		lineTotalMinor := item.UnitPriceMinor * qty64

		// Addition overflow check: grandTotal + lineTotalMinor
		if lineTotalMinor > 0 && grandTotal > math.MaxInt64-lineTotalMinor {
			return ErrArithmeticOverflow
		}
		grandTotal += lineTotalMinor

		item.UnitPrice = money.Money{Amount: item.UnitPriceMinor, Currency: currency}
		item.LineTotal = money.Money{Amount: lineTotalMinor, Currency: currency}
	}

	c.TotalAmount = money.Money{Amount: grandTotal, Currency: currency}
	return nil
}

// Request DTOs
type CreateCartRequest struct {
	CustomerID uuid.UUID `json:"customer_id"`
}

type AddCartItemRequest struct {
	SKU      string `json:"sku"`
	Quantity int    `json:"quantity"`
}

type UpdateCartItemRequest struct {
	Quantity int `json:"quantity"`
}
