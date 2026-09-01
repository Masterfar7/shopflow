package search_test

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"shopflow/internal/platform/search"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAdversarial_ZeroFloatsInvariant asserts via reflection that no float types exist in search structures.
func TestAdversarial_ZeroFloatsInvariant(t *testing.T) {
	typesToCheck := []any{
		search.ProductDocument{},
		search.Query{},
		search.FacetBucket{},
		search.Facets{},
		search.SearchResult{},
	}

	for _, instance := range typesToCheck {
		typ := reflect.TypeOf(instance)
		t.Run(typ.Name(), func(t *testing.T) {
			for i := 0; i < typ.NumField(); i++ {
				field := typ.Field(i)
				fKind := field.Type.Kind()
				if fKind == reflect.Pointer || fKind == reflect.Slice {
					fKind = field.Type.Elem().Kind()
				}
				assert.NotEqual(t, reflect.Float32, fKind, "Field %s in %s must not be float32", field.Name, typ.Name())
				assert.NotEqual(t, reflect.Float64, fKind, "Field %s in %s must not be float64", field.Name, typ.Name())
			}
		})
	}

	// Verify specific monetary fields are int64
	docType := reflect.TypeOf(search.ProductDocument{})
	priceField, ok := docType.FieldByName("PriceMinor")
	require.True(t, ok, "PriceMinor field must exist on ProductDocument")
	assert.Equal(t, reflect.Int64, priceField.Type.Kind(), "PriceMinor must be int64")

	queryType := reflect.TypeOf(search.Query{})
	minPriceField, ok := queryType.FieldByName("MinPriceMinor")
	require.True(t, ok, "MinPriceMinor field must exist on Query")
	assert.Equal(t, reflect.Int64, minPriceField.Type.Elem().Kind(), "MinPriceMinor must be *int64")

	maxPriceField, ok := queryType.FieldByName("MaxPriceMinor")
	require.True(t, ok, "MaxPriceMinor field must exist on Query")
	assert.Equal(t, reflect.Int64, maxPriceField.Type.Elem().Kind(), "MaxPriceMinor must be *int64")
}

// TestAdversarial_TitleWeightingOracle verifies hierarchical boosting order:
// Title+Desc (4.0) > Title (3.0) > SKU+Desc (3.0 vs 2.0+1.0) > SKU (2.0) > Desc (1.0)
func TestAdversarial_TitleWeightingOracle(t *testing.T) {
	ctx := context.Background()
	client := search.NewMemoryClient()

	docs := []search.ProductDocument{
		{
			ID:          "doc-title-only",
			SKU:         "SKU-AAA",
			Title:       "Pro Gaming Keyboard with Red Switches",
			Description: "High endurance office accessory",
			PriceMinor:  10000,
			InStock:     true,
		},
		{
			ID:          "doc-sku-only",
			SKU:         "KEYBOARD-RGB-100",
			Title:       "Pro Gaming Peripherals",
			Description: "High endurance office accessory",
			PriceMinor:  10000,
			InStock:     true,
		},
		{
			ID:          "doc-desc-only",
			SKU:         "SKU-CCC",
			Title:       "Pro Gaming Peripherals",
			Description: "Premium wireless mechanical keyboard with wrist support",
			PriceMinor:  10000,
			InStock:     true,
		},
		{
			ID:          "doc-title-and-desc",
			SKU:         "SKU-DDD",
			Title:       "Pro Gaming Keyboard 80%",
			Description: "Best ergonomic keyboard on the market",
			PriceMinor:  10000,
			InStock:     true,
		},
	}
	require.NoError(t, client.BulkIndex(ctx, docs))

	// Search case-insensitive
	queries := []string{"keyboard", "KEYBOARD", "kEyBoArD"}
	for _, qStr := range queries {
		t.Run(qStr, func(t *testing.T) {
			res, err := client.Search(ctx, search.Query{Text: qStr})
			require.NoError(t, err)
			require.Equal(t, int64(4), res.TotalHits)

			// 1. doc-title-and-desc matches title (+3) and desc (+1) = 4.0
			assert.Equal(t, "doc-title-and-desc", res.Products[0].ID)
			// 2. doc-title-only matches title (+3) = 3.0
			assert.Equal(t, "doc-title-only", res.Products[1].ID)
			// 3. doc-sku-only matches sku (+2) = 2.0
			assert.Equal(t, "doc-sku-only", res.Products[2].ID)
			// 4. doc-desc-only matches desc (+1) = 1.0
			assert.Equal(t, "doc-desc-only", res.Products[3].ID)
		})
	}
}

