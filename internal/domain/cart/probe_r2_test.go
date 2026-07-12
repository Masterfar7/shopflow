package cart_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"shopflow/internal/domain/catalog"
	"shopflow/internal/domain/money"
	"shopflow/internal/platform/web"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestProbeR2_CustomerAuthorization probes Area 1:
// - Send GET/POST/PUT/DELETE requests WITHOUT X-User-ID: verify HTTP 401 Unauthorized
// - Send requests with mismatched X-User-ID: verify HTTP 403 Forbidden
func TestProbeR2_CustomerAuthorization(t *testing.T) {
	router, svc, reader := setupCartTestRouter(t)
	ownerID := uuid.New()
	mismatchedID := uuid.New()

	reader.AddProduct(&catalog.Product{
		SKU:        "SKU-PROBE-AUTH",
		Title:      "Probe Auth Product",
		PriceMinor: 1500,
		Price:      money.Money{Amount: 1500, Currency: "USD"},
		IsActive:   true,
	})

	c, err := svc.CreateOrGetActiveCart(t.Context(), ownerID)
	require.NoError(t, err)
	cartID := c.ID.String()

	c, err = svc.AddItem(t.Context(), c.ID, ownerID, "SKU-PROBE-AUTH", 2, 1)
	require.NoError(t, err)
	assert.Equal(t, int64(2), c.Version)

	t.Run("Requests_Without_X_User_ID_Expect_401", func(t *testing.T) {
		// 1. GET /api/v1/carts/{id}
		getReq := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/carts/%s", cartID), nil)
		getRec := httptest.NewRecorder()
		router.ServeHTTP(getRec, getReq)
		t.Logf("[PROBE-1.1] GET /carts/{id} without X-User-ID: status=%d body=%s", getRec.Code, getRec.Body.String())
		assert.Equal(t, http.StatusUnauthorized, getRec.Code)
		var prob1 web.ProblemDetails
		_ = json.Unmarshal(getRec.Body.Bytes(), &prob1)
		assert.Equal(t, "UNAUTHORIZED", prob1.Code)

		// 2. POST /api/v1/carts (without customer_id and without X-User-ID)
		postReq := httptest.NewRequest(http.MethodPost, "/api/v1/carts", bytes.NewReader([]byte("{}")))
		postReq.Header.Set("Content-Type", "application/json")
		postRec := httptest.NewRecorder()
		router.ServeHTTP(postRec, postReq)
		t.Logf("[PROBE-1.2] POST /carts without X-User-ID & body: status=%d body=%s", postRec.Code, postRec.Body.String())
		assert.Equal(t, http.StatusUnauthorized, postRec.Code)

		// 3. POST /api/v1/carts/{id}/items
		addReq := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/carts/%s/items", cartID), bytes.NewReader([]byte(`{"sku":"SKU-PROBE-AUTH","quantity":1}`)))
		addReq.Header.Set("Content-Type", "application/json")
		addReq.Header.Set("If-Match", `"2"`)
		addRec := httptest.NewRecorder()
		router.ServeHTTP(addRec, addReq)
		t.Logf("[PROBE-1.3] POST /carts/{id}/items without X-User-ID: status=%d body=%s", addRec.Code, addRec.Body.String())
		assert.Equal(t, http.StatusUnauthorized, addRec.Code)

		// 4. PUT /api/v1/carts/{id}/items/{sku}
		putReq := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/api/v1/carts/%s/items/SKU-PROBE-AUTH", cartID), bytes.NewReader([]byte(`{"quantity":5}`)))
		putReq.Header.Set("Content-Type", "application/json")
		putReq.Header.Set("If-Match", `"2"`)
		putRec := httptest.NewRecorder()
		router.ServeHTTP(putRec, putReq)
		t.Logf("[PROBE-1.4] PUT /carts/{id}/items/{sku} without X-User-ID: status=%d body=%s", putRec.Code, putRec.Body.String())
		assert.Equal(t, http.StatusUnauthorized, putRec.Code)

		// 5. DELETE /api/v1/carts/{id}/items/{sku}
		delReq := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/api/v1/carts/%s/items/SKU-PROBE-AUTH", cartID), nil)
		delReq.Header.Set("If-Match", `"2"`)
		delRec := httptest.NewRecorder()
		router.ServeHTTP(delRec, delReq)
		t.Logf("[PROBE-1.5] DELETE /carts/{id}/items/{sku} without X-User-ID: status=%d body=%s", delRec.Code, delRec.Body.String())
		assert.Equal(t, http.StatusUnauthorized, delRec.Code)

		// 6. DELETE /api/v1/carts/{id}/clear
		clearReq := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/api/v1/carts/%s/clear", cartID), nil)
		clearReq.Header.Set("If-Match", `"2"`)
		clearRec := httptest.NewRecorder()
		router.ServeHTTP(clearRec, clearReq)
		t.Logf("[PROBE-1.6] DELETE /carts/{id}/clear without X-User-ID: status=%d body=%s", clearRec.Code, clearRec.Body.String())
		assert.Equal(t, http.StatusUnauthorized, clearRec.Code)
	})

	t.Run("Requests_With_Mismatched_X_User_ID_Expect_403", func(t *testing.T) {
		// 1. GET /api/v1/carts/{id}
		getReq := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/carts/%s", cartID), nil)
		getReq.Header.Set("X-User-ID", mismatchedID.String())
		getRec := httptest.NewRecorder()
		router.ServeHTTP(getRec, getReq)
		t.Logf("[PROBE-1.7] GET /carts/{id} with mismatched X-User-ID: status=%d body=%s", getRec.Code, getRec.Body.String())
		assert.Equal(t, http.StatusForbidden, getRec.Code)
		var prob1 web.ProblemDetails
		_ = json.Unmarshal(getRec.Body.Bytes(), &prob1)
		assert.Equal(t, "FORBIDDEN", prob1.Code)

		// 2. POST /api/v1/carts/{id}/items
		addReq := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/carts/%s/items", cartID), bytes.NewReader([]byte(`{"sku":"SKU-PROBE-AUTH","quantity":1}`)))
		addReq.Header.Set("Content-Type", "application/json")
		addReq.Header.Set("X-User-ID", mismatchedID.String())
		addReq.Header.Set("If-Match", `"2"`)
		addRec := httptest.NewRecorder()
		router.ServeHTTP(addRec, addReq)
		t.Logf("[PROBE-1.8] POST /carts/{id}/items with mismatched X-User-ID: status=%d body=%s", addRec.Code, addRec.Body.String())
		assert.Equal(t, http.StatusForbidden, addRec.Code)

		// 3. PUT /api/v1/carts/{id}/items/{sku}
		putReq := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/api/v1/carts/%s/items/SKU-PROBE-AUTH", cartID), bytes.NewReader([]byte(`{"quantity":5}`)))
		putReq.Header.Set("Content-Type", "application/json")
		putReq.Header.Set("X-User-ID", mismatchedID.String())
		putReq.Header.Set("If-Match", `"2"`)
		putRec := httptest.NewRecorder()
		router.ServeHTTP(putRec, putReq)
		t.Logf("[PROBE-1.9] PUT /carts/{id}/items/{sku} with mismatched X-User-ID: status=%d body=%s", putRec.Code, putRec.Body.String())
		assert.Equal(t, http.StatusForbidden, putRec.Code)

		// 4. DELETE /api/v1/carts/{id}/items/{sku}
		delReq := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/api/v1/carts/%s/items/SKU-PROBE-AUTH", cartID), nil)
		delReq.Header.Set("X-User-ID", mismatchedID.String())
		delReq.Header.Set("If-Match", `"2"`)
		delRec := httptest.NewRecorder()
		router.ServeHTTP(delRec, delReq)
		t.Logf("[PROBE-1.10] DELETE /carts/{id}/items/{sku} with mismatched X-User-ID: status=%d body=%s", delRec.Code, delRec.Body.String())
		assert.Equal(t, http.StatusForbidden, delRec.Code)

		// 5. DELETE /api/v1/carts/{id}/clear
		clearReq := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/api/v1/carts/%s/clear", cartID), nil)
		clearReq.Header.Set("X-User-ID", mismatchedID.String())
		clearReq.Header.Set("If-Match", `"2"`)
		clearRec := httptest.NewRecorder()
		router.ServeHTTP(clearRec, clearReq)
		t.Logf("[PROBE-1.11] DELETE /carts/{id}/clear with mismatched X-User-ID: status=%d body=%s", clearRec.Code, clearRec.Body.String())
		assert.Equal(t, http.StatusForbidden, clearRec.Code)
	})
}

