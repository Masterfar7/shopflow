# ADR-0004: Strict int64 Minor Unit Monetary Arithmetic

- **Status**: Accepted
- **Deciders**: Lead Architect, Financial Domain Specialist
- **Date**: 2026-09-10
- **Consulted**: M0 Explorer 3, Adversarial Reviewer

## 1. Context and Problem Statement

Financial computations in e-commerce systems require absolute mathematical precision. Many software systems mistakenly represent monetary values using floating-point types (`float32`, `float64`).

### The Floating-Point Problem:
Standard IEEE-754 floating-point representations store numbers in base-2 fractions. Decimals such as 0.10, 0.20, or 0.70 cannot be represented exactly in binary floating point:
- In Go: `0.1 + 0.2` results in `0.30000000000000004`.
- Multiplying $19.99 by a quantity of 3, applying a 15% discount, and calculating a 7.25% sales tax using floating-point operations introduces cumulative rounding drift and fractional cents.
- Over millions of transactions, rounding drift leads to ledger discrepancies, reconciliation failures between ShopFlow and external payment gateways (Stripe/PayPal), and severe regulatory audit non-compliance.

## 2. Decision

We strictly mandate that **all monetary values across the entire ShopFlow platform are represented as 64-bit signed integers (`int64`) in minor currency units** (e.g., cents in USD/EUR, kopecks in RUB, yen in JPY).

### 2.1 Concrete Rules:
1. **Zero Floats Policy**:
   - `float32` and `float64` are STRICTLY BANNED from domain models, persistence schemas, API contracts, JSON representations, and Protobuf definitions.
   - Database columns MUST use `BIGINT NOT NULL` with an explicit `_minor` column name suffix and a non-negative check constraint:
     ```sql
     unit_price_minor BIGINT NOT NULL CHECK (unit_price_minor >= 0),
     currency VARCHAR(3) NOT NULL DEFAULT 'USD'
     ```
2. **Canonical `Money` Value Object**:
   All Go domain packages must represent money using the immutable value object:
   ```go
   package money

   import (
       "errors"
       "math"
   )

   type Money struct {
       Amount   int64  `json:"amount"`   // In minor units (e.g., cents)
       Currency string `json:"currency"` // ISO-4217, e.g. "USD"
   }

   func New(amount int64, currency string) (Money, error) {
       if amount < 0 {
           return Money{}, errors.New("monetary amount cannot be negative")
       }
       if len(currency) != 3 {
           return Money{}, errors.New("currency must be 3-letter ISO-4217 code")
       }
       return Money{Amount: amount, Currency: currency}, nil
   }

   func (m Money) Add(other Money) (Money, error) {
       if m.Currency != other.Currency {
           return Money{}, errors.New("currency mismatch")
       }
       if other.Amount > 0 && m.Amount > math.MaxInt64-other.Amount {
           return Money{}, errors.New("monetary addition overflow")
       }
       return Money{Amount: m.Amount + other.Amount, Currency: m.Currency}, nil
   }

   func (m Money) Multiply(quantity int64) (Money, error) {
       if quantity < 0 {
           return Money{}, errors.New("quantity cannot be negative")
       }
       if quantity > 0 && m.Amount > math.MaxInt64/quantity {
           return Money{}, errors.New("monetary multiplication overflow")
       }
       return Money{Amount: m.Amount * quantity, Currency: m.Currency}, nil
   }
   ```
3. **Deterministic Division & Tax Allocation**:
   When distributing discounts or computing taxes across line items, division remainders must be distributed penny-by-penny (e.g. the largest remainder method) so that:
   $$\sum \text{LineItemTotals} = \text{OrderTotal}$$
   No fractional cents may ever be discarded or created out of thin air.

## 3. Consequences

### Positive
- **Absolute Precision**: Exact, reproducible arithmetic across all CPU architectures, operating systems, and database engines.
- **Direct Database Aggregations**: Enables clean, lossless SQL queries like `SELECT SUM(total_minor) FROM orders`.
- **Seamless Payment Gateway Alignment**: Major payment processors (Stripe, Adyen) expect amounts in integer minor units. Zero conversion risk.
- **Audit-Ready Financial Integrity**: Guarantees zero penny drift in payment reconciliation.

### Negative / Trade-offs
- **Display Conversion Required**: Human-readable decimal strings (e.g., "$19.99") must be explicitly formatted at the API/UI presentation layer.
- **Multi-Currency Conversions**: Currency exchange rate conversions (if introduced in future phases) must be handled via fixed-point integer basis points (e.g. multiplying by exchange rate scaled by $10^6$ and integer dividing).

## 4. Invariants Enforced
- Float types are rejected at compile time and database schema validation.
- All monetary amounts check bounds against `math.MaxInt64`.
- Order subtotal, tax, and grand total invariants:
  $$\text{total\_minor} = \text{subtotal\_minor} + \text{tax\_minor} + \text{shipping\_minor} - \text{discount\_minor}$$

## 5. Compliance Verification
- Automated static checks and reviewer veto rules reject any PR introducing `float32` or `float64` in money fields.
- **Tier 2 Test**: `TestCatalog_MaxInt64Overflow_Protection` and `TestCatalog_ZeroOrNegativePrice_Rejected` assert strict boundary enforcement.
