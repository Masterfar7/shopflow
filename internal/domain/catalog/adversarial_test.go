package catalog_test

import (
	"bytes"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"shopflow/internal/domain/catalog"
	"shopflow/internal/domain/money"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAdversarialCatalog_ConcurrentOCC_MatchingVersions verifies that when 50 concurrent
// workers attempt to update a catalog product with the exact same matching version ("1"),
// exactly ONE worker succeeds and all 49 other workers receive HTTP 412 Precondition Failed.
func TestAdversarialCatalog_ConcurrentOCC_MatchingVersions(t *testing.T) {
	router, svc := setupCatalogTestRouter(t)

	prod, err := svc.CreateProduct(t.Context(), catalog.CreateProductRequest{
		SKU:   "SKU-ADV-CAT-01",
		Title: "Initial Adversarial Product",
		Price: money.Money{Amount: 1000, Currency: "USD"},
	})
	require.NoError(t, err)
	assert.Equal(t, int64(1), prod.Version)

	url := fmt.Sprintf("/api/v1/products/%s", prod.ID.String())
	concurrency := 50
	var wg sync.WaitGroup
	wg.Add(concurrency)

	var mu sync.Mutex
	var successCount int
	var conflictCount int
	var otherStatusCodes []int

	for i := 0; i < concurrency; i++ {
		workerID := i
		go func() {
			defer wg.Done()
			updateBody := []byte(fmt.Sprintf(`{"title":"Updated by Worker %d","is_active":true}`, workerID))
			req := httptest.NewRequest(http.MethodPut, url, bytes.NewReader(updateBody))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("If-Match", `"1"`)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			mu.Lock()
			defer mu.Unlock()
			switch rec.Code {
			case http.StatusOK:
				successCount++
			case http.StatusPreconditionFailed:
				conflictCount++
			default:
				otherStatusCodes = append(otherStatusCodes, rec.Code)
			}
		}()
	}

	wg.Wait()

	assert.Equal(t, 1, successCount, "Exactly ONE concurrent update with If-Match '1' must succeed")
	assert.Equal(t, concurrency-1, conflictCount, "All other 49 concurrent updates must fail with 412 Precondition Failed")
	assert.Empty(t, otherStatusCodes, "No unexpected status codes should occur")

	// Final product state must be version 2
	finalProd, err := svc.GetProduct(t.Context(), prod.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(2), finalProd.Version)
}

// TestAdversarialCatalog_ConcurrentOCC_NonMatchingVersions verifies that when 50 concurrent
// workers attempt updates with non-matching versions (stale "0", negative "-1", future "99"),
// ZERO updates succeed and all 50 workers fail.
func TestAdversarialCatalog_ConcurrentOCC_NonMatchingVersions(t *testing.T) {
	router, svc := setupCatalogTestRouter(t)

	prod, err := svc.CreateProduct(t.Context(), catalog.CreateProductRequest{
		SKU:   "SKU-ADV-CAT-02",
		Title: "Non-Matching OCC Product",
		Price: money.Money{Amount: 1000, Currency: "USD"},
	})
	require.NoError(t, err)
	assert.Equal(t, int64(1), prod.Version)

	url := fmt.Sprintf("/api/v1/products/%s", prod.ID.String())
	concurrency := 50
	var wg sync.WaitGroup
	wg.Add(concurrency)

	var mu sync.Mutex
	var successCount int
	var failureCount int

	invalidVersions := []string{`"0"`, `"-1"`, `"99"`, `"100"`, `"5"`}

	for i := 0; i < concurrency; i++ {
		workerID := i
		versionHeader := invalidVersions[workerID%len(invalidVersions)]
		go func() {
			defer wg.Done()
			updateBody := []byte(fmt.Sprintf(`{"title":"Worker %d Invalid Version","is_active":true}`, workerID))
			req := httptest.NewRequest(http.MethodPut, url, bytes.NewReader(updateBody))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("If-Match", versionHeader)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			mu.Lock()
			defer mu.Unlock()
			if rec.Code == http.StatusOK {
				successCount++
			} else {
				failureCount++
			}
		}()
	}

	wg.Wait()

	assert.Equal(t, 0, successCount, "Zero updates must succeed when version is non-matching")
	assert.Equal(t, concurrency, failureCount, "All 50 updates must be rejected")

	// Version must remain untouched at 1
	finalProd, err := svc.GetProduct(t.Context(), prod.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), finalProd.Version)
}

