package cart_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"shopflow/internal/domain/cart"
	"shopflow/internal/domain/catalog"
	"shopflow/internal/domain/money"
	"shopflow/internal/platform/web"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestChallenger_F01_AuthenticationSeparation verifies that requests without valid authentication
// consistently return HTTP 401 Unauthorized, while requests with mismatched authentication return HTTP 403 Forbidden.
func TestChallenger_F01_AuthenticationSeparation(t *testing.T) {
	router, svc, reader := setupCartTestRouter(t)
	ownerID := uuid.New()
	mismatchedID := uuid.New()

	reader.AddProduct(&catalog.Product{
		SKU:        "SKU-F01-AUTH",
		Title:      "F01 Auth Item",
		PriceMinor: 2000,
		Price:      money.Money{Amount: 2000, Currency: "USD"},
		IsActive:   true,
	})

	cartObj, err := svc.CreateOrGetActiveCart(t.Context(), ownerID)
	require.NoError(t, err)
	cartID := cartObj.ID.String()

	cartObj, err = svc.AddItem(t.Context(), cartObj.ID, ownerID, "SKU-F01-AUTH", 2, 1)
	require.NoError(t, err)
	assert.Equal(t, int64(2), cartObj.Version)

	t.Run("Missing_Or_Invalid_X_User_ID_Returns_401", func(t *testing.T) {
		invalidHeaderCases := []struct {
			name   string
			header string
			setHdr bool
		}{
			{"Omitted_Header", "", false},
			{"Empty_Header", "", true},
			{"Whitespace_Header", "    ", true},
			{"Malformed_UUID", "12345-not-a-uuid", true},
			{"Nil_UUID", "00000000-0000-0000-0000-000000000000", true},
		}

		for _, tc := range invalidHeaderCases {
			t.Run(tc.name, func(t *testing.T) {
				// 1. POST /carts with invalid/missing header and empty body
				createReq := httptest.NewRequest(http.MethodPost, "/api/v1/carts", bytes.NewReader([]byte("{}")))
				createReq.Header.Set("Content-Type", "application/json")
				if tc.setHdr {
					createReq.Header.Set("X-User-ID", tc.header)
				}
				createRec := httptest.NewRecorder()
				router.ServeHTTP(createRec, createReq)
				assert.Equal(t, http.StatusUnauthorized, createRec.Code)
				assert.Equal(t, "application/problem+json", createRec.Header().Get("Content-Type"))

				// 2. GET /carts/{id}
				getReq := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/carts/%s", cartID), nil)
				if tc.setHdr {
					getReq.Header.Set("X-User-ID", tc.header)
				}
				getRec := httptest.NewRecorder()
				router.ServeHTTP(getRec, getReq)
				assert.Equal(t, http.StatusUnauthorized, getRec.Code)

				// 3. POST /carts/{id}/items
				addReq := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/carts/%s/items", cartID), bytes.NewReader([]byte(`{"sku":"SKU-F01-AUTH","quantity":1}`)))
				addReq.Header.Set("Content-Type", "application/json")
				addReq.Header.Set("If-Match", `"2"`)
				if tc.setHdr {
					addReq.Header.Set("X-User-ID", tc.header)
				}
				addRec := httptest.NewRecorder()
				router.ServeHTTP(addRec, addReq)
				assert.Equal(t, http.StatusUnauthorized, addRec.Code)

				// 4. PUT /carts/{id}/items/{sku}
				putReq := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/api/v1/carts/%s/items/SKU-F01-AUTH", cartID), bytes.NewReader([]byte(`{"quantity":3}`)))
				putReq.Header.Set("Content-Type", "application/json")
				putReq.Header.Set("If-Match", `"2"`)
				if tc.setHdr {
					putReq.Header.Set("X-User-ID", tc.header)
				}
				putRec := httptest.NewRecorder()
				router.ServeHTTP(putRec, putReq)
				assert.Equal(t, http.StatusUnauthorized, putRec.Code)

				// 5. DELETE /carts/{id}/items/{sku}
				delReq := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/api/v1/carts/%s/items/SKU-F01-AUTH", cartID), nil)
				delReq.Header.Set("If-Match", `"2"`)
				if tc.setHdr {
					delReq.Header.Set("X-User-ID", tc.header)
				}
				delRec := httptest.NewRecorder()
				router.ServeHTTP(delRec, delReq)
				assert.Equal(t, http.StatusUnauthorized, delRec.Code)

				// 6. DELETE /carts/{id}/clear
				clearReq := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/api/v1/carts/%s/clear", cartID), nil)
				clearReq.Header.Set("If-Match", `"2"`)
				if tc.setHdr {
					clearReq.Header.Set("X-User-ID", tc.header)
				}
				clearRec := httptest.NewRecorder()
				router.ServeHTTP(clearRec, clearReq)
				assert.Equal(t, http.StatusUnauthorized, clearRec.Code)
			})
		}
	})

	t.Run("Mismatched_Owner_Returns_403", func(t *testing.T) {
		// 1. GET /carts/{id}
		getReq := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/carts/%s", cartID), nil)
		getReq.Header.Set("X-User-ID", mismatchedID.String())
		getRec := httptest.NewRecorder()
		router.ServeHTTP(getRec, getReq)
		assert.Equal(t, http.StatusForbidden, getRec.Code)
		assert.Equal(t, "application/problem+json", getRec.Header().Get("Content-Type"))

		// 2. POST /carts/{id}/items
		addReq := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/carts/%s/items", cartID), bytes.NewReader([]byte(`{"sku":"SKU-F01-AUTH","quantity":1}`)))
		addReq.Header.Set("Content-Type", "application/json")
		addReq.Header.Set("X-User-ID", mismatchedID.String())
		addReq.Header.Set("If-Match", `"2"`)
		addRec := httptest.NewRecorder()
		router.ServeHTTP(addRec, addReq)
		assert.Equal(t, http.StatusForbidden, addRec.Code)

		// 3. PUT /carts/{id}/items/{sku}
		putReq := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/api/v1/carts/%s/items/SKU-F01-AUTH", cartID), bytes.NewReader([]byte(`{"quantity":5}`)))
		putReq.Header.Set("Content-Type", "application/json")
		putReq.Header.Set("X-User-ID", mismatchedID.String())
		putReq.Header.Set("If-Match", `"2"`)
		putRec := httptest.NewRecorder()
		router.ServeHTTP(putRec, putReq)
		assert.Equal(t, http.StatusForbidden, putRec.Code)

		// 4. DELETE /carts/{id}/items/{sku}
		delReq := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/api/v1/carts/%s/items/SKU-F01-AUTH", cartID), nil)
		delReq.Header.Set("X-User-ID", mismatchedID.String())
		delReq.Header.Set("If-Match", `"2"`)
		delRec := httptest.NewRecorder()
		router.ServeHTTP(delRec, delReq)
		assert.Equal(t, http.StatusForbidden, delRec.Code)

		// 5. DELETE /carts/{id}/clear
		clearReq := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/api/v1/carts/%s/clear", cartID), nil)
		clearReq.Header.Set("X-User-ID", mismatchedID.String())
		clearReq.Header.Set("If-Match", `"2"`)
		clearRec := httptest.NewRecorder()
		router.ServeHTTP(clearRec, clearReq)
		assert.Equal(t, http.StatusForbidden, clearRec.Code)
	})

	t.Run("Service_Level_Auth_Enforcement", func(t *testing.T) {
		ctx := context.Background()

		// Nil customer ID -> ErrUnauthorized
		_, err := svc.CreateOrGetActiveCart(ctx, uuid.Nil)
		assert.ErrorIs(t, err, cart.ErrUnauthorized)

		_, err = svc.GetCart(ctx, cartObj.ID, uuid.Nil)
		assert.ErrorIs(t, err, cart.ErrUnauthorized)

		_, err = svc.AddItem(ctx, cartObj.ID, uuid.Nil, "SKU-F01-AUTH", 1, 2)
		assert.ErrorIs(t, err, cart.ErrUnauthorized)

		_, err = svc.UpdateQuantity(ctx, cartObj.ID, uuid.Nil, "SKU-F01-AUTH", 1, 2)
		assert.ErrorIs(t, err, cart.ErrUnauthorized)

		_, err = svc.RemoveItem(ctx, cartObj.ID, uuid.Nil, "SKU-F01-AUTH", 2)
		assert.ErrorIs(t, err, cart.ErrUnauthorized)

		_, err = svc.ClearCart(ctx, cartObj.ID, uuid.Nil, 2)
		assert.ErrorIs(t, err, cart.ErrUnauthorized)

		// Mismatched customer ID -> ErrForbidden
		_, err = svc.GetCart(ctx, cartObj.ID, mismatchedID)
		assert.ErrorIs(t, err, cart.ErrForbidden)

		_, err = svc.AddItem(ctx, cartObj.ID, mismatchedID, "SKU-F01-AUTH", 1, 2)
		assert.ErrorIs(t, err, cart.ErrForbidden)

		_, err = svc.UpdateQuantity(ctx, cartObj.ID, mismatchedID, "SKU-F01-AUTH", 1, 2)
		assert.ErrorIs(t, err, cart.ErrForbidden)

		_, err = svc.RemoveItem(ctx, cartObj.ID, mismatchedID, "SKU-F01-AUTH", 2)
		assert.ErrorIs(t, err, cart.ErrForbidden)

		_, err = svc.ClearCart(ctx, cartObj.ID, mismatchedID, 2)
		assert.ErrorIs(t, err, cart.ErrForbidden)
	})
}

