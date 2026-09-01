package search

import (
	"context"
)

// ProductDocument represents an indexed product in the search engine.
// Strictly adheres to Zero Floats for Money invariant by using int64 PriceMinor.
type ProductDocument struct {
	ID           string  `json:"id"`
	SKU          string  `json:"sku"`
	Title        string  `json:"title"`
	Description  string  `json:"description"`
	CategoryID   *string `json:"category_id,omitempty"`
	CategoryName string  `json:"category_name"`
	PriceMinor   int64   `json:"price_minor"`
	Currency     string  `json:"currency"`
	InStock      bool    `json:"in_stock"`
}

// Query specifies search criteria, filters, and pagination parameters.
type Query struct {
	Text          string  // Free-text search query
	CategoryID    *string // Exact match filter on category UUID
	MinPriceMinor *int64  // Filter price_minor >= MinPriceMinor
	MaxPriceMinor *int64  // Filter price_minor <= MaxPriceMinor
	InStockOnly   *bool   // Filter in_stock == true
	Page          int     // 1-based page index (default 1)
	PageSize      int     // Number of items per page (default 20, max 100)
}

// FacetBucket represents an aggregation bucket for a facet value.
type FacetBucket struct {
	Key   string `json:"key"`
	Count int64  `json:"count"`
}

// Facets represents faceted search aggregations across categories.
type Facets struct {
	Categories []FacetBucket `json:"categories"`
}

// SearchResult contains the search query results, pagination metadata, and facets.
type SearchResult struct {
	TotalHits  int64             `json:"total_hits"`
	Page       int               `json:"page"`
	PageSize   int               `json:"page_size"`
	TotalPages int               `json:"total_pages"`
	Products   []ProductDocument `json:"products"`
	Facets     Facets            `json:"facets"`
}

// Client defines the contract for product search and indexing engines.
type Client interface {
	Ping(ctx context.Context) error
	EnsureIndex(ctx context.Context) error
	IndexProduct(ctx context.Context, doc ProductDocument) error
	DeleteProduct(ctx context.Context, id string) error
	BulkIndex(ctx context.Context, docs []ProductDocument) error
	Search(ctx context.Context, q Query) (*SearchResult, error)
}
