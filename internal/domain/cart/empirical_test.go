package cart_test

import (
	"bytes"
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

// Empirical Test Suite 1: Cart HTTP Routes & OpenAPI 3.1 Contract Verification
func TestEmpiricalCart_OpenAPIContract(t *testing.T) {
	router, svc, reader := setupCartTestRouter(t)
	ownerID := uuid.New()

	reader.AddProduct(&catalog.Product{
		SKU:        "SKU-SHIRT-BLUE",
		Title:      "Blue Casual Shirt",
		PriceMinor: 2500,
		Price:      money.Money{Amount: 2500, Currency: "USD"},
		IsActive:   true,
	})

	// 1. POST /api/v1/carts - Create Cart
	createBody := []byte(fmt.Sprintf(`{"customer_id":"%s"}`, ownerID.String()))
	reqCreate := httptest.NewRequest(http.MethodPost, "/api/v1/carts", bytes.NewReader(createBody))
	reqCreate.Header.Set("Content-Type", "application/json")
	recCreate := httptest.NewRecorder()
	router.ServeHTTP(recCreate, reqCreate)

	require.Equal(t, http.StatusOK, recCreate.Code, "Expected HTTP 200 for POST /carts")
	assert.Equal(t, `"1"`, recCreate.Header().Get("ETag"))

	var c cart.Cart
	err := json.Unmarshal(recCreate.Body.Bytes(), &c)
	require.NoError(t, err)
	assert.Equal(t, ownerID, c.UserID)
	assert.Equal(t, int64(1), c.Version)
	assert.Equal(t, cart.CartStatusActive, c.Status)

	cartID := c.ID.String()

	// 2. GET /api/v1/carts/{cart_id} - Read Cart
	getReq := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/carts/%s", cartID), nil)
	getReq.Header.Set("X-User-ID", ownerID.String())
	getRec := httptest.NewRecorder()
	router.ServeHTTP(getRec, getReq)

	require.Equal(t, http.StatusOK, getRec.Code)
	assert.Equal(t, `"1"`, getRec.Header().Get("ETag"))

	// 3. POST /api/v1/carts/{cart_id}/items - Add Item
	addBody := []byte(`{"sku":"SKU-SHIRT-BLUE","quantity":2}`)
	addReq := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/carts/%s/items", cartID), bytes.NewReader(addBody))
	addReq.Header.Set("Content-Type", "application/json")
	addReq.Header.Set("X-User-ID", ownerID.String())
	addReq.Header.Set("If-Match", `"1"`)
	addRec := httptest.NewRecorder()
	router.ServeHTTP(addRec, addReq)

	require.Equal(t, http.StatusOK, addRec.Code)
	assert.Equal(t, `"2"`, addRec.Header().Get("ETag"))
	var updated cart.Cart
	err = json.Unmarshal(addRec.Body.Bytes(), &updated)
	require.NoError(t, err)
	assert.Equal(t, int64(2), updated.Version)
	require.Len(t, updated.Items, 1)
	assert.Equal(t, int64(5000), updated.TotalAmount.Amount)

	// 4. PUT /api/v1/carts/{cart_id}/items/{sku} - Update Quantity
	putBody := []byte(`{"quantity":4}`)
	putReq := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/api/v1/carts/%s/items/SKU-SHIRT-BLUE", cartID), bytes.NewReader(putBody))
	putReq.Header.Set("Content-Type", "application/json")
	putReq.Header.Set("X-User-ID", ownerID.String())
	putReq.Header.Set("If-Match", `"2"`)
	putRec := httptest.NewRecorder()
	router.ServeHTTP(putRec, putReq)

	require.Equal(t, http.StatusOK, putRec.Code)
	assert.Equal(t, `"3"`, putRec.Header().Get("ETag"))

	// 5. DELETE /api/v1/carts/{cart_id}/items/{sku} - Remove Item
	delReq := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/api/v1/carts/%s/items/SKU-SHIRT-BLUE", cartID), nil)
	delReq.Header.Set("X-User-ID", ownerID.String())
	delReq.Header.Set("If-Match", `"3"`)
	delRec := httptest.NewRecorder()
	router.ServeHTTP(delRec, delReq)

	require.Equal(t, http.StatusOK, delRec.Code)
	assert.Equal(t, `"4"`, delRec.Header().Get("ETag"))

	// 6. DELETE /api/v1/carts/{cart_id}/clear - Clear Cart
	// Add an item first so clear has something to clear
	_, err = svc.AddItem(t.Context(), c.ID, ownerID, "SKU-SHIRT-BLUE", 1, 4)
	require.NoError(t, err)

	clearReq := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/api/v1/carts/%s/clear", cartID), nil)
	clearReq.Header.Set("X-User-ID", ownerID.String())
	clearReq.Header.Set("If-Match", `"5"`)
	clearRec := httptest.NewRecorder()
	router.ServeHTTP(clearRec, clearReq)

	require.Equal(t, http.StatusNoContent, clearRec.Code, "Expected HTTP 204 No Content for DELETE /clear")
}

// Empirical Test Suite 2: Cart Optimistic Concurrency Control (OCC) Edge Cases
func TestEmpiricalCart_OCC_EdgeCases(t *testing.T) {
	router, svc, reader := setupCartTestRouter(t)
	ownerID := uuid.New()

	reader.AddProduct(&catalog.Product{
		SKU:        "SKU-OCC-ITEM",
		Title:      "OCC Item",
		PriceMinor: 1000,
		Price:      money.Money{Amount: 1000, Currency: "USD"},
		IsActive:   true,
	})

	c, err := svc.CreateOrGetActiveCart(t.Context(), ownerID)
	require.NoError(t, err)
	cartID := c.ID.String()

	// Case 1: AddItem with Stale If-Match -> 412 Precondition Failed
	body := []byte(`{"sku":"SKU-OCC-ITEM","quantity":1}`)
	reqStale := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/carts/%s/items", cartID), bytes.NewReader(body))
	reqStale.Header.Set("Content-Type", "application/json")
	reqStale.Header.Set("X-User-ID", ownerID.String())
	reqStale.Header.Set("If-Match", `"999"`) // Stale!
	recStale := httptest.NewRecorder()
	router.ServeHTTP(recStale, reqStale)

	assert.Equal(t, http.StatusPreconditionFailed, recStale.Code)
	assert.Equal(t, "application/problem+json", recStale.Header().Get("Content-Type"))
	var prob web.ProblemDetails
	err = json.Unmarshal(recStale.Body.Bytes(), &prob)
	require.NoError(t, err)
	assert.Equal(t, "PRECONDITION_FAILED", prob.Code)

	// Case 2: AddItem with Missing If-Match -> 400 Bad Request
	reqMissing := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/carts/%s/items", cartID), bytes.NewReader(body))
	reqMissing.Header.Set("Content-Type", "application/json")
	reqMissing.Header.Set("X-User-ID", ownerID.String())
	recMissing := httptest.NewRecorder()
	router.ServeHTTP(recMissing, reqMissing)

	assert.Equal(t, http.StatusBadRequest, recMissing.Code)
	assert.Equal(t, "application/problem+json", recMissing.Header().Get("Content-Type"))

	// Case 3: AddItem with Matching If-Match "1" -> 200 OK, version becomes 2
	reqOK := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/carts/%s/items", cartID), bytes.NewReader(body))
	reqOK.Header.Set("Content-Type", "application/json")
	reqOK.Header.Set("X-User-ID", ownerID.String())
	reqOK.Header.Set("If-Match", `"1"`)
	recOK := httptest.NewRecorder()
	router.ServeHTTP(recOK, reqOK)

	require.Equal(t, http.StatusOK, recOK.Code)
	assert.Equal(t, `"2"`, recOK.Header().Get("ETag"))

	// Case 4: UpdateQuantity with Stale If-Match "1" (current is 2) -> 412 Precondition Failed
	updateBody := []byte(`{"quantity":5}`)
	reqUpdateStale := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/api/v1/carts/%s/items/SKU-OCC-ITEM", cartID), bytes.NewReader(updateBody))
	reqUpdateStale.Header.Set("Content-Type", "application/json")
	reqUpdateStale.Header.Set("X-User-ID", ownerID.String())
	reqUpdateStale.Header.Set("If-Match", `"1"`) // Stale!
	recUpdateStale := httptest.NewRecorder()
	router.ServeHTTP(recUpdateStale, reqUpdateStale)

	assert.Equal(t, http.StatusPreconditionFailed, recUpdateStale.Code)

	// Case 5: RemoveItem with Stale If-Match "1" (current is 2) -> 412 Precondition Failed
	reqRemoveStale := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/api/v1/carts/%s/items/SKU-OCC-ITEM", cartID), nil)
	reqRemoveStale.Header.Set("X-User-ID", ownerID.String())
	reqRemoveStale.Header.Set("If-Match", `"1"`) // Stale!
	recRemoveStale := httptest.NewRecorder()
	router.ServeHTTP(recRemoveStale, reqRemoveStale)

	assert.Equal(t, http.StatusPreconditionFailed, recRemoveStale.Code)

	// Case 6: ClearCart with Stale If-Match "1" (current is 2) -> 412 Precondition Failed
	reqClearStale := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/api/v1/carts/%s/clear", cartID), nil)
	reqClearStale.Header.Set("X-User-ID", ownerID.String())
	reqClearStale.Header.Set("If-Match", `"1"`) // Stale!
	recClearStale := httptest.NewRecorder()
	router.ServeHTTP(recClearStale, reqClearStale)

	assert.Equal(t, http.StatusPreconditionFailed, recClearStale.Code)
}

// Empirical Test Suite 3: Customer Ownership Authorization Verification
// Checks both mismatched customer ID AND missing authorization.
func TestEmpiricalCart_CustomerOwnership_MismatchedAndMissingAuth(t *testing.T) {
	router, svc, reader := setupCartTestRouter(t)
	ownerID := uuid.New()
	attackerID := uuid.New()

	reader.AddProduct(&catalog.Product{
		SKU:        "SKU-AUTH-TEST",
		Title:      "Auth Test Product",
		PriceMinor: 1000,
		Price:      money.Money{Amount: 1000, Currency: "USD"},
		IsActive:   true,
	})

	c, err := svc.CreateOrGetActiveCart(t.Context(), ownerID)
	require.NoError(t, err)
	cartID := c.ID.String()

	c, err = svc.AddItem(t.Context(), c.ID, ownerID, "SKU-AUTH-TEST", 2, 1)
	require.NoError(t, err)
	assert.Equal(t, int64(2), c.Version)

	// -------------------------------------------------------------
	// PART A: Mismatched Customer ID (Attacker provides their own X-User-ID)
	// MUST FAIL with HTTP 403 Forbidden
	// -------------------------------------------------------------
	t.Run("Mismatched_CustomerID_Rejection", func(t *testing.T) {
		// 1. GET cart with mismatched ID -> 403
		getReq := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/carts/%s", cartID), nil)
		getReq.Header.Set("X-User-ID", attackerID.String())
		getRec := httptest.NewRecorder()
		router.ServeHTTP(getRec, getReq)
		assert.Equal(t, http.StatusForbidden, getRec.Code, "Expected 403 Forbidden for mismatched customer GET")

		// 2. AddItem with mismatched ID -> 403
		addBody := []byte(`{"sku":"SKU-AUTH-TEST","quantity":1}`)
		addReq := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/carts/%s/items", cartID), bytes.NewReader(addBody))
		addReq.Header.Set("Content-Type", "application/json")
		addReq.Header.Set("X-User-ID", attackerID.String())
		addReq.Header.Set("If-Match", `"2"`)
		addRec := httptest.NewRecorder()
		router.ServeHTTP(addRec, addReq)
		assert.Equal(t, http.StatusForbidden, addRec.Code, "Expected 403 Forbidden for mismatched customer AddItem")

		// 3. UpdateQuantity with mismatched ID -> 403
		upBody := []byte(`{"quantity":5}`)
		upReq := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/api/v1/carts/%s/items/SKU-AUTH-TEST", cartID), bytes.NewReader(upBody))
		upReq.Header.Set("Content-Type", "application/json")
		upReq.Header.Set("X-User-ID", attackerID.String())
		upReq.Header.Set("If-Match", `"2"`)
		upRec := httptest.NewRecorder()
		router.ServeHTTP(upRec, upReq)
		assert.Equal(t, http.StatusForbidden, upRec.Code, "Expected 403 Forbidden for mismatched customer UpdateQuantity")

		// 4. RemoveItem with mismatched ID -> 403
		delReq := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/api/v1/carts/%s/items/SKU-AUTH-TEST", cartID), nil)
		delReq.Header.Set("X-User-ID", attackerID.String())
		delReq.Header.Set("If-Match", `"2"`)
		delRec := httptest.NewRecorder()
		router.ServeHTTP(delRec, delReq)
		assert.Equal(t, http.StatusForbidden, delRec.Code, "Expected 403 Forbidden for mismatched customer RemoveItem")

		// 5. ClearCart with mismatched ID -> 403
		clearReq := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/api/v1/carts/%s/clear", cartID), nil)
		clearReq.Header.Set("X-User-ID", attackerID.String())
		clearReq.Header.Set("If-Match", `"2"`)
		clearRec := httptest.NewRecorder()
		router.ServeHTTP(clearRec, clearReq)
		assert.Equal(t, http.StatusForbidden, clearRec.Code, "Expected 403 Forbidden for mismatched customer ClearCart")
	})

	// -------------------------------------------------------------
	// PART B: Missing Authorization (Caller omits X-User-ID header)
	// MUST FAIL with HTTP 401 Unauthorized
	// -------------------------------------------------------------
	t.Run("Missing_Authorization_Behavior", func(t *testing.T) {
		// 1. GET /carts/{cart_id} without X-User-ID -> 401 Unauthorized
		getReq := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/carts/%s", cartID), nil)
		getRec := httptest.NewRecorder()
		router.ServeHTTP(getRec, getReq)

		assert.Equal(t, http.StatusUnauthorized, getRec.Code, "Expected 401 Unauthorized for GET without X-User-ID")
		assert.Equal(t, "application/problem+json", getRec.Header().Get("Content-Type"))

		// 2. AddItem without X-User-ID -> 401 Unauthorized
		addBody := []byte(`{"sku":"SKU-AUTH-TEST","quantity":1}`)
		addReq := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/carts/%s/items", cartID), bytes.NewReader(addBody))
		addReq.Header.Set("Content-Type", "application/json")
		addReq.Header.Set("If-Match", `"2"`)
		addRec := httptest.NewRecorder()
		router.ServeHTTP(addRec, addReq)

		assert.Equal(t, http.StatusUnauthorized, addRec.Code, "Expected 401 Unauthorized for POST /items without X-User-ID")
		assert.Equal(t, "application/problem+json", addRec.Header().Get("Content-Type"))

		// 3. UpdateQuantity without X-User-ID -> 401 Unauthorized
		upBody := []byte(`{"quantity":10}`)
		upReq := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/api/v1/carts/%s/items/SKU-AUTH-TEST", cartID), bytes.NewReader(upBody))
		upReq.Header.Set("Content-Type", "application/json")
		upReq.Header.Set("If-Match", `"2"`)
		upRec := httptest.NewRecorder()
		router.ServeHTTP(upRec, upReq)

		assert.Equal(t, http.StatusUnauthorized, upRec.Code, "Expected 401 Unauthorized for PUT /items without X-User-ID")
		assert.Equal(t, "application/problem+json", upRec.Header().Get("Content-Type"))

		// 4. RemoveItem without X-User-ID -> 401 Unauthorized
		delReq := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/api/v1/carts/%s/items/SKU-AUTH-TEST", cartID), nil)
		delReq.Header.Set("If-Match", `"2"`)
		delRec := httptest.NewRecorder()
		router.ServeHTTP(delRec, delReq)

		assert.Equal(t, http.StatusUnauthorized, delRec.Code, "Expected 401 Unauthorized for DELETE /items without X-User-ID")
		assert.Equal(t, "application/problem+json", delRec.Header().Get("Content-Type"))

		// 5. ClearCart without X-User-ID -> 401 Unauthorized
		clearReq := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/api/v1/carts/%s/clear", cartID), nil)
		clearReq.Header.Set("If-Match", `"2"`)
		clearRec := httptest.NewRecorder()
		router.ServeHTTP(clearRec, clearReq)

		assert.Equal(t, http.StatusUnauthorized, clearRec.Code, "Expected 401 Unauthorized for DELETE /clear without X-User-ID")
		assert.Equal(t, "application/problem+json", clearRec.Header().Get("Content-Type"))
	})
}

// Empirical Test Suite 4: Money Arithmetic Invariants - Fractional/Float Rejection in Cart
func TestEmpiricalCart_FloatRejection(t *testing.T) {
	router, svc, reader := setupCartTestRouter(t)
	ownerID := uuid.New()

	reader.AddProduct(&catalog.Product{
		SKU:        "SKU-FLOAT-CART",
		Title:      "Float Cart Item",
		PriceMinor: 2000,
		Price:      money.Money{Amount: 2000, Currency: "USD"},
		IsActive:   true,
	})

	c, err := svc.CreateOrGetActiveCart(t.Context(), ownerID)
	require.NoError(t, err)
	cartID := c.ID.String()

	// Test 4.1: POST /carts/{cart_id}/items with fractional quantity: {"sku":"...","quantity": 1.5}
	floatQtyBody := []byte(`{"sku":"SKU-FLOAT-CART","quantity": 1.5}`)
	reqFloat := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/carts/%s/items", cartID), bytes.NewReader(floatQtyBody))
	reqFloat.Header.Set("Content-Type", "application/json")
	reqFloat.Header.Set("X-User-ID", ownerID.String())
	reqFloat.Header.Set("If-Match", `"1"`)
	recFloat := httptest.NewRecorder()
	router.ServeHTTP(recFloat, reqFloat)

	assert.Equal(t, http.StatusBadRequest, recFloat.Code, "Expected HTTP 400 Bad Request on fractional quantity")
	assert.Equal(t, "application/problem+json", recFloat.Header().Get("Content-Type"))
	var prob web.ProblemDetails
	err = json.Unmarshal(recFloat.Body.Bytes(), &prob)
	require.NoError(t, err)
	assert.Equal(t, "INVALID_REQUEST_BODY", prob.Code)
	assert.Contains(t, prob.Detail, "cannot unmarshal number")

	// Test 4.2: PUT /carts/{cart_id}/items/{sku} with fractional quantity: {"quantity": 3.75}
	// First add valid item
	c, err = svc.AddItem(t.Context(), c.ID, ownerID, "SKU-FLOAT-CART", 1, 1)
	require.NoError(t, err)

	floatUpdateBody := []byte(`{"quantity": 3.75}`)
	reqUpdateFloat := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/api/v1/carts/%s/items/SKU-FLOAT-CART", cartID), bytes.NewReader(floatUpdateBody))
	reqUpdateFloat.Header.Set("Content-Type", "application/json")
	reqUpdateFloat.Header.Set("X-User-ID", ownerID.String())
	reqUpdateFloat.Header.Set("If-Match", `"2"`)
	recUpdateFloat := httptest.NewRecorder()
	router.ServeHTTP(recUpdateFloat, reqUpdateFloat)

	assert.Equal(t, http.StatusBadRequest, recUpdateFloat.Code, "Expected HTTP 400 Bad Request on fractional update quantity")
	assert.Equal(t, "application/problem+json", recUpdateFloat.Header().Get("Content-Type"))

	// Test 4.3: POST /carts/{cart_id}/items with unknown float money field injected
	injectedBody := []byte(`{"sku":"SKU-FLOAT-CART","quantity": 2, "unit_price": 19.99}`)
	reqInjected := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/carts/%s/items", cartID), bytes.NewReader(injectedBody))
	reqInjected.Header.Set("Content-Type", "application/json")
	reqInjected.Header.Set("X-User-ID", ownerID.String())
	reqInjected.Header.Set("If-Match", `"2"`)
	recInjected := httptest.NewRecorder()
	router.ServeHTTP(recInjected, reqInjected)

	assert.Equal(t, http.StatusBadRequest, recInjected.Code, "Expected HTTP 400 Bad Request on unknown field injection due to DisallowUnknownFields")
	assert.Equal(t, "application/problem+json", recInjected.Header().Get("Content-Type"))

	// Test 4.4: Zero or Negative Quantity -> 400 Bad Request
	negQtyBody := []byte(`{"sku":"SKU-FLOAT-CART","quantity": -1}`)
	reqNeg := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/carts/%s/items", cartID), bytes.NewReader(negQtyBody))
	reqNeg.Header.Set("Content-Type", "application/json")
	reqNeg.Header.Set("X-User-ID", ownerID.String())
	reqNeg.Header.Set("If-Match", `"2"`)
	recNeg := httptest.NewRecorder()
	router.ServeHTTP(recNeg, reqNeg)

	assert.Equal(t, http.StatusBadRequest, recNeg.Code)
}

// Empirical Test Suite 5: Concurrent Updates Stress Harness (Cart OCC)
func TestEmpiricalCart_ConcurrentOCC_StressHarness(t *testing.T) {
	router, svc, reader := setupCartTestRouter(t)
	ownerID := uuid.New()

	reader.AddProduct(&catalog.Product{
		SKU:        "SKU-CONC-CART",
		Title:      "Concurrent Cart Item",
		PriceMinor: 1000,
		Price:      money.Money{Amount: 1000, Currency: "USD"},
		IsActive:   true,
	})

	c, err := svc.CreateOrGetActiveCart(t.Context(), ownerID)
	require.NoError(t, err)

	cartID := c.ID.String()
	concurrency := 20
	var wg sync.WaitGroup
	wg.Add(concurrency)

	var successCount int
	var conflictCount int
	var mu sync.Mutex

	for i := 0; i < concurrency; i++ {
		qty := i + 1
		go func() {
			defer wg.Done()
			body := []byte(fmt.Sprintf(`{"sku":"SKU-CONC-CART","quantity":%d}`, qty))
			req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/carts/%s/items", cartID), bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-User-ID", ownerID.String())
			req.Header.Set("If-Match", `"1"`) // All 20 goroutines compete with the exact same initial version 1!
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			mu.Lock()
			defer mu.Unlock()
			if rec.Code == http.StatusOK {
				successCount++
			} else if rec.Code == http.StatusPreconditionFailed {
				conflictCount++
			}
		}()
	}

	wg.Wait()

	// Exactly 1 concurrent request with If-Match "1" must win and commit; all other 19 must fail with 412 Precondition Failed.
	assert.Equal(t, 1, successCount, "Exactly one concurrent AddItem with If-Match '1' must succeed")
	assert.Equal(t, concurrency-1, conflictCount, "All other concurrent AddItems must fail with 412 Precondition Failed")
}