// TestChallenger_F02_CurrencyMismatch verifies that adding products with conflicting currency
// is rejected with HTTP 400 Bad Request (CURRENCY_MISMATCH) and domain ErrCurrencyMismatch.
func TestChallenger_F02_CurrencyMismatch(t *testing.T) {
	router, svc, reader := setupCartTestRouter(t)
	customerID := uuid.New()

	// Products with diverse currencies
	currencies := []string{"EUR", "GBP", "JPY", "CAD", "CHF"}
	for _, cur := range currencies {
		reader.AddProduct(&catalog.Product{
			SKU:        fmt.Sprintf("SKU-CUR-%s", cur),
			Title:      fmt.Sprintf("Product in %s", cur),
			PriceMinor: 1000,
			Price:      money.Money{Amount: 1000, Currency: cur},
			IsActive:   true,
		})
	}

	reader.AddProduct(&catalog.Product{
		SKU:        "SKU-CUR-USD",
		Title:      "Product in USD",
		PriceMinor: 1000,
		Price:      money.Money{Amount: 1000, Currency: "USD"},
		IsActive:   true,
	})

	cartObj, err := svc.CreateOrGetActiveCart(t.Context(), customerID)
	require.NoError(t, err)
	assert.Equal(t, "USD", cartObj.Currency)
	cartID := cartObj.ID.String()

	t.Run("HTTP_Boundary_Rejects_Foreign_Currencies", func(t *testing.T) {
		for _, cur := range currencies {
			sku := fmt.Sprintf("SKU-CUR-%s", cur)
			body := []byte(fmt.Sprintf(`{"sku":"%s","quantity":1}`, sku))
			req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/carts/%s/items", cartID), bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-User-ID", customerID.String())
			req.Header.Set("If-Match", `"1"`)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			assert.Equal(t, http.StatusBadRequest, rec.Code, "Expected HTTP 400 for currency %s", cur)
			assert.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))

			var prob web.ProblemDetails
			err := json.Unmarshal(rec.Body.Bytes(), &prob)
			require.NoError(t, err)
			assert.Equal(t, "CURRENCY_MISMATCH", prob.Code)
		}
	})

	t.Run("HTTP_Boundary_Accepts_Matching_Currency", func(t *testing.T) {
		body := []byte(`{"sku":"SKU-CUR-USD","quantity":2}`)
		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/carts/%s/items", cartID), bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-User-ID", customerID.String())
		req.Header.Set("If-Match", `"1"`)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		var updated cart.Cart
		err := json.Unmarshal(rec.Body.Bytes(), &updated)
		require.NoError(t, err)
		assert.Equal(t, "USD", updated.Currency)
		assert.Equal(t, int64(2000), updated.TotalAmount.Amount)
	})

	t.Run("Service_Level_CurrencyMismatch", func(t *testing.T) {
		ctx := context.Background()
		_, err := svc.AddItem(ctx, cartObj.ID, customerID, "SKU-CUR-EUR", 1, 2)
		assert.ErrorIs(t, err, cart.ErrCurrencyMismatch)
	})

	t.Run("Cart_CalculateTotals_Rejects_Mixed_Currencies", func(t *testing.T) {
		mixedCart := &cart.Cart{
			Currency: "USD",
			Items: []cart.CartItem{
				{
					SKU:            "SKU-1",
					Quantity:       1,
					UnitPriceMinor: 100,
					UnitPrice:      money.Money{Amount: 100, Currency: "USD"},
				},
				{
					SKU:            "SKU-2",
					Quantity:       1,
					UnitPriceMinor: 200,
					UnitPrice:      money.Money{Amount: 200, Currency: "GBP"},
				},
			},
		}
		err := mixedCart.CalculateTotals("USD")
		assert.ErrorIs(t, err, cart.ErrCurrencyMismatch)
	})
}

