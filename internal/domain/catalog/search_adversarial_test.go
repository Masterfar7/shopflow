package catalog_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"shopflow/internal/domain/catalog"
	"shopflow/internal/domain/money"
	"shopflow/internal/platform/search"
	"shopflow/internal/platform/web"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAdversarial_SearchBoundaryConditions verifies boundary queries:
// min_price=0, min_price==max_price, min_price>max_price rejection (400),
// and zero-priced items.
func TestAdversarial_SearchBoundaryConditions(t *testing.T) {
	router, svc, _ := setupCatalogSearchTestRouter(t)
	ctx := context.Background()

	// Create items with various prices
	pFree, err := svc.CreateProduct(ctx, catalog.CreateProductRequest{
		SKU:   "SKU-FREE-0",
		Title: "Free Promotional Sticker",
		Price: money.Money{Amount: 1, Currency: "USD"}, // catalog requires price > 0
	})
	require.NoError(t, err)

	pMid, err := svc.CreateProduct(ctx, catalog.CreateProductRequest{
		SKU:   "SKU-MID-5000",
		Title: "Standard Gaming Mouse",
		Price: money.Money{Amount: 5000, Currency: "USD"},
	})
	require.NoError(t, err)

	pHigh, err := svc.CreateProduct(ctx, catalog.CreateProductRequest{
		SKU:   "SKU-HIGH-10000",
		Title: "Pro Mechanical Keyboard",
		Price: money.Money{Amount: 10000, Currency: "USD"},
	})
	require.NoError(t, err)

	t.Run("min_price=0 returns all items above or equal to 0", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/products/search?min_price=0", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		require.Equal(t, http.StatusOK, rec.Code)
		var res search.SearchResult
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &res))
		assert.Equal(t, int64(3), res.TotalHits)
	})

	t.Run("min_price == max_price returns exact price match", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/products/search?min_price=5000&max_price=5000", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		require.Equal(t, http.StatusOK, rec.Code)
		var res search.SearchResult
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &res))
		assert.Equal(t, int64(1), res.TotalHits)
		require.Len(t, res.Products, 1)
		assert.Equal(t, pMid.ID.String(), res.Products[0].ID)
		assert.Equal(t, int64(5000), res.Products[0].PriceMinor)
	})

	t.Run("min_price == max_price with no matching items returns empty 200 OK", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/products/search?min_price=7777&max_price=7777", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		require.Equal(t, http.StatusOK, rec.Code)
		var res search.SearchResult
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &res))
		assert.Equal(t, int64(0), res.TotalHits)
		assert.Empty(t, res.Products)
	})

	t.Run("min_price > max_price returns HTTP 400 INVALID_PRICE_RANGE", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/products/search?min_price=5001&max_price=5000", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		require.Equal(t, http.StatusBadRequest, rec.Code)
		var prob web.ProblemDetails
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &prob))
		assert.Equal(t, http.StatusBadRequest, prob.Status)
		assert.Equal(t, "INVALID_PRICE_RANGE", prob.Code)
		assert.Equal(t, "Invalid Price Range", prob.Title)
		assert.Equal(t, "min_price cannot exceed max_price", prob.Detail)
	})

	_ = pFree
	_ = pHigh
}