// TestAdversarialCatalog_ConcurrentOCC_MixedMatchingAndNonMatching verifies that when
// 1 worker presents matching version "1" and 49 workers present invalid/stale versions,
// exactly 1 worker succeeds.
func TestAdversarialCatalog_ConcurrentOCC_MixedMatchingAndNonMatching(t *testing.T) {
	router, svc := setupCatalogTestRouter(t)

	prod, err := svc.CreateProduct(t.Context(), catalog.CreateProductRequest{
		SKU:   "SKU-ADV-CAT-03",
		Title: "Mixed OCC Product",
		Price: money.Money{Amount: 1000, Currency: "USD"},
	})
	require.NoError(t, err)

	url := fmt.Sprintf("/api/v1/products/%s", prod.ID.String())
	concurrency := 50
	var wg sync.WaitGroup
	wg.Add(concurrency)

	var mu sync.Mutex
	var successCount int
	var failureCount int

	for i := 0; i < concurrency; i++ {
		workerID := i
		var versionHeader string
		if workerID == 25 {
			versionHeader = `"1"` // The single lucky worker with matching version
		} else {
			// Versions that do not match initial (1) and cannot be reached (0, -1, 999)
			staleVersions := []string{`"0"`, `"-1"`, `"999"`, `"888"`, `"777"`}
			versionHeader = staleVersions[workerID%len(staleVersions)]
		}

		go func() {
			defer wg.Done()
			updateBody := []byte(fmt.Sprintf(`{"title":"Worker %d Mixed","is_active":true}`, workerID))
			req := httptest.NewRequest(http.MethodPut, url, bytes.NewReader(updateBody))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("If-Match", versionHeader)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			mu.Lock()
			defer mu.Unlock()
			if rec.Code == http.StatusOK {
				successCount++
			} else {
				failureCount++
			}
		}()
	}

	wg.Wait()

	assert.Equal(t, 1, successCount, "The single worker with matching version must succeed")
	assert.Equal(t, concurrency-1, failureCount, "All 49 invalid/stale workers must fail")
}