// TestChallenger_F03_StrictOptimisticConcurrency verifies that mutating operations with expectedVersion <= 0
// return ErrInvalidVersion at the service layer, and that optimistic concurrency conflicts return HTTP 412.
func TestChallenger_F03_StrictOptimisticConcurrency(t *testing.T) {
	router, svc, reader := setupCartTestRouter(t)
	customerID := uuid.New()

	reader.AddProduct(&catalog.Product{
		SKU:        "SKU-F03-OCC",
		Title:      "F03 OCC Item",
		PriceMinor: 1000,
		Price:      money.Money{Amount: 1000, Currency: "USD"},
		IsActive:   true,
	})

	cartObj, err := svc.CreateOrGetActiveCart(t.Context(), customerID)
	require.NoError(t, err)
	cartID := cartObj.ID.String()

	t.Run("Service_Level_Rejects_Non_Positive_Versions", func(t *testing.T) {
		ctx := context.Background()
		nonPositiveVersions := []int64{0, -1, -5, -999}

		for _, v := range nonPositiveVersions {
			// AddItem
			_, err := svc.AddItem(ctx, cartObj.ID, customerID, "SKU-F03-OCC", 1, v)
			assert.ErrorIs(t, err, cart.ErrInvalidVersion, "AddItem should reject version %d with ErrInvalidVersion", v)

			// UpdateQuantity
			_, err = svc.UpdateQuantity(ctx, cartObj.ID, customerID, "SKU-F03-OCC", 2, v)
			assert.ErrorIs(t, err, cart.ErrInvalidVersion, "UpdateQuantity should reject version %d with ErrInvalidVersion", v)

			// RemoveItem
			_, err = svc.RemoveItem(ctx, cartObj.ID, customerID, "SKU-F03-OCC", v)
			assert.ErrorIs(t, err, cart.ErrInvalidVersion, "RemoveItem should reject version %d with ErrInvalidVersion", v)

			// ClearCart
			_, err = svc.ClearCart(ctx, cartObj.ID, customerID, v)
			assert.ErrorIs(t, err, cart.ErrInvalidVersion, "ClearCart should reject version %d with ErrInvalidVersion", v)
		}
	})

	t.Run("HTTP_Level_OCC_Conflict_Returns_412", func(t *testing.T) {
		// First add item with valid initial version 1 -> cart version becomes 2
		cartObj, err = svc.AddItem(t.Context(), cartObj.ID, customerID, "SKU-F03-OCC", 1, 1)
		require.NoError(t, err)
		assert.Equal(t, int64(2), cartObj.Version)

		// 1. AddItem with stale version "1" (current is 2) -> 412
		addReq := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/carts/%s/items", cartID), bytes.NewReader([]byte(`{"sku":"SKU-F03-OCC","quantity":1}`)))
		addReq.Header.Set("Content-Type", "application/json")
		addReq.Header.Set("X-User-ID", customerID.String())
		addReq.Header.Set("If-Match", `"1"`) // Stale!
		addRec := httptest.NewRecorder()
		router.ServeHTTP(addRec, addReq)

		assert.Equal(t, http.StatusPreconditionFailed, addRec.Code)
		assert.Equal(t, "application/problem+json", addRec.Header().Get("Content-Type"))

		// 2. UpdateQuantity with stale version "1" -> 412
		putReq := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/api/v1/carts/%s/items/SKU-F03-OCC", cartID), bytes.NewReader([]byte(`{"quantity":5}`)))
		putReq.Header.Set("Content-Type", "application/json")
		putReq.Header.Set("X-User-ID", customerID.String())
		putReq.Header.Set("If-Match", `"1"`) // Stale!
		putRec := httptest.NewRecorder()
		router.ServeHTTP(putRec, putReq)

		assert.Equal(t, http.StatusPreconditionFailed, putRec.Code)

		// 3. RemoveItem with stale version "1" -> 412
		delReq := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/api/v1/carts/%s/items/SKU-F03-OCC", cartID), nil)
		delReq.Header.Set("X-User-ID", customerID.String())
		delReq.Header.Set("If-Match", `"1"`) // Stale!
		delRec := httptest.NewRecorder()
		router.ServeHTTP(delRec, delReq)

		assert.Equal(t, http.StatusPreconditionFailed, delRec.Code)

		// 4. ClearCart with stale version "1" -> 412
		clearReq := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/api/v1/carts/%s/clear", cartID), nil)
		clearReq.Header.Set("X-User-ID", customerID.String())
		clearReq.Header.Set("If-Match", `"1"`) // Stale!
		clearRec := httptest.NewRecorder()
		router.ServeHTTP(clearRec, clearReq)

		assert.Equal(t, http.StatusPreconditionFailed, clearRec.Code)
	})

	t.Run("HTTP_Level_IfMatch_Extraction_Validation", func(t *testing.T) {
		// When clients send If-Match <= 0 (e.g. "0" or "-1"), web.ExtractIfMatch catches
		// this as an invalid header value and returns HTTP 400 PRECONDITION_REQUIRED,
		// preventing invalid version parameters from ever reaching the domain service.
		endpoints := []struct {
			name   string
			method string
			url    string
			body   []byte
		}{
			{"AddItem", http.MethodPost, fmt.Sprintf("/api/v1/carts/%s/items", cartID), []byte(`{"sku":"SKU-F03-OCC","quantity":1}`)},
			{"UpdateQuantity", http.MethodPut, fmt.Sprintf("/api/v1/carts/%s/items/SKU-F03-OCC", cartID), []byte(`{"quantity":3}`)},
			{"RemoveItem", http.MethodDelete, fmt.Sprintf("/api/v1/carts/%s/items/SKU-F03-OCC", cartID), nil},
			{"ClearCart", http.MethodDelete, fmt.Sprintf("/api/v1/carts/%s/clear", cartID), nil},
		}

		for _, ep := range endpoints {
			for _, invalidHdr := range []string{`"0"`, `0`, `"-1"`} {
				var bodyReader *bytes.Reader
				if ep.body != nil {
					bodyReader = bytes.NewReader(ep.body)
				} else {
					bodyReader = bytes.NewReader(nil)
				}
				req := httptest.NewRequest(ep.method, ep.url, bodyReader)
				if ep.body != nil {
					req.Header.Set("Content-Type", "application/json")
				}
				req.Header.Set("X-User-ID", customerID.String())
				req.Header.Set("If-Match", invalidHdr)
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)

				// Header validation rejection: HTTP 400 Bad Request
				assert.Equal(t, http.StatusBadRequest, rec.Code, "Expected HTTP 400 for %s with If-Match: %s", ep.name, invalidHdr)
			}
		}
	})
}

