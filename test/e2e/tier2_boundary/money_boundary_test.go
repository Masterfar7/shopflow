package tier2_boundary

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"testing"
	"time"

	"shopflow/test/e2e/harness"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Invariant: Float money in request payload MUST be rejected with HTTP 400 Bad Request.
func TestFloatMoneyRejection(t *testing.T) {
	env := harness.SetupEnvironment(t)
	client := env.RequireHTTPServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Sending a float value in price_minor field
	rawFloatPayload := []byte(`{
		"sku": "SKU-FLOAT-TEST",
		"title": "Float Injection Item",
		"price_minor": 19.99,
		"currency": "USD"
	}`)

	headers := map[string]string{
		"Content-Type":    "application/json",
		"Idempotency-Key": uuid.New().String(),
	}

	statusCode, respBody, _, err := client.SendRaw(ctx, http.MethodPost, "/api/v1/products", rawFloatPayload, headers)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, statusCode, "float monetary values MUST be rejected with HTTP 400")
	assert.NotEmpty(t, respBody)
}

// Invariant: Zero or negative prices in catalog MUST be rejected.
func TestZeroAndNegativePriceRejection(t *testing.T) {
	env := harness.SetupEnvironment(t)
	client := env.RequireHTTPServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	testCases := []struct {
		name       string
		priceMinor int64
	}{
		{"zero price", 0},
		{"negative price", -500},
		{"extreme negative price", -999999},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			rawPayload := fmt.Sprintf(`{
				"sku": "SKU-NEG-%s",
				"title": "Invalid Price Item",
				"price_minor": %d,
				"currency": "USD"
			}`, uuid.New().String()[:8], tc.priceMinor)

			headers := map[string]string{
				"Content-Type":    "application/json",
				"Idempotency-Key": uuid.New().String(),
			}

			statusCode, _, _, err := client.SendRaw(ctx, http.MethodPost, "/api/v1/products", []byte(rawPayload), headers)
			require.NoError(t, err)
			assert.Equal(t, http.StatusBadRequest, statusCode, "zero or negative prices must be rejected with 400 Bad Request")
		})
	}
}

// Invariant: Integer overflow prevention near math.MaxInt64.
func TestExtremeMonetaryOverflowPrevention(t *testing.T) {
	env := harness.SetupEnvironment(t)
	client := env.RequireHTTPServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Value exceeding int64 range or causing multiplication overflow
	rawPayload := fmt.Sprintf(`{
		"sku": "SKU-MAXINT-%s",
		"title": "Overflow Item",
		"price_minor": %d,
		"currency": "USD"
	}`, uuid.New().String()[:8], int64(math.MaxInt64))

	headers := map[string]string{
		"Content-Type":    "application/json",
		"Idempotency-Key": uuid.New().String(),
	}

	statusCode, _, _, err := client.SendRaw(ctx, http.MethodPost, "/api/v1/products", []byte(rawPayload), headers)
	require.NoError(t, err)
	assert.True(t, statusCode == http.StatusBadRequest || statusCode == http.StatusUnprocessableEntity)
}

// Database Invariant: Database CHECK constraint enforces price_minor > 0.
func TestDatabaseMoneyCheckConstraint(t *testing.T) {
	env := harness.SetupEnvironment(t)
	db := env.RequireDB(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Attempt inserting product with price_minor <= 0 directly
	_, err := db.Pool.Exec(ctx, `
		INSERT INTO products (sku, title, price_minor, currency)
		VALUES ('SKU-DB-FAIL', 'DB Check Fail', 0, 'USD');
	`)
	assert.Error(t, err, "database CHECK constraint MUST prevent price_minor <= 0")
}