// TestAdversarial_CategoryFacetingOracle validates that facet counts precisely mirror matching query results.
func TestAdversarial_CategoryFacetingOracle(t *testing.T) {
	ctx := context.Background()
	client := search.NewMemoryClient()

	categories := []string{"Audio", "Laptops", "Peripherals", "Displays"}
	var allDocs []search.ProductDocument

	// 10 docs in Audio ($100-$1000)
	// 10 docs in Laptops ($500-$2000)
	// 10 docs in Peripherals ($20-$200)
	// 10 docs in Displays ($150-$800)
	for i := 0; i < 40; i++ {
		cat := categories[i/10]
		price := int64((i%10 + 1) * 10000) // 10000, 20000, ..., 100000 minor units
		inStock := (i % 2 == 0)

		allDocs = append(allDocs, search.ProductDocument{
			ID:           fmt.Sprintf("prod-%02d", i),
			SKU:          fmt.Sprintf("SKU-%s-%02d", strings.ToUpper(cat[:3]), i),
			Title:        fmt.Sprintf("Ultra %s Device %d", cat, i),
			Description:  fmt.Sprintf("Description for %s device %d", cat, i),
			CategoryName: cat,
			PriceMinor:   price,
			Currency:     "USD",
			InStock:      inStock,
		})
	}
	require.NoError(t, client.BulkIndex(ctx, allDocs))

	// Filter with min_price=30000, max_price=70000, in_stock=true
	minP := int64(30000)
	maxP := int64(70000)
	inStock := true

	res, err := client.Search(ctx, search.Query{
		MinPriceMinor: &minP,
		MaxPriceMinor: &maxP,
		InStockOnly:   &inStock,
	})
	require.NoError(t, err)

	// Independent oracle computation
	expectedCounts := make(map[string]int64)
	for _, doc := range allDocs {
		if doc.PriceMinor >= minP && doc.PriceMinor <= maxP && doc.InStock {
			expectedCounts[doc.CategoryName]++
		}
	}

	var expectedTotal int64
	for _, c := range expectedCounts {
		expectedTotal += c
	}

	assert.Equal(t, expectedTotal, res.TotalHits)

	// Verify facet buckets match expected counts
	actualCounts := make(map[string]int64)
	for _, bucket := range res.Facets.Categories {
		actualCounts[bucket.Key] = bucket.Count
	}
	assert.Equal(t, expectedCounts, actualCounts)

	// Verify facets sorting: Count DESC, Key ASC
	for i := 1; i < len(res.Facets.Categories); i++ {
		prev := res.Facets.Categories[i-1]
		curr := res.Facets.Categories[i]
		if prev.Count == curr.Count {
			assert.Less(t, prev.Key, curr.Key, "Facets with equal count must be sorted lexicographically")
		} else {
			assert.Greater(t, prev.Count, curr.Count, "Facets must be sorted count descending")
		}
	}
}

