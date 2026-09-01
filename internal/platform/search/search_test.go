package search_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"shopflow/internal/platform/config"
	"shopflow/internal/platform/search"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMemoryClient_TitleBoostAndWeighting(t *testing.T) {
	ctx := context.Background()
	client := search.NewMemoryClient()

	catID := "c1111111-1111-1111-1111-111111111111"

	// Doc A: match in title
	docA := search.ProductDocument{
		ID:           "doc-a",
		SKU:          "SKU-AAA",
		Title:        "Ergonomic Wireless Mechanical Keyboard",
		Description:  "Standard office peripheral equipment",
		CategoryID:   &catID,
		CategoryName: "Keyboards",
		PriceMinor:   9900,
		Currency:     "USD",
		InStock:      true,
	}

	// Doc B: match in sku
	docB := search.ProductDocument{
		ID:           "doc-b",
		SKU:          "KEYBOARD-SKU-99",
		Title:        "Custom Wrist Rest Pad",
		Description:  "Foam rest for typing comfortably",
		CategoryID:   &catID,
		CategoryName: "Accessories",
		PriceMinor:   2500,
		Currency:     "USD",
		InStock:      true,
	}

	// Doc C: match in description
	docC := search.ProductDocument{
		ID:           "doc-c",
		SKU:          "SKU-CCC",
		Title:        "Desk Mat XXL",
		Description:  "Large desk mat suitable for any keyboard and mouse",
		CategoryID:   &catID,
		CategoryName: "Accessories",
		PriceMinor:   1999,
		Currency:     "USD",
		InStock:      true,
	}

	require.NoError(t, client.BulkIndex(ctx, []search.ProductDocument{docA, docB, docC}))

	res, err := client.Search(ctx, search.Query{
		Text: "keyboard",
	})
	require.NoError(t, err)
	require.Equal(t, int64(3), res.TotalHits)
	require.Len(t, res.Products, 3)

	// Verification of weighting: title^3 (score 3) > sku^2 (score 2) > description^1 (score 1)
	assert.Equal(t, "doc-a", res.Products[0].ID, "Title match must rank first due to title^3 boost")
	assert.Equal(t, "doc-b", res.Products[1].ID, "SKU match must rank second due to sku^2 boost")
	assert.Equal(t, "doc-c", res.Products[2].ID, "Description match must rank third due to description^1 weight")
}