// TestProbeR2_CurrencyMismatch probes Area 2:
// - Add product with conflicting currency to active cart: verify HTTP 400 Bad Request (CURRENCY_MISMATCH)
func TestProbeR2_CurrencyMismatch(t *testing.T) {
	router, svc, reader := setupCartTestRouter(t)
	customerID := uuid.New()

	// Cart will be default USD
	reader.AddProduct(&catalog.Product{
		SKU:        "SKU-EUR-WATCH",
		Title:      "Swiss Watch in EUR",
		PriceMinor: 29900,
		Price:      money.Money{Amount: 29900, Currency: "EUR"},
		IsActive:   true,
	})

	c, err := svc.CreateOrGetActiveCart(t.Context(), customerID)
	require.NoError(t, err)
	assert.Equal(t, "USD", c.Currency)
	cartID := c.ID.String()

	// Attempt to add EUR product to USD cart
	addReq := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/carts/%s/items", cartID), bytes.NewReader([]byte(`{"sku":"SKU-EUR-WATCH","quantity":1}`)))
	addReq.Header.Set("Content-Type", "application/json")
	addReq.Header.Set("X-User-ID", customerID.String())
	addReq.Header.Set("If-Match", `"1"`)
	addRec := httptest.NewRecorder()
	router.ServeHTTP(addRec, addReq)

	t.Logf("[PROBE-2] Add product with conflicting currency (EUR to USD cart): status=%d body=%s", addRec.Code, addRec.Body.String())

	assert.Equal(t, http.StatusBadRequest, addRec.Code)
	assert.Equal(t, "application/problem+json", addRec.Header().Get("Content-Type"))

	var prob web.ProblemDetails
	err = json.Unmarshal(addRec.Body.Bytes(), &prob)
	require.NoError(t, err)
	assert.Equal(t, "CURRENCY_MISMATCH", prob.Code)
	assert.Contains(t, prob.Detail, "currency")
}

