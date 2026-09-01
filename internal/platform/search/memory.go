package search

import (
	"context"
	"sort"
	"strings"
	"sync"
)

// MemoryClient provides a thread-safe, zero-dependency in-memory search implementation.
type MemoryClient struct {
	mu   sync.RWMutex
	docs map[string]ProductDocument
}

// NewMemoryClient constructs an in-memory search client.
func NewMemoryClient() *MemoryClient {
	return &MemoryClient{
		docs: make(map[string]ProductDocument),
	}
}

func (m *MemoryClient) Ping(ctx context.Context) error {
	return ctx.Err()
}

func (m *MemoryClient) EnsureIndex(ctx context.Context) error {
	return ctx.Err()
}

func (m *MemoryClient) IndexProduct(ctx context.Context, doc ProductDocument) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.docs[doc.ID] = doc
	return nil
}

func (m *MemoryClient) DeleteProduct(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.docs, id)
	return nil
}

func (m *MemoryClient) BulkIndex(ctx context.Context, docs []ProductDocument) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, d := range docs {
		m.docs[d.ID] = d
	}
	return nil
}

type scoredDoc struct {
	doc   ProductDocument
	score float64
}

func (m *MemoryClient) Search(ctx context.Context, q Query) (*SearchResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	page := q.Page
	if page < 1 {
		page = 1
	}
	pageSize := q.PageSize
	if pageSize < 1 {
		pageSize = 20
	} else if pageSize > 100 {
		pageSize = 100
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	queryText := strings.TrimSpace(strings.ToLower(q.Text))
	tokens := strings.Fields(queryText)

	var matched []scoredDoc
	categoryCounts := make(map[string]int64)

	for _, doc := range m.docs {
		// 1. Filter CategoryID
		if q.CategoryID != nil {
			if doc.CategoryID == nil || *doc.CategoryID != *q.CategoryID {
				continue
			}
		}

		// 2. Filter MinPriceMinor (int64)
		if q.MinPriceMinor != nil && doc.PriceMinor < *q.MinPriceMinor {
			continue
		}

		// 3. Filter MaxPriceMinor (int64)
		if q.MaxPriceMinor != nil && doc.PriceMinor > *q.MaxPriceMinor {
			continue
		}

		// 4. Filter InStockOnly
		if q.InStockOnly != nil && *q.InStockOnly && !doc.InStock {
			continue
		}

		// 5. Score Text Query with Title Boost (title^3, sku^2, description^1)
		score := 0.0
		if len(tokens) > 0 {
			lowerTitle := strings.ToLower(doc.Title)
			lowerSKU := strings.ToLower(doc.SKU)
			lowerDesc := strings.ToLower(doc.Description)

			matchedTokens := 0
			for _, token := range tokens {
				tokenMatched := false
				if strings.Contains(lowerTitle, token) {
					score += 3.0
					tokenMatched = true
				}
				if strings.Contains(lowerSKU, token) {
					score += 2.0
					tokenMatched = true
				}
				if strings.Contains(lowerDesc, token) {
					score += 1.0
					tokenMatched = true
				}
				if tokenMatched {
					matchedTokens++
				}
			}
			// Require at least one token match
			if matchedTokens == 0 {
				continue
			}
		} else {
			score = 1.0 // match_all
		}

		// Document passed all filters and query match
		matched = append(matched, scoredDoc{doc: doc, score: score})

		// Accumulate category facets
		if doc.CategoryName != "" {
			categoryCounts[doc.CategoryName]++
		}
	}

	// Sort matching documents by score DESC, then ID ASC for determinism
	sort.Slice(matched, func(i, j int) bool {
		if matched[i].score != matched[j].score {
			return matched[i].score > matched[j].score
		}
		return matched[i].doc.ID < matched[j].doc.ID
	})

	// Build sorted Category Facets
	facetBuckets := make([]FacetBucket, 0, len(categoryCounts))
	for catName, count := range categoryCounts {
		facetBuckets = append(facetBuckets, FacetBucket{
			Key:   catName,
			Count: count,
		})
	}
	sort.Slice(facetBuckets, func(i, j int) bool {
		if facetBuckets[i].Count != facetBuckets[j].Count {
			return facetBuckets[i].Count > facetBuckets[j].Count
		}
		return facetBuckets[i].Key < facetBuckets[j].Key
	})

	totalHits := int64(len(matched))
	totalPages := 0
	if totalHits > 0 {
		totalPages = int((totalHits + int64(pageSize) - 1) / int64(pageSize))
	}

	offset := (page - 1) * pageSize
	pagedDocs := make([]ProductDocument, 0)
	if offset < len(matched) {
		end := offset + pageSize
		if end > len(matched) {
			end = len(matched)
		}
		pagedDocs = make([]ProductDocument, end-offset)
		for i := offset; i < end; i++ {
			pagedDocs[i-offset] = matched[i].doc
		}
	}

	return &SearchResult{
		TotalHits:  totalHits,
		Page:       page,
		PageSize:   pageSize,
		TotalPages: totalPages,
		Products:   pagedDocs,
		Facets: Facets{
			Categories: facetBuckets,
		},
	}, nil
}