// TestChallenger_HighConcurrency_OCC_StressHarness stresses the cart domain under 50 concurrent mutating requests.
func TestChallenger_HighConcurrency_OCC_StressHarness(t *testing.T) {
	router, svc, reader := setupCartTestRouter(t)
	customerID := uuid.New()

	reader.AddProduct(&catalog.Product{
		SKU:        "SKU-CONC-STRESS",
		Title:      "Stress Test SKU",
		PriceMinor: 500,
		Price:      money.Money{Amount: 500, Currency: "USD"},
		IsActive:   true,
	})

	cartObj, err := svc.CreateOrGetActiveCart(t.Context(), customerID)
	require.NoError(t, err)
	cartID := cartObj.ID.String()

	concurrency := 50
	var wg sync.WaitGroup
	wg.Add(concurrency)

	var mu sync.Mutex
	var successCount int
	var conflictCount int

	for i := 0; i < concurrency; i++ {
		go func(idx int) {
			defer wg.Done()
			body := []byte(fmt.Sprintf(`{"sku":"SKU-CONC-STRESS","quantity":%d}`, idx+1))
			req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/carts/%s/items", cartID), bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-User-ID", customerID.String())
			req.Header.Set("If-Match", `"1"`) // All 50 compete with initial version 1!
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			mu.Lock()
			defer mu.Unlock()
			if rec.Code == http.StatusOK {
				successCount++
			} else if rec.Code == http.StatusPreconditionFailed {
				conflictCount++
			}
		}(i)
	}

	wg.Wait()

	// Invariant: Exactly 1 wins, exactly concurrency - 1 fail with 412 Precondition Failed
	assert.Equal(t, 1, successCount, "Exactly one concurrent request with If-Match '1' must succeed")
	assert.Equal(t, concurrency-1, conflictCount, "All other concurrent requests must fail with 412 Precondition Failed")

	// Verify cart state
	freshCart, err := svc.GetCart(t.Context(), cartObj.ID, customerID)
	require.NoError(t, err)
	assert.Equal(t, int64(2), freshCart.Version)
	require.Len(t, freshCart.Items, 1)
	assert.Equal(t, freshCart.Items[0].LineTotal.Amount, freshCart.TotalAmount.Amount)
}
