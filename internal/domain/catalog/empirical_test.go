package catalog_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"shopflow/internal/domain/catalog"
	"shopflow/internal/domain/money"
	"shopflow/internal/platform/web"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Empirical Test Suite 1: Catalog HTTP Routes & OpenAPI 3.1 Contract Verification
func TestEmpiricalCatalog_OpenAPIContract(t *testing.T) {
	router, _ := setupCatalogTestRouter(t)

	// 1. POST /api/v1/products - Valid Creation
	validBody := []byte(`{
		"sku": "SKU-EMP-001",
		"title": "Empirical Test Keyboard",
		"description": "Ergonomic mechanical keyboard",
		"price": {
			"amount": 14900,
			"currency": "USD"
		}
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/products", bytes.NewReader(validBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code, "Expected HTTP 201 for POST /products")
	assert.Equal(t, `"1"`, rec.Header().Get("ETag"), "Expected initial ETag '1'")
	assert.Contains(t, rec.Header().Get("Location"), "/api/v1/products/", "Expected Location header with new resource URI")

	var prod catalog.Product
	err := json.Unmarshal(rec.Body.Bytes(), &prod)
	require.NoError(t, err)
	assert.Equal(t, "SKU-EMP-001", prod.SKU)
	assert.Equal(t, int64(14900), prod.Price.Amount)
	assert.Equal(t, "USD", prod.Price.Currency)
	assert.Equal(t, int64(1), prod.Version)

	// 2. GET /api/v1/products/{id} - Existing Product
	getReq := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/products/%s", prod.ID.String()), nil)
	getRec := httptest.NewRecorder()
	router.ServeHTTP(getRec, getReq)

	require.Equal(t, http.StatusOK, getRec.Code, "Expected HTTP 200 for GET /products/{id}")
	assert.Equal(t, `"1"`, getRec.Header().Get("ETag"))

	// 3. GET /api/v1/products - List Products
	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/products?limit=10", nil)
	listRec := httptest.NewRecorder()
	router.ServeHTTP(listRec, listReq)

	require.Equal(t, http.StatusOK, listRec.Code, "Expected HTTP 200 for GET /products")
	var listResp catalog.ProductListResponse
	err = json.Unmarshal(listRec.Body.Bytes(), &listResp)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(listResp.Items), 1)

	// 4. GET /api/v1/products/{id} - Non-existent UUID -> 404
	missingID := uuid.New()
	missingReq := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/products/%s", missingID.String()), nil)
	missingRec := httptest.NewRecorder()
	router.ServeHTTP(missingRec, missingReq)

	assert.Equal(t, http.StatusNotFound, missingRec.Code)
	assert.Equal(t, "application/problem+json", missingRec.Header().Get("Content-Type"))

	// 5. GET /api/v1/products/{id} - Malformed UUID -> 400
	badReq := httptest.NewRequest(http.MethodGet, "/api/v1/products/invalid-uuid", nil)
	badRec := httptest.NewRecorder()
	router.ServeHTTP(badRec, badReq)

	assert.Equal(t, http.StatusBadRequest, badRec.Code)
	assert.Equal(t, "application/problem+json", badRec.Header().Get("Content-Type"))
}

// Empirical Test Suite 2: Catalog Optimistic Concurrency Control (OCC) Edge Cases
func TestEmpiricalCatalog_OCC_EdgeCases(t *testing.T) {
	router, svc := setupCatalogTestRouter(t)

	prod, err := svc.CreateProduct(t.Context(), catalog.CreateProductRequest{
		SKU:   "SKU-OCC-TEST",
		Title: "OCC Test Product",
		Price: money.Money{Amount: 5000, Currency: "USD"},
	})
	require.NoError(t, err)
	url := fmt.Sprintf("/api/v1/products/%s", prod.ID.String())

	// Case 1: Missing If-Match Header on PUT -> 400 Precondition Required
	putBody := []byte(`{"title":"Updated Title","description":"Updated Desc","is_active":true}`)
	reqNoIfMatch := httptest.NewRequest(http.MethodPut, url, bytes.NewReader(putBody))
	reqNoIfMatch.Header.Set("Content-Type", "application/json")
	recNoIfMatch := httptest.NewRecorder()
	router.ServeHTTP(recNoIfMatch, reqNoIfMatch)

	assert.Equal(t, http.StatusBadRequest, recNoIfMatch.Code)
	assert.Equal(t, "application/problem+json", recNoIfMatch.Header().Get("Content-Type"))
	var prob web.ProblemDetails
	err = json.Unmarshal(recNoIfMatch.Body.Bytes(), &prob)
	require.NoError(t, err)
	assert.Equal(t, "PRECONDITION_REQUIRED", prob.Code)

	// Case 2: Invalid If-Match Header (non-numeric) -> 400 Bad Request
	reqInvalidIfMatch := httptest.NewRequest(http.MethodPut, url, bytes.NewReader(putBody))
	reqInvalidIfMatch.Header.Set("Content-Type", "application/json")
	reqInvalidIfMatch.Header.Set("If-Match", `"invalid"`)
	recInvalidIfMatch := httptest.NewRecorder()
	router.ServeHTTP(recInvalidIfMatch, reqInvalidIfMatch)

	assert.Equal(t, http.StatusBadRequest, recInvalidIfMatch.Code)

	// Case 3: Invalid If-Match Header (zero or negative) -> 400 Bad Request
	reqZeroIfMatch := httptest.NewRequest(http.MethodPut, url, bytes.NewReader(putBody))
	reqZeroIfMatch.Header.Set("Content-Type", "application/json")
	reqZeroIfMatch.Header.Set("If-Match", `"0"`)
	recZeroIfMatch := httptest.NewRecorder()
	router.ServeHTTP(recZeroIfMatch, reqZeroIfMatch)

	assert.Equal(t, http.StatusBadRequest, recZeroIfMatch.Code)

	// Case 4: Mismatched / Stale If-Match Header -> 412 Precondition Failed
	reqStaleIfMatch := httptest.NewRequest(http.MethodPut, url, bytes.NewReader(putBody))
	reqStaleIfMatch.Header.Set("Content-Type", "application/json")
	reqStaleIfMatch.Header.Set("If-Match", `"999"`) // current version is 1
	recStaleIfMatch := httptest.NewRecorder()
	router.ServeHTTP(recStaleIfMatch, reqStaleIfMatch)

	assert.Equal(t, http.StatusPreconditionFailed, recStaleIfMatch.Code)
	assert.Equal(t, "application/problem+json", recStaleIfMatch.Header().Get("Content-Type"))
	var staleProb web.ProblemDetails
	err = json.Unmarshal(recStaleIfMatch.Body.Bytes(), &staleProb)
	require.NoError(t, err)
	assert.Equal(t, http.StatusPreconditionFailed, staleProb.Status)
	assert.Equal(t, "PRECONDITION_FAILED", staleProb.Code)

	// Case 5: Valid Matching If-Match Header -> 200 OK with ETag "2"
	reqValidIfMatch := httptest.NewRequest(http.MethodPut, url, bytes.NewReader(putBody))
	reqValidIfMatch.Header.Set("Content-Type", "application/json")
	reqValidIfMatch.Header.Set("If-Match", `"1"`)
	recValidIfMatch := httptest.NewRecorder()
	router.ServeHTTP(recValidIfMatch, reqValidIfMatch)

	require.Equal(t, http.StatusOK, recValidIfMatch.Code)
	assert.Equal(t, `"2"`, recValidIfMatch.Header().Get("ETag"))

	// Case 6: Replaying previous If-Match "1" now fails with 412 Precondition Failed
	reqReplay := httptest.NewRequest(http.MethodPut, url, bytes.NewReader(putBody))
	reqReplay.Header.Set("Content-Type", "application/json")
	reqReplay.Header.Set("If-Match", `"1"`)
	recReplay := httptest.NewRecorder()
	router.ServeHTTP(recReplay, reqReplay)

	assert.Equal(t, http.StatusPreconditionFailed, recReplay.Code)

	// Case 7: PATCH /products/{id}/price with Stale If-Match -> 412 Precondition Failed
	priceUrl := fmt.Sprintf("/api/v1/products/%s/price", prod.ID.String())
	priceBody := []byte(`{"price":{"amount":7500,"currency":"USD"}}`)
	reqPriceStale := httptest.NewRequest(http.MethodPatch, priceUrl, bytes.NewReader(priceBody))
	reqPriceStale.Header.Set("Content-Type", "application/json")
	reqPriceStale.Header.Set("If-Match", `"1"`) // Current is 2
	recPriceStale := httptest.NewRecorder()
	router.ServeHTTP(recPriceStale, reqPriceStale)

	assert.Equal(t, http.StatusPreconditionFailed, recPriceStale.Code)
	assert.Equal(t, "application/problem+json", recPriceStale.Header().Get("Content-Type"))

	// Case 8: PATCH /products/{id}/price with Valid Matching If-Match "2" -> 200 OK, ETag "3"
	reqPriceValid := httptest.NewRequest(http.MethodPatch, priceUrl, bytes.NewReader(priceBody))
	reqPriceValid.Header.Set("Content-Type", "application/json")
	reqPriceValid.Header.Set("If-Match", `"2"`)
	recPriceValid := httptest.NewRecorder()
	router.ServeHTTP(recPriceValid, reqPriceValid)

	require.Equal(t, http.StatusOK, recPriceValid.Code)
	assert.Equal(t, `"3"`, recPriceValid.Header().Get("ETag"))
}

// Empirical Test Suite 4: Money Arithmetic Invariants - Fractional/Float Rejection in Catalog
func TestEmpiricalCatalog_MoneyFloatRejection(t *testing.T) {
	router, svc := setupCatalogTestRouter(t)

	// Test 4.1: POST /api/v1/products with fractional float amount: {"amount": 19.99}
	floatBody := []byte(`{
		"sku": "SKU-FLOAT-PROD",
		"title": "Float Product",
		"price": {
			"amount": 19.99,
			"currency": "USD"
		}
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/products", bytes.NewReader(floatBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code, "Expected HTTP 400 Bad Request when fractional amount is supplied")
	assert.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))
	var prob web.ProblemDetails
	err := json.Unmarshal(rec.Body.Bytes(), &prob)
	require.NoError(t, err)
	assert.Equal(t, "INVALID_REQUEST_BODY", prob.Code)
	assert.Contains(t, prob.Detail, "cannot unmarshal number")

	// Test 4.2: POST /api/v1/products with float price directly: {"price": 19.99}
	flatFloatBody := []byte(`{
		"sku": "SKU-FLOAT-FLAT",
		"title": "Flat Float Product",
		"price": 19.99
	}`)
	reqFlat := httptest.NewRequest(http.MethodPost, "/api/v1/products", bytes.NewReader(flatFloatBody))
	reqFlat.Header.Set("Content-Type", "application/json")
	recFlat := httptest.NewRecorder()
	router.ServeHTTP(recFlat, reqFlat)

	assert.Equal(t, http.StatusBadRequest, recFlat.Code, "Expected HTTP 400 Bad Request when price is non-object float")
	assert.Equal(t, "application/problem+json", recFlat.Header().Get("Content-Type"))

	// Test 4.3: PATCH /api/v1/products/{id}/price with fractional float amount
	prod, err := svc.CreateProduct(t.Context(), catalog.CreateProductRequest{
		SKU:   "SKU-FLOAT-PATCH",
		Title: "Float Patch Product",
		Price: money.Money{Amount: 1000, Currency: "USD"},
	})
	require.NoError(t, err)

	patchFloatBody := []byte(`{"price":{"amount": 25.50, "currency": "USD"}}`)
	patchUrl := fmt.Sprintf("/api/v1/products/%s/price", prod.ID.String())
	reqPatch := httptest.NewRequest(http.MethodPatch, patchUrl, bytes.NewReader(patchFloatBody))
	reqPatch.Header.Set("Content-Type", "application/json")
	reqPatch.Header.Set("If-Match", `"1"`)
	recPatch := httptest.NewRecorder()
	router.ServeHTTP(recPatch, reqPatch)

	assert.Equal(t, http.StatusBadRequest, recPatch.Code, "Expected HTTP 400 Bad Request on PATCH with fractional amount")
	assert.Equal(t, "application/problem+json", recPatch.Header().Get("Content-Type"))

	// Test 4.4: POST /api/v1/products with negative price -> 400 Bad Request
	negBody := []byte(`{
		"sku": "SKU-NEG-PRICE",
		"title": "Negative Price Product",
		"price": {
			"amount": -500,
			"currency": "USD"
		}
	}`)
	reqNeg := httptest.NewRequest(http.MethodPost, "/api/v1/products", bytes.NewReader(negBody))
	reqNeg.Header.Set("Content-Type", "application/json")
	recNeg := httptest.NewRecorder()
	router.ServeHTTP(recNeg, reqNeg)

	assert.Equal(t, http.StatusBadRequest, recNeg.Code)
	var negProb web.ProblemDetails
	err = json.Unmarshal(recNeg.Body.Bytes(), &negProb)
	require.NoError(t, err)
	assert.Equal(t, "VALIDATION_FAILED", negProb.Code)
}