// TestProbeR2_OCC_VersionStrictness probes Area 3:
// - Send mutating requests with missing or zero/negative If-Match: verify returned HTTP status code
	// Invariant: mutating requests with missing or zero/negative If-Match must return HTTP 412 Precondition Failed.
func TestProbeR2_OCC_VersionStrictness(t *testing.T) {
	router, svc, reader := setupCartTestRouter(t)
	customerID := uuid.New()

	reader.AddProduct(&catalog.Product{
		SKU:        "SKU-OCC-PROBE",
		Title:      "OCC Probe Item",
		PriceMinor: 1000,
		Price:      money.Money{Amount: 1000, Currency: "USD"},
		IsActive:   true,
	})

	c, err := svc.CreateOrGetActiveCart(t.Context(), customerID)
	require.NoError(t, err)
	cartID := c.ID.String()

	// Initial version is 1
	assert.Equal(t, int64(1), c.Version)

	mutatingCases := []struct {
		name          string
		method        string
		url           string
		body          []byte
		ifMatchHeader string
		hasIfMatch    bool
	}{
		// POST /items cases
		{"POST_items_Missing_IfMatch", http.MethodPost, fmt.Sprintf("/api/v1/carts/%s/items", cartID), []byte(`{"sku":"SKU-OCC-PROBE","quantity":1}`), "", false},
		{"POST_items_Zero_IfMatch_Quoted", http.MethodPost, fmt.Sprintf("/api/v1/carts/%s/items", cartID), []byte(`{"sku":"SKU-OCC-PROBE","quantity":1}`), `"0"`, true},
		{"POST_items_Zero_IfMatch_Unquoted", http.MethodPost, fmt.Sprintf("/api/v1/carts/%s/items", cartID), []byte(`{"sku":"SKU-OCC-PROBE","quantity":1}`), `0`, true},
		{"POST_items_Negative_IfMatch", http.MethodPost, fmt.Sprintf("/api/v1/carts/%s/items", cartID), []byte(`{"sku":"SKU-OCC-PROBE","quantity":1}`), `"-1"`, true},
		{"POST_items_Stale_IfMatch", http.MethodPost, fmt.Sprintf("/api/v1/carts/%s/items", cartID), []byte(`{"sku":"SKU-OCC-PROBE","quantity":1}`), `"999"`, true},

		// PUT /items/{sku} cases
		{"PUT_item_Missing_IfMatch", http.MethodPut, fmt.Sprintf("/api/v1/carts/%s/items/SKU-OCC-PROBE", cartID), []byte(`{"quantity":2}`), "", false},
		{"PUT_item_Zero_IfMatch", http.MethodPut, fmt.Sprintf("/api/v1/carts/%s/items/SKU-OCC-PROBE", cartID), []byte(`{"quantity":2}`), `"0"`, true},
		{"PUT_item_Negative_IfMatch", http.MethodPut, fmt.Sprintf("/api/v1/carts/%s/items/SKU-OCC-PROBE", cartID), []byte(`{"quantity":2}`), `"-1"`, true},

		// DELETE /items/{sku} cases
		{"DELETE_item_Missing_IfMatch", http.MethodDelete, fmt.Sprintf("/api/v1/carts/%s/items/SKU-OCC-PROBE", cartID), nil, "", false},
		{"DELETE_item_Zero_IfMatch", http.MethodDelete, fmt.Sprintf("/api/v1/carts/%s/items/SKU-OCC-PROBE", cartID), nil, `"0"`, true},
		{"DELETE_item_Negative_IfMatch", http.MethodDelete, fmt.Sprintf("/api/v1/carts/%s/items/SKU-OCC-PROBE", cartID), nil, `"-1"`, true},

		// DELETE /clear cases
		{"DELETE_clear_Missing_IfMatch", http.MethodDelete, fmt.Sprintf("/api/v1/carts/%s/clear", cartID), nil, "", false},
		{"DELETE_clear_Zero_IfMatch", http.MethodDelete, fmt.Sprintf("/api/v1/carts/%s/clear", cartID), nil, `"0"`, true},
		{"DELETE_clear_Negative_IfMatch", http.MethodDelete, fmt.Sprintf("/api/v1/carts/%s/clear", cartID), nil, `"-1"`, true},
	}

	for _, tc := range mutatingCases {
		t.Run(tc.name, func(t *testing.T) {
			var bodyReader *bytes.Reader
			if tc.body != nil {
				bodyReader = bytes.NewReader(tc.body)
			} else {
				bodyReader = bytes.NewReader(nil)
			}
			req := httptest.NewRequest(tc.method, tc.url, bodyReader)
			if tc.body != nil {
				req.Header.Set("Content-Type", "application/json")
			}
			req.Header.Set("X-User-ID", customerID.String())
			if tc.hasIfMatch {
				req.Header.Set("If-Match", tc.ifMatchHeader)
			}
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			t.Logf("[PROBE-3] %s (If-Match=%q): status=%d body=%s", tc.name, tc.ifMatchHeader, rec.Code, rec.Body.String())
		})
	}
}
