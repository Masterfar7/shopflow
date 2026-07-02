package money_test

import (
	"encoding/json"
	"math"
	"reflect"
	"testing"

	"shopflow/internal/domain/cart"
	"shopflow/internal/domain/catalog"
	"shopflow/internal/domain/money"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAdversarialMoney_BoundaryArithmeticFuzzing verifies arithmetic boundaries,
// overflow protection, zero values, and negative inputs.
func TestAdversarialMoney_BoundaryArithmeticFuzzing(t *testing.T) {
	t.Run("Addition_MaxInt64_Boundaries", func(t *testing.T) {
		// Boundary: MaxInt64 + 0 = MaxInt64
		mMax, err := money.New(math.MaxInt64, "USD")
		require.NoError(t, err)
		mZero, err := money.New(0, "USD")
		require.NoError(t, err)

		res, err := mMax.Add(mZero)
		require.NoError(t, err)
		assert.Equal(t, int64(math.MaxInt64), res.Amount)

		// MaxInt64 + 1 must overflow
		mOne, err := money.New(1, "USD")
		require.NoError(t, err)
		_, err = mMax.Add(mOne)
		require.ErrorIs(t, err, money.ErrArithmeticOverflow)

		// (MaxInt64 - 10) + 11 must overflow
		mNearMax, err := money.New(math.MaxInt64-10, "USD")
		require.NoError(t, err)
		mEleven, err := money.New(11, "USD")
		require.NoError(t, err)
		_, err = mNearMax.Add(mEleven)
		require.ErrorIs(t, err, money.ErrArithmeticOverflow)

		// (MaxInt64 - 10) + 10 = MaxInt64 (exact boundary)
		mTen, err := money.New(10, "USD")
		require.NoError(t, err)
		resExact, err := mNearMax.Add(mTen)
		require.NoError(t, err)
		assert.Equal(t, int64(math.MaxInt64), resExact.Amount)
	})

	t.Run("Subtraction_Zero_And_Negative_Boundaries", func(t *testing.T) {
		m500, err := money.New(500, "USD")
		require.NoError(t, err)

		// Subtraction resulting in exact zero: 500 - 500 = 0
		resZero, err := m500.Sub(m500)
		require.NoError(t, err)
		assert.Equal(t, int64(0), resZero.Amount)
		assert.Equal(t, "USD", resZero.Currency)

		// Subtraction below zero: 500 - 501 = ErrNegativeAmount
		m501, err := money.New(501, "USD")
		require.NoError(t, err)
		_, err = m500.Sub(m501)
		require.ErrorIs(t, err, money.ErrNegativeAmount)

		// 0 - 1 = ErrNegativeAmount
		mZero, err := money.New(0, "USD")
		require.NoError(t, err)
		mOne, err := money.New(1, "USD")
		require.NoError(t, err)
		_, err = mZero.Sub(mOne)
		require.ErrorIs(t, err, money.ErrNegativeAmount)
	})

	t.Run("Multiplication_Boundaries_And_Fuzzing", func(t *testing.T) {
		// Exact boundary: (MaxInt64 / 7) * 7
		multiplier := int64(7)
		maxDiv7 := math.MaxInt64 / multiplier
		mMulBase, err := money.New(maxDiv7, "USD")
		require.NoError(t, err)

		resMul, err := mMulBase.Multiply(multiplier)
		require.NoError(t, err)
		assert.Equal(t, maxDiv7*multiplier, resMul.Amount)

		// One unit above boundary: (MaxInt64 / 7 + 1) * 7 must overflow
		mOverflowBase, err := money.New(maxDiv7+1, "USD")
		require.NoError(t, err)
		_, err = mOverflowBase.Multiply(multiplier)
		require.ErrorIs(t, err, money.ErrArithmeticOverflow)

		// Multiplication by zero yields zero
		resZeroMul, err := mOverflowBase.Multiply(0)
		require.NoError(t, err)
		assert.Equal(t, int64(0), resZeroMul.Amount)
		assert.Equal(t, "USD", resZeroMul.Currency)

		// Negative multipliers must fail
		negativeMultipliers := []int64{-1, -100, math.MinInt64}
		for _, nm := range negativeMultipliers {
			_, err = mMulBase.Multiply(nm)
			require.ErrorIs(t, err, money.ErrNegativeMultiplier, "expected ErrNegativeMultiplier for %d", nm)
		}
	})

	t.Run("Currency_Validation_Fuzzing", func(t *testing.T) {
		invalidCodes := []string{
			"", "U", "US", "USDD", "usd", "Usd", "uSD", "123", "US1", "U$D",
			" USD", "USD ", "U SD", "\nUSD", "USD\t",
		}
		for _, code := range invalidCodes {
			_, err := money.New(100, code)
			require.ErrorIs(t, err, money.ErrInvalidCurrency, "expected invalid currency for %q", code)
		}

		validCodes := []string{"USD", "EUR", "GBP", "JPY", "CAD", "AUD", "CHF"}
		for _, code := range validCodes {
			m, err := money.New(100, code)
			require.NoError(t, err, "expected valid currency for %q", code)
			assert.Equal(t, code, m.Currency)
		}
	})
}

// TestAdversarialMoney_ZeroFloatVerification verifies that float representations
// are completely rejected during serialization, and verifies via reflection that
// NO float32 or float64 fields exist in any domain model structs.
func TestAdversarialMoney_ZeroFloatVerification(t *testing.T) {
	t.Run("JSON_Float_Deserialization_Rejection", func(t *testing.T) {
		floatPayloads := []string{
			`{"amount": 19.99, "currency": "USD"}`,
			`{"amount": 19.0, "currency": "USD"}`,
			`{"amount": 1.5e3, "currency": "USD"}`,
			`{"amount": 0.01, "currency": "USD"}`,
			`{"amount": -0.5, "currency": "USD"}`,
		}

		for _, payload := range floatPayloads {
			var m money.Money
			err := json.Unmarshal([]byte(payload), &m)
			require.Error(t, err, "expected unmarshal error for float payload: %s", payload)
			assert.Contains(t, err.Error(), "cannot unmarshal number")
		}
	})

	t.Run("Structural_Reflection_Zero_Floats_In_Domain_Models", func(t *testing.T) {
		typesToCheck := []any{
			money.Money{},
			catalog.Product{},
			catalog.Category{},
			cart.Cart{},
			cart.CartItem{},
		}

		for _, item := range typesToCheck {
			val := reflect.ValueOf(item)
			typ := val.Type()
			t.Logf("Checking struct %s for absence of float types...", typ.String())

			for i := 0; i < typ.NumField(); i++ {
				field := typ.Field(i)
				kind := field.Type.Kind()

				assert.NotEqual(t, reflect.Float32, kind,
					"Struct %s field %s must NOT be float32", typ.Name(), field.Name)
				assert.NotEqual(t, reflect.Float64, kind,
					"Struct %s field %s must NOT be float64", typ.Name(), field.Name)

				// If the field is a nested struct, check it as well
				if kind == reflect.Struct {
					for j := 0; j < field.Type.NumField(); j++ {
						nestedField := field.Type.Field(j)
						nestedKind := nestedField.Type.Kind()
						assert.NotEqual(t, reflect.Float32, nestedKind,
							"Struct %s.%s nested field %s must NOT be float32", typ.Name(), field.Name, nestedField.Name)
						assert.NotEqual(t, reflect.Float64, nestedKind,
							"Struct %s.%s nested field %s must NOT be float64", typ.Name(), field.Name, nestedField.Name)
					}
				}
			}
		}
	})
}