// Empirical Test Suite 5: Concurrent Updates Stress Harness (Catalog OCC)
func TestEmpiricalCatalog_ConcurrentOCC_StressHarness(t *testing.T) {
	router, svc := setupCatalogTestRouter(t)

	prod, err := svc.CreateProduct(t.Context(), catalog.CreateProductRequest{
		SKU:   "SKU-CONC-OCC",
		Title: "Concurrent OCC Product",
		Price: money.Money{Amount: 1000, Currency: "USD"},
	})
	require.NoError(t, err)

	url := fmt.Sprintf("/api/v1/products/%s", prod.ID.String())
	concurrency := 20
	var wg sync.WaitGroup
	wg.Add(concurrency)

	var successCount int
	var conflictCount int
	var mu sync.Mutex

	for i := 0; i < concurrency; i++ {
		workerID := i
		go func() {
			defer wg.Done()
			updateBody := []byte(fmt.Sprintf(`{"title":"Worker Update %d","is_active":true}`, workerID))
			req := httptest.NewRequest(http.MethodPut, url, bytes.NewReader(updateBody))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("If-Match", `"1"`) // All workers compete with the exact same initial version 1!
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

	// Exactly 1 worker must succeed, and all others must receive HTTP 412 Precondition Failed
	assert.Equal(t, 1, successCount, "Exactly one concurrent update with If-Match '1' must succeed")
	assert.Equal(t, concurrency-1, conflictCount, "All other concurrent updates must fail with 412 Precondition Failed")
}