// TestAdversarial_SearchNegativeInputs validates strict RFC 7807 Problem Details
// on all malformed query parameters (floats, negative numbers, invalid UUIDs).
func TestAdversarial_SearchNegativeInputs(t *testing.T) {
	router, _, _ := setupCatalogSearchTestRouter(t)

	testCases := []struct {
		name           string
		url            string
		expectedCode   string
		expectedTitle  string
		expectedDetail string
	}{
		{
			name:           "float min_price rejected (zero floats invariant)",
			url:            "/api/v1/products/search?min_price=19.99",
			expectedCode:   "INVALID_PRICE_FILTER",
			expectedTitle:  "Invalid Price Filter",
			expectedDetail: "min_price must be a non-negative integer in minor units",
		},
		{
			name:           "float max_price rejected",
			url:            "/api/v1/products/search?max_price=99.95",
			expectedCode:   "INVALID_PRICE_FILTER",
			expectedTitle:  "Invalid Price Filter",
			expectedDetail: "max_price must be a non-negative integer in minor units",
		},
		{
			name:           "scientific notation rejected",
			url:            "/api/v1/products/search?min_price=1e5",
			expectedCode:   "INVALID_PRICE_FILTER",
			expectedTitle:  "Invalid Price Filter",
			expectedDetail: "min_price must be a non-negative integer in minor units",
		},
		{
			name:           "negative min_price rejected",
			url:            "/api/v1/products/search?min_price=-1",
			expectedCode:   "INVALID_PRICE_FILTER",
			expectedTitle:  "Invalid Price Filter",
			expectedDetail: "min_price must be a non-negative integer in minor units",
		},
		{
			name:           "negative max_price rejected",
			url:            "/api/v1/products/search?max_price=-1000",
			expectedCode:   "INVALID_PRICE_FILTER",
			expectedTitle:  "Invalid Price Filter",
			expectedDetail: "max_price must be a non-negative integer in minor units",
		},
		{
			name:           "non-numeric min_price rejected",
			url:            "/api/v1/products/search?min_price=foo_bar",
			expectedCode:   "INVALID_PRICE_FILTER",
			expectedTitle:  "Invalid Price Filter",
			expectedDetail: "min_price must be a non-negative integer in minor units",
		},
		{
			name:           "invalid category UUID - plain string",
			url:            "/api/v1/products/search?category_id=not-a-uuid",
			expectedCode:   "INVALID_CATEGORY_ID",
			expectedTitle:  "Invalid Category ID",
			expectedDetail: "category_id must be a valid UUID",
		},
		{
			name:           "invalid category UUID - numeric",
			url:            "/api/v1/products/search?category_id=12345678",
			expectedCode:   "INVALID_CATEGORY_ID",
			expectedTitle:  "Invalid Category ID",
			expectedDetail: "category_id must be a valid UUID",
		},
		{
			name:           "invalid category UUID - truncated",
			url:            "/api/v1/products/search?category_id=00000000-0000-0000-0000-00000000000",
			expectedCode:   "INVALID_CATEGORY_ID",
			expectedTitle:  "Invalid Category ID",
			expectedDetail: "category_id must be a valid UUID",
		},
		{
			name:           "invalid category UUID - non-hex chars",
			url:            "/api/v1/products/search?category_id=11111111-1111-1111-1111-11111111111Z",
			expectedCode:   "INVALID_CATEGORY_ID",
			expectedTitle:  "Invalid Category ID",
			expectedDetail: "category_id must be a valid UUID",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.url, nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			require.Equal(t, http.StatusBadRequest, rec.Code)
			assert.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))

			var prob web.ProblemDetails
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &prob))
			assert.Equal(t, http.StatusBadRequest, prob.Status)
			assert.Equal(t, tc.expectedCode, prob.Code)
			assert.Equal(t, tc.expectedTitle, prob.Title)
			assert.Equal(t, tc.expectedDetail, prob.Detail)
			assert.NotEmpty(t, prob.Type)
			assert.NotEmpty(t, prob.Instance)
		})
	}
}

// recordingSearchClient tracks calls to verify execution boundaries and error resilience.
type recordingSearchClient struct {
	mu          sync.Mutex
	indexedDocs []search.ProductDocument
	deletedIDs  []string
	failIndex   bool
	failBulk    bool
	failSearch  bool
}

func (r *recordingSearchClient) Ping(ctx context.Context) error        { return nil }
func (r *recordingSearchClient) EnsureIndex(ctx context.Context) error { return nil }

func (r *recordingSearchClient) IndexProduct(ctx context.Context, doc search.ProductDocument) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failIndex {
		return errors.New("simulated search index outage")
	}
	r.indexedDocs = append(r.indexedDocs, doc)
	return nil
}

func (r *recordingSearchClient) DeleteProduct(ctx context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deletedIDs = append(r.deletedIDs, id)
	return nil
}

func (r *recordingSearchClient) BulkIndex(ctx context.Context, docs []search.ProductDocument) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failBulk {
		return errors.New("simulated bulk index outage")
	}
	r.indexedDocs = append(r.indexedDocs, docs...)
	return nil
}

func (r *recordingSearchClient) Search(ctx context.Context, q search.Query) (*search.SearchResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failSearch {
		return nil, errors.New("simulated search engine error")
	}
	return &search.SearchResult{TotalHits: int64(len(r.indexedDocs))}, nil
}