// TestAdversarial_PaginationBoundaries tests extreme and edge case pagination inputs.
func TestAdversarial_PaginationBoundaries(t *testing.T) {
	ctx := context.Background()
	client := search.NewMemoryClient()

	for i := 1; i <= 15; i++ {
		require.NoError(t, client.IndexProduct(ctx, search.ProductDocument{
			ID:         fmt.Sprintf("item-%02d", i),
			SKU:        fmt.Sprintf("SKU-%02d", i),
			Title:      fmt.Sprintf("Widget %02d", i),
			PriceMinor: int64(i * 100),
			InStock:    true,
		}))
	}

	// Negative/Zero page clamped to 1
	res, err := client.Search(ctx, search.Query{Page: 0, PageSize: 5})
	require.NoError(t, err)
	assert.Equal(t, 1, res.Page)
	assert.Len(t, res.Products, 5)

	resNeg, err := client.Search(ctx, search.Query{Page: -5, PageSize: 5})
	require.NoError(t, err)
	assert.Equal(t, 1, resNeg.Page)

	// Negative/Zero pageSize clamped to 20
	resSizeZero, err := client.Search(ctx, search.Query{Page: 1, PageSize: 0})
	require.NoError(t, err)
	assert.Equal(t, 20, resSizeZero.PageSize)

	// Oversized pageSize clamped to 100
	resSizeHuge, err := client.Search(ctx, search.Query{Page: 1, PageSize: 500})
	require.NoError(t, err)
	assert.Equal(t, 100, resSizeHuge.PageSize)

	// Page far beyond total items
	resFar, err := client.Search(ctx, search.Query{Page: 100, PageSize: 10})
	require.NoError(t, err)
	assert.Equal(t, int64(15), resFar.TotalHits)
	assert.Equal(t, 2, resFar.TotalPages)
	assert.Empty(t, resFar.Products)
}

// TestAdversarial_ContextCancellation verifies that canceled contexts return context.Canceled.
func TestAdversarial_ContextCancellation(t *testing.T) {
	client := search.NewMemoryClient()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	doc := search.ProductDocument{
		ID:         "p1",
		SKU:        "S1",
		Title:      "Canceled Item",
		PriceMinor: 1000,
	}

	assert.ErrorIs(t, client.Ping(ctx), context.Canceled)
	assert.ErrorIs(t, client.EnsureIndex(ctx), context.Canceled)
	assert.ErrorIs(t, client.IndexProduct(ctx, doc), context.Canceled)
	assert.ErrorIs(t, client.DeleteProduct(ctx, "p1"), context.Canceled)
	assert.ErrorIs(t, client.BulkIndex(ctx, []search.ProductDocument{doc}), context.Canceled)

	_, err := client.Search(ctx, search.Query{Text: "Canceled"})
	assert.ErrorIs(t, err, context.Canceled)
}

// TestAdversarial_HighStressParallelism hammers the client with concurrent readers and writers.
func TestAdversarial_HighStressParallelism(t *testing.T) {
	ctx := context.Background()
	client := search.NewMemoryClient()

	const numWriters = 20
	const numReaders = 30
	const numOps = 100

	var wg sync.WaitGroup
	wg.Add(numWriters + numReaders)

	// Concurrent Writers (Index + Bulk + Delete)
	for w := 0; w < numWriters; w++ {
		workerID := w
		go func() {
			defer wg.Done()
			for i := 0; i < numOps; i++ {
				docID := fmt.Sprintf("stress-%d-%d", workerID, i)
				doc := search.ProductDocument{
					ID:           docID,
					SKU:          fmt.Sprintf("SKU-%d-%d", workerID, i),
					Title:        fmt.Sprintf("High Stress Laptop %d", i),
					Description:  "Stress testing concurrent memory search",
					CategoryName: fmt.Sprintf("Category-%d", i%5),
					PriceMinor:   int64(i * 1000),
					InStock:      i%2 == 0,
				}
				_ = client.IndexProduct(ctx, doc)

				if i%10 == 0 {
					_ = client.DeleteProduct(ctx, docID)
				}
			}
		}()
	}

	// Concurrent Readers
	for r := 0; r < numReaders; r++ {
		go func() {
			defer wg.Done()
			for i := 0; i < numOps; i++ {
				minP := int64(10000)
				maxP := int64(50000)
				res, err := client.Search(ctx, search.Query{
					Text:          "Laptop",
					MinPriceMinor: &minP,
					MaxPriceMinor: &maxP,
					Page:          1,
					PageSize:      20,
				})
				if err == nil && res != nil {
					// Validate integrity of returned page
					assert.LessOrEqual(t, len(res.Products), 20)
					for _, p := range res.Products {
						assert.GreaterOrEqual(t, p.PriceMinor, minP)
						assert.LessOrEqual(t, p.PriceMinor, maxP)
					}
				}
			}
		}()
	}

	wg.Wait()
}
