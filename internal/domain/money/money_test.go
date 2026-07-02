package money_test

import (
	"encoding/json"
	"math"
	"testing"

	"shopflow/internal/domain/money"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMoney_ValidCreation(t *testing.T) {
	m, err := money.New(1999, "USD")
	require.NoError(t, err)
	assert.Equal(t, int64(1999), m.Amount)
	assert.Equal(t, "USD", m.Currency)
}

func TestMoney_NegativeAmount_Rejected(t *testing.T) {
	_, err := money.New(-1, "USD")
	require.ErrorIs(t, err, money.ErrNegativeAmount)
}

func TestMoney_InvalidCurrency_Rejected(t *testing.T) {
	invalidCurrencies := []string{"usd", "US1", "USDD", "U", "", "123", "US$", "US "}
	for _, curr := range invalidCurrencies {
		_, err := money.New(100, curr)
		require.ErrorIs(t, err, money.ErrInvalidCurrency, "expected invalid currency for %q", curr)
	}
}

func TestMoney_Addition_Success(t *testing.T) {
	m1, err := money.New(1000, "USD")
	require.NoError(t, err)
	m2, err := money.New(500, "USD")
	require.NoError(t, err)

	result, err := m1.Add(m2)
	require.NoError(t, err)
	assert.Equal(t, int64(1500), result.Amount)
	assert.Equal(t, "USD", result.Currency)
}

func TestMoney_Addition_CurrencyMismatch(t *testing.T) {
	m1, err := money.New(1000, "USD")
	require.NoError(t, err)
	m2, err := money.New(500, "EUR")
	require.NoError(t, err)

	_, err = m1.Add(m2)
	require.ErrorIs(t, err, money.ErrCurrencyMismatch)
}

func TestMoney_Addition_Overflow(t *testing.T) {
	m1, err := money.New(math.MaxInt64-10, "USD")
	require.NoError(t, err)
	m2, err := money.New(20, "USD")
	require.NoError(t, err)

	_, err = m1.Add(m2)
	require.ErrorIs(t, err, money.ErrArithmeticOverflow)
}

func TestMoney_Subtraction(t *testing.T) {
	m1, err := money.New(1000, "USD")
	require.NoError(t, err)
	m2, err := money.New(400, "USD")
	require.NoError(t, err)

	result, err := m1.Sub(m2)
	require.NoError(t, err)
	assert.Equal(t, int64(600), result.Amount)

	// Currency mismatch
	eur, err := money.New(100, "EUR")
	require.NoError(t, err)
	_, err = m1.Sub(eur)
	require.ErrorIs(t, err, money.ErrCurrencyMismatch)

	// Negative result
	m3, err := money.New(2000, "USD")
	require.NoError(t, err)
	_, err = m1.Sub(m3)
	require.ErrorIs(t, err, money.ErrNegativeAmount)
}

func TestMoney_Multiplication_Success(t *testing.T) {
	m, err := money.New(250, "USD")
	require.NoError(t, err)

	result, err := m.Multiply(4)
	require.NoError(t, err)
	assert.Equal(t, int64(1000), result.Amount)
	assert.Equal(t, "USD", result.Currency)
}

func TestMoney_Multiplication_Zero(t *testing.T) {
	m, err := money.New(250, "USD")
	require.NoError(t, err)

	result, err := m.Multiply(0)
	require.NoError(t, err)
	assert.Equal(t, int64(0), result.Amount)
}

func TestMoney_Multiplication_NegativeQuantity(t *testing.T) {
	m, err := money.New(250, "USD")
	require.NoError(t, err)

	_, err = m.Multiply(-2)
	require.ErrorIs(t, err, money.ErrNegativeMultiplier)
}

func TestMoney_Multiplication_Overflow(t *testing.T) {
	m, err := money.New(math.MaxInt64/2+1, "USD")
	require.NoError(t, err)

	_, err = m.Multiply(2)
	require.ErrorIs(t, err, money.ErrArithmeticOverflow)
}

func TestMoney_Format(t *testing.T) {
	m1, err := money.New(1999, "USD")
	require.NoError(t, err)
	assert.Equal(t, "USD 19.99", m1.Format())

	m2, err := money.New(5, "EUR")
	require.NoError(t, err)
	assert.Equal(t, "EUR 0.05", m2.Format())

	m3, err := money.New(500, "GBP")
	require.NoError(t, err)
	assert.Equal(t, "GBP 5.00", m3.Format())
}

func TestMoney_JSONSerialization(t *testing.T) {
	m, err := money.New(1999, "USD")
	require.NoError(t, err)

	data, err := json.Marshal(m)
	require.NoError(t, err)
	assert.JSONEq(t, `{"amount":1999,"currency":"USD"}`, string(data))

	var parsed money.Money
	err = json.Unmarshal(data, &parsed)
	require.NoError(t, err)
	assert.Equal(t, m, parsed)
}