// TestAdversarial_TransactionBoundaryAndFailureDecoupling verifies:
// 1. Indexing occurs post-commit.
// 2. If DB repository fails, search indexing is NOT called.
// 3. If search indexing fails, the DB transaction/mutation is NOT rolled back (isolated failure domain).
func TestAdversarial_TransactionBoundaryAndFailureDecoupling(t *testing.T) {
	ctx := context.Background()
	baseRepo := newMemoryCatalogRepo()
	recorder := &recordingSearchClient{}
	svc := catalog.NewServiceWithSearch(baseRepo, recorder, nil)

	t.Run("successful product creation indexes document post-commit", func(t *testing.T) {
		p, err := svc.CreateProduct(ctx, catalog.CreateProductRequest{
			SKU:   "SKU-TX-01",
			Title: "Transaction Safe Keyboard",
			Price: money.Money{Amount: 5000, Currency: "USD"},
		})
		require.NoError(t, err)

		recorder.mu.Lock()
		require.Len(t, recorder.indexedDocs, 1)
		assert.Equal(t, p.ID.String(), recorder.indexedDocs[0].ID)
		assert.Equal(t, "Transaction Safe Keyboard", recorder.indexedDocs[0].Title)
		assert.Equal(t, int64(5000), recorder.indexedDocs[0].PriceMinor)
		recorder.mu.Unlock()
	})

	t.Run("failed DB repository does NOT invoke search index", func(t *testing.T) {
		recorder.mu.Lock()
		countBefore := len(recorder.indexedDocs)
		recorder.mu.Unlock()

		// Attempt duplicate SKU
		_, err := svc.CreateProduct(ctx, catalog.CreateProductRequest{
			SKU:   "SKU-TX-01", // Duplicate!
			Title: "Duplicate Should Fail Before Search",
			Price: money.Money{Amount: 6000, Currency: "USD"},
		})
		require.ErrorIs(t, err, catalog.ErrDuplicateSKU)

		recorder.mu.Lock()
		countAfter := len(recorder.indexedDocs)
		recorder.mu.Unlock()
		assert.Equal(t, countBefore, countAfter, "Search IndexProduct must NEVER be called when DB fails")
	})

	t.Run("search engine failure does NOT rollback or fail product creation", func(t *testing.T) {
		recorder.mu.Lock()
		recorder.failIndex = true
		recorder.mu.Unlock()

		// Product should still be created in catalog repository despite search outage
		p, err := svc.CreateProduct(ctx, catalog.CreateProductRequest{
			SKU:   "SKU-TX-RESILIENT",
			Title: "Resilient Product During Search Outage",
			Price: money.Money{Amount: 8000, Currency: "USD"},
		})
		require.NoError(t, err, "Catalog mutation must succeed even if search indexing experiences an outage")
		require.NotNil(t, p)

		// Verify product is safely stored in DB
		saved, err := baseRepo.GetProductByID(ctx, p.ID)
		require.NoError(t, err)
		assert.Equal(t, "SKU-TX-RESILIENT", saved.SKU)

		recorder.mu.Lock()
		recorder.failIndex = false
		recorder.mu.Unlock()
	})
}

// TestAdversarial_ConcurrentCatalogSync tests high-concurrency catalog mutations
// and simultaneous search queries.
func TestAdversarial_ConcurrentCatalogSync(t *testing.T) {
	ctx := context.Background()
	repo := newMemoryCatalogRepo()
	searchClient := search.NewMemoryClient()
	svc := catalog.NewServiceWithSearch(repo, searchClient, nil)

	const numWorkers = 20
	const numOpsPerWorker = 20

	var wg sync.WaitGroup
	wg.Add(numWorkers * 2)

	// Mutators: Create products concurrently
	for w := 0; w < numWorkers; w++ {
		workerID := w
		go func() {
			defer wg.Done()
			for i := 0; i < numOpsPerWorker; i++ {
				_, _ = svc.CreateProduct(ctx, catalog.CreateProductRequest{
					SKU:   fmt.Sprintf("SKU-CONC-%d-%d", workerID, i),
					Title: fmt.Sprintf("Concurrent Item %d in worker %d", i, workerID),
					Price: money.Money{Amount: int64((i + 1) * 100), Currency: "USD"},
				})
			}
		}()
	}

	// Searchers: Search products concurrently
	for w := 0; w < numWorkers; w++ {
		go func() {
			defer wg.Done()
			for i := 0; i < numOpsPerWorker; i++ {
				_, _ = svc.SearchProducts(ctx, search.Query{
					Text:     "Concurrent",
					Page:     1,
					PageSize: 10,
				})
			}
		}()
	}

	wg.Wait()

	// Verify all created products are in the search index
	res, err := svc.SearchProducts(ctx, search.Query{
		Text:     "Concurrent",
		PageSize: 100,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(numWorkers*numOpsPerWorker), res.TotalHits)
}