func TestMemoryClient_PriceFilterMinorUnits(t *testing.T) {
	ctx := context.Background()
	client := search.NewMemoryClient()

	docs := []search.ProductDocument{
		{ID: "p1", SKU: "SKU-1", Title: "Budget Item", PriceMinor: 1000, Currency: "USD", InStock: true},
		{ID: "p2", SKU: "SKU-2", Title: "Mid Item", PriceMinor: 5000, Currency: "USD", InStock: true},
		{ID: "p3", SKU: "SKU-3", Title: "Premium Item", PriceMinor: 10000, Currency: "USD", InStock: true},
		{ID: "p4", SKU: "SKU-4", Title: "Luxury Item", PriceMinor: 50000, Currency: "USD", InStock: true},
	}
	require.NoError(t, client.BulkIndex(ctx, docs))

	// Min price filter
	minPrice := int64(5000)
	res, err := client.Search(ctx, search.Query{
		MinPriceMinor: &minPrice,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(3), res.TotalHits)
	for _, p := range res.Products {
		assert.GreaterOrEqual(t, p.PriceMinor, int64(5000))
	}

	// Max price filter
	maxPrice := int64(5000)
	res, err = client.Search(ctx, search.Query{
		MaxPriceMinor: &maxPrice,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(2), res.TotalHits)
	for _, p := range res.Products {
		assert.LessOrEqual(t, p.PriceMinor, int64(5000))
	}

	// Range filter
	minRange := int64(5000)
	maxRange := int64(10000)
	res, err = client.Search(ctx, search.Query{
		MinPriceMinor: &minRange,
		MaxPriceMinor: &maxRange,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(2), res.TotalHits)
	assert.Equal(t, "p2", res.Products[0].ID)
	assert.Equal(t, "p3", res.Products[1].ID)
}

func TestMemoryClient_CategoryFilterAndFaceting(t *testing.T) {
	ctx := context.Background()
	client := search.NewMemoryClient()

	catAudio := "11111111-0000-0000-0000-000000000001"
	catGaming := "11111111-0000-0000-0000-000000000002"

	docs := []search.ProductDocument{
		{ID: "1", SKU: "S1", Title: "Studio Headphone", CategoryID: &catAudio, CategoryName: "Audio", PriceMinor: 15000, InStock: true},
		{ID: "2", SKU: "S2", Title: "Wireless Earbuds", CategoryID: &catAudio, CategoryName: "Audio", PriceMinor: 8000, InStock: true},
		{ID: "3", SKU: "S3", Title: "Gaming Headset", CategoryID: &catGaming, CategoryName: "Gaming", PriceMinor: 9900, InStock: true},
	}
	require.NoError(t, client.BulkIndex(ctx, docs))

	// Search all -> check facets
	res, err := client.Search(ctx, search.Query{})
	require.NoError(t, err)
	require.Equal(t, int64(3), res.TotalHits)
	require.Len(t, res.Facets.Categories, 2)
	assert.Equal(t, "Audio", res.Facets.Categories[0].Key)
	assert.Equal(t, int64(2), res.Facets.Categories[0].Count)
	assert.Equal(t, "Gaming", res.Facets.Categories[1].Key)
	assert.Equal(t, int64(1), res.Facets.Categories[1].Count)

	// Filter by CategoryID
	resCat, err := client.Search(ctx, search.Query{
		CategoryID: &catGaming,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(1), resCat.TotalHits)
	assert.Equal(t, "Gaming Headset", resCat.Products[0].Title)
	assert.Len(t, resCat.Facets.Categories, 1)
	assert.Equal(t, "Gaming", resCat.Facets.Categories[0].Key)
	assert.Equal(t, int64(1), resCat.Facets.Categories[0].Count)
}

func TestMemoryClient_InStockFilter(t *testing.T) {
	ctx := context.Background()
	client := search.NewMemoryClient()

	docs := []search.ProductDocument{
		{ID: "1", SKU: "S1", Title: "In Stock Item", PriceMinor: 1000, InStock: true},
		{ID: "2", SKU: "S2", Title: "Out of Stock Item", PriceMinor: 2000, InStock: false},
	}
	require.NoError(t, client.BulkIndex(ctx, docs))

	inStock := true
	res, err := client.Search(ctx, search.Query{
		InStockOnly: &inStock,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(1), res.TotalHits)
	assert.Equal(t, "In Stock Item", res.Products[0].Title)
}

func TestMemoryClient_Pagination(t *testing.T) {
	ctx := context.Background()
	client := search.NewMemoryClient()

	var docs []search.ProductDocument
	for i := 1; i <= 25; i++ {
		docs = append(docs, search.ProductDocument{
			ID:         fmt.Sprintf("item-%02d", i),
			SKU:        fmt.Sprintf("SKU-%02d", i),
			Title:      fmt.Sprintf("Product %02d", i),
			PriceMinor: int64(i * 100),
			InStock:    true,
		})
	}
	require.NoError(t, client.BulkIndex(ctx, docs))

	// Page 1 with pageSize 10
	resP1, err := client.Search(ctx, search.Query{
		Page:     1,
		PageSize: 10,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(25), resP1.TotalHits)
	assert.Equal(t, 3, resP1.TotalPages)
	assert.Equal(t, 1, resP1.Page)
	assert.Len(t, resP1.Products, 10)
	assert.Equal(t, "item-01", resP1.Products[0].ID)

	// Page 2
	resP2, err := client.Search(ctx, search.Query{
		Page:     2,
		PageSize: 10,
	})
	require.NoError(t, err)
	assert.Equal(t, 2, resP2.Page)
	assert.Len(t, resP2.Products, 10)
	assert.Equal(t, "item-11", resP2.Products[0].ID)

	// Page 3
	resP3, err := client.Search(ctx, search.Query{
		Page:     3,
		PageSize: 10,
	})
	require.NoError(t, err)
	assert.Equal(t, 3, resP3.Page)
	assert.Len(t, resP3.Products, 5)
	assert.Equal(t, "item-21", resP3.Products[0].ID)

	// Page 4 (out of range)
	resP4, err := client.Search(ctx, search.Query{
		Page:     4,
		PageSize: 10,
	})
	require.NoError(t, err)
	assert.Empty(t, resP4.Products)
}

func TestMemoryClient_DeleteProduct(t *testing.T) {
	ctx := context.Background()
	client := search.NewMemoryClient()

	doc := search.ProductDocument{
		ID:         "del-1",
		SKU:        "SKU-DEL",
		Title:      "Temporary Item",
		PriceMinor: 500,
		InStock:    true,
	}
	require.NoError(t, client.IndexProduct(ctx, doc))

	res, err := client.Search(ctx, search.Query{Text: "Temporary"})
	require.NoError(t, err)
	assert.Equal(t, int64(1), res.TotalHits)

	require.NoError(t, client.DeleteProduct(ctx, "del-1"))

	resAfter, err := client.Search(ctx, search.Query{Text: "Temporary"})
	require.NoError(t, err)
	assert.Equal(t, int64(0), resAfter.TotalHits)
	assert.Empty(t, resAfter.Products)
}

func TestMemoryClient_Concurrency(t *testing.T) {
	ctx := context.Background()
	client := search.NewMemoryClient()

	const numWorkers = 20
	const numOps = 50

	var wg sync.WaitGroup
	wg.Add(numWorkers * 2)

	// Concurrent writers
	for w := 0; w < numWorkers; w++ {
		workerID := w
		go func() {
			defer wg.Done()
			for i := 0; i < numOps; i++ {
				docID := fmt.Sprintf("doc-%d-%d", workerID, i)
				_ = client.IndexProduct(ctx, search.ProductDocument{
					ID:         docID,
					SKU:        fmt.Sprintf("SKU-%d-%d", workerID, i),
					Title:      fmt.Sprintf("Concurrent Item %d", i),
					PriceMinor: int64(i * 10),
					InStock:    true,
				})
			}
		}()
	}

	// Concurrent readers
	for w := 0; w < numWorkers; w++ {
		go func() {
			defer wg.Done()
			for i := 0; i < numOps; i++ {
				_, _ = client.Search(ctx, search.Query{
					Text: "Concurrent",
					Page: 1,
				})
			}
		}()
	}

	wg.Wait()

	res, err := client.Search(ctx, search.Query{Text: "Concurrent"})
	require.NoError(t, err)
	assert.Equal(t, int64(numWorkers*numOps), res.TotalHits)
}

func TestOpenSearchClient_HTTPAdapter(t *testing.T) {
	// Mock OpenSearch HTTP server
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/_cluster/health":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status": "green"}`))

		case r.Method == http.MethodHead && r.URL.Path == "/test_products":
			w.WriteHeader(http.StatusNotFound)

		case r.Method == http.MethodPut && r.URL.Path == "/test_products":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"acknowledged": true}`))

		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/test_products/_doc/"):
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"result": "created"}`))

		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/test_products/_doc/"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"result": "deleted"}`))

		case r.Method == http.MethodPost && r.URL.Path == "/_bulk":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"errors": false, "items": []}`))

		case r.Method == http.MethodPost && r.URL.Path == "/test_products/_search":
			resp := map[string]any{
				"hits": map[string]any{
					"total": map[string]any{"value": 1},
					"hits": []map[string]any{
						{
							"_source": search.ProductDocument{
								ID:           "test-1",
								SKU:          "SKU-TEST",
								Title:        "OpenSearch Keyboard",
								Description:  "Testing OpenSearch adapter",
								CategoryName: "Peripherals",
								PriceMinor:   12500,
								Currency:     "USD",
								InStock:      true,
							},
						},
					},
				},
				"aggregations": map[string]any{
					"categories": map[string]any{
						"buckets": []map[string]any{
							{"key": "Peripherals", "doc_count": 1},
						},
					},
				},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)

		default:
			http.NotFound(w, r)
		}
	}))
	defer mockServer.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	osClient, err := search.NewOpenSearchClient(search.OpenSearchConfig{
		URL:      mockServer.URL,
		Index:    "test_products",
		Timeout:  2 * time.Second,
		Username: "admin",
		Password: "password",
	}, nil)
	require.NoError(t, err)

	// Ping
	require.NoError(t, osClient.Ping(ctx))

	// EnsureIndex
	require.NoError(t, osClient.EnsureIndex(ctx))

	// IndexProduct
	doc := search.ProductDocument{
		ID:           "test-1",
		SKU:          "SKU-TEST",
		Title:        "OpenSearch Keyboard",
		PriceMinor:   12500,
		Currency:     "USD",
		CategoryName: "Peripherals",
		InStock:      true,
	}
	require.NoError(t, osClient.IndexProduct(ctx, doc))

	// BulkIndex
	require.NoError(t, osClient.BulkIndex(ctx, []search.ProductDocument{doc}))

	// Search
	res, err := osClient.Search(ctx, search.Query{
		Text: "Keyboard",
	})
	require.NoError(t, err)
	assert.Equal(t, int64(1), res.TotalHits)
	require.Len(t, res.Products, 1)
	assert.Equal(t, "test-1", res.Products[0].ID)
	assert.Equal(t, int64(12500), res.Products[0].PriceMinor)
	require.Len(t, res.Facets.Categories, 1)
	assert.Equal(t, "Peripherals", res.Facets.Categories[0].Key)
	assert.Equal(t, int64(1), res.Facets.Categories[0].Count)

	// DeleteProduct
	require.NoError(t, osClient.DeleteProduct(ctx, "test-1"))
}

func TestNewClientFromConfig(t *testing.T) {
	ctx := context.Background()

	// Default/Empty -> MemoryClient
	cfgMemory := config.Config{
		SearchProvider: "memory",
	}
	c1, err := search.NewClientFromConfig(ctx, cfgMemory, nil)
	require.NoError(t, err)
	_, isMemory := c1.(*search.MemoryClient)
	assert.True(t, isMemory, "expected *search.MemoryClient")

	// Empty OpenSearchURL -> MemoryClient
	cfgEmptyURL := config.Config{
		SearchProvider: "opensearch",
		OpenSearchURL:  "",
	}
	c2, err := search.NewClientFromConfig(ctx, cfgEmptyURL, nil)
	require.NoError(t, err)
	_, isMemory2 := c2.(*search.MemoryClient)
	assert.True(t, isMemory2, "expected *search.MemoryClient when URL empty")

	// OpenSearch provider with URL -> OpenSearchClient
	cfgOS := config.Config{
		SearchProvider: "opensearch",
		OpenSearchURL:  "http://localhost:9200",
	}
	c3, err := search.NewClientFromConfig(ctx, cfgOS, nil)
	require.NoError(t, err)
	_, isOS := c3.(*search.OpenSearchClient)
	assert.True(t, isOS, "expected *search.OpenSearchClient")
}