// TestAdversarialCatalog_ConcurrentTitleAndPriceUpdates verifies OCC across multiple
// mutating endpoints (PUT /products/{id} and PATCH /products/{id}/price).
func TestAdversarialCatalog_ConcurrentTitleAndPriceUpdates(t *testing.T) {
	router, svc := setupCatalogTestRouter(t)

	prod, err := svc.CreateProduct(t.Context(), catalog.CreateProductRequest{
		SKU:   "SKU-ADV-CAT-04",
		Title: "Dual Endpoint OCC Product",
		Price: money.Money{Amount: 2000, Currency: "USD"},
	})
	require.NoError(t, err)

	putURL := fmt.Sprintf("/api/v1/products/%s", prod.ID.String())
	patchURL := fmt.Sprintf("/api/v1/products/%s/price", prod.ID.String())

	concurrencyPerEndpoint := 25
	totalConcurrency := concurrencyPerEndpoint * 2
	var wg sync.WaitGroup
	wg.Add(totalConcurrency)

	var mu sync.Mutex
	var successCount int
	var conflictCount int

	// 25 Title updates competing on version 1
	for i := 0; i < concurrencyPerEndpoint; i++ {
		workerID := i
		go func() {
			defer wg.Done()
			body := []byte(fmt.Sprintf(`{"title":"Title Worker %d","is_active":true}`, workerID))
			req := httptest.NewRequest(http.MethodPut, putURL, bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("If-Match", `"1"`)
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

	// 25 Price updates competing on version 1
	for i := 0; i < concurrencyPerEndpoint; i++ {
		workerID := i
		go func() {
			defer wg.Done()
			body := []byte(fmt.Sprintf(`{"price":{"amount":%d,"currency":"USD"}}`, 3000+workerID))
			req := httptest.NewRequest(http.MethodPatch, patchURL, bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("If-Match", `"1"`)
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

	// Across BOTH endpoints competing on version 1, exactly ONE update may commit!
	assert.Equal(t, 1, successCount, "Exactly one update across title or price endpoints must succeed")
	assert.Equal(t, totalConcurrency-1, conflictCount, "All other 49 concurrent updates must fail with 412")

	finalProd, err := svc.GetProduct(t.Context(), prod.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(2), finalProd.Version)
}

// TestAdversarialCatalog_BoundaryFuzzing verifies boundary inputs for SKU, Title, Price,
// and pagination parameters.
func TestAdversarialCatalog_BoundaryFuzzing(t *testing.T) {
	router, svc := setupCatalogTestRouter(t)

	t.Run("SKU_Boundary_Lengths", func(t *testing.T) {
		// 2 chars: too short (min is 3)
		_, err := svc.CreateProduct(t.Context(), catalog.CreateProductRequest{
			SKU:   "SK",
			Title: "Valid Title",
			Price: money.Money{Amount: 100, Currency: "USD"},
		})
		require.ErrorIs(t, err, catalog.ErrInvalidSKU)

		// 3 chars: min valid
		p3, err := svc.CreateProduct(t.Context(), catalog.CreateProductRequest{
			SKU:   "SKU",
			Title: "Valid Title",
			Price: money.Money{Amount: 100, Currency: "USD"},
		})
		require.NoError(t, err)
		assert.Equal(t, "SKU", p3.SKU)

		// 64 chars: max valid
		sku64 := strings.Repeat("A", 64)
		p64, err := svc.CreateProduct(t.Context(), catalog.CreateProductRequest{
			SKU:   sku64,
			Title: "Valid Title",
			Price: money.Money{Amount: 100, Currency: "USD"},
		})
		require.NoError(t, err)
		assert.Equal(t, sku64, p64.SKU)

		// 65 chars: too long (max is 64)
		sku65 := strings.Repeat("A", 65)
		_, err = svc.CreateProduct(t.Context(), catalog.CreateProductRequest{
			SKU:   sku65,
			Title: "Valid Title",
			Price: money.Money{Amount: 100, Currency: "USD"},
		})
		require.ErrorIs(t, err, catalog.ErrInvalidSKU)
	})

	t.Run("Title_Boundary_Lengths", func(t *testing.T) {
		// Empty title
		_, err := svc.CreateProduct(t.Context(), catalog.CreateProductRequest{
			SKU:   "SKU-TITLE-0",
			Title: "",
			Price: money.Money{Amount: 100, Currency: "USD"},
		})
		require.ErrorIs(t, err, catalog.ErrInvalidTitle)

		// Whitespace only title
		_, err = svc.CreateProduct(t.Context(), catalog.CreateProductRequest{
			SKU:   "SKU-TITLE-WS",
			Title: "     \t\n  ",
			Price: money.Money{Amount: 100, Currency: "USD"},
		})
		require.ErrorIs(t, err, catalog.ErrInvalidTitle)

		// 1 char title (valid)
		p1, err := svc.CreateProduct(t.Context(), catalog.CreateProductRequest{
			SKU:   "SKU-TITLE-1",
			Title: "X",
			Price: money.Money{Amount: 100, Currency: "USD"},
		})
		require.NoError(t, err)
		assert.Equal(t, "X", p1.Title)

		// 255 chars title (valid)
		title255 := strings.Repeat("T", 255)
		p255, err := svc.CreateProduct(t.Context(), catalog.CreateProductRequest{
			SKU:   "SKU-TITLE-255",
			Title: title255,
			Price: money.Money{Amount: 100, Currency: "USD"},
		})
		require.NoError(t, err)
		assert.Equal(t, title255, p255.Title)

		// 256 chars title (invalid, exceeds 255)
		title256 := strings.Repeat("T", 256)
		_, err = svc.CreateProduct(t.Context(), catalog.CreateProductRequest{
			SKU:   "SKU-TITLE-256",
			Title: title256,
			Price: money.Money{Amount: 100, Currency: "USD"},
		})
		require.ErrorIs(t, err, catalog.ErrInvalidTitle)
	})

	t.Run("Price_Boundaries", func(t *testing.T) {
		// Zero price
		_, err := svc.CreateProduct(t.Context(), catalog.CreateProductRequest{
			SKU:   "SKU-PRICE-0",
			Title: "Zero Price",
			Price: money.Money{Amount: 0, Currency: "USD"},
		})
		require.ErrorIs(t, err, catalog.ErrInvalidPrice)

		// Negative price
		_, err = svc.CreateProduct(t.Context(), catalog.CreateProductRequest{
			SKU:   "SKU-PRICE-NEG",
			Title: "Negative Price",
			Price: money.Money{Amount: -100, Currency: "USD"},
		})
		require.ErrorIs(t, err, catalog.ErrInvalidPrice)

		// MaxInt64 price
		pMax, err := svc.CreateProduct(t.Context(), catalog.CreateProductRequest{
			SKU:   "SKU-PRICE-MAX",
			Title: "MaxInt64 Price",
			Price: money.Money{Amount: math.MaxInt64, Currency: "USD"},
		})
		require.NoError(t, err)
		assert.Equal(t, int64(math.MaxInt64), pMax.PriceMinor)
	})

	t.Run("Non_Existent_Product_Updates_Return_404", func(t *testing.T) {
		randomID := uuid.New()
		url := fmt.Sprintf("/api/v1/products/%s", randomID.String())

		putBody := []byte(`{"title":"Ghost Product","is_active":true}`)
		req := httptest.NewRequest(http.MethodPut, url, bytes.NewReader(putBody))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("If-Match", `"1"`)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusNotFound, rec.Code)
	})
}
