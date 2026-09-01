package search

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// OpenSearchConfig defines configuration parameters for OpenSearchClient.
type OpenSearchConfig struct {
	URL      string
	Index    string
	Username string
	Password string
	Timeout  time.Duration
}

// OpenSearchClient communicates with an OpenSearch cluster via HTTP.
type OpenSearchClient struct {
	baseURL    string
	indexName  string
	username   string
	password   string
	httpClient *http.Client
	logger     *slog.Logger
}

// NewOpenSearchClient constructs a new OpenSearchClient.
func NewOpenSearchClient(cfg OpenSearchConfig, logger *slog.Logger) (*OpenSearchClient, error) {
	if logger == nil {
		logger = slog.Default()
	}

	baseURL := strings.TrimRight(cfg.URL, "/")
	if baseURL == "" {
		return nil, fmt.Errorf("opensearch url is required")
	}

	indexName := cfg.Index
	if indexName == "" {
		indexName = "shopflow_products"
	}

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}

	client := &http.Client{
		Timeout: timeout,
	}

	return &OpenSearchClient{
		baseURL:    baseURL,
		indexName:  indexName,
		username:   cfg.Username,
		password:   cfg.Password,
		httpClient: client,
		logger:     logger,
	}, nil
}

func (c *OpenSearchClient) setAuthAndHeaders(req *http.Request, contentType string) {
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if c.username != "" || c.password != "" {
		req.SetBasicAuth(c.username, c.password)
	}
}

// Ping verifies cluster health.
func (c *OpenSearchClient) Ping(ctx context.Context) error {
	reqURL := fmt.Sprintf("%s/_cluster/health", c.baseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create ping request: %w", err)
	}
	c.setAuthAndHeaders(req, "")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("opensearch ping failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("opensearch ping returned status %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

const defaultIndexMapping = `{
  "settings": {
    "number_of_shards": 1,
    "number_of_replicas": 0,
    "analysis": {
      "analyzer": {
        "shopflow_analyzer": {
          "type": "standard"
        }
      }
    }
  },
  "mappings": {
    "properties": {
      "id": { "type": "keyword" },
      "sku": { "type": "keyword" },
      "title": {
        "type": "text",
        "analyzer": "shopflow_analyzer",
        "fields": {
          "keyword": { "type": "keyword", "ignore_above": 256 }
        }
      },
      "description": {
        "type": "text",
        "analyzer": "shopflow_analyzer"
      },
      "category_id": { "type": "keyword" },
      "category_name": { "type": "keyword" },
      "price_minor": { "type": "long" },
      "currency": { "type": "keyword" },
      "in_stock": { "type": "boolean" }
    }
  }
}`

// EnsureIndex checks if the index exists and creates it with the schema mapping if not.
func (c *OpenSearchClient) EnsureIndex(ctx context.Context) error {
	checkURL := fmt.Sprintf("%s/%s", c.baseURL, c.indexName)
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, checkURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create check index request: %w", err)
	}
	c.setAuthAndHeaders(req, "")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("check index failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		return nil
	}

	if resp.StatusCode == http.StatusNotFound {
		// Index does not exist, create it
		createReq, err := http.NewRequestWithContext(ctx, http.MethodPut, checkURL, strings.NewReader(defaultIndexMapping))
		if err != nil {
			return fmt.Errorf("failed to create put index request: %w", err)
		}
		c.setAuthAndHeaders(createReq, "application/json")

		createResp, err := c.httpClient.Do(createReq)
		if err != nil {
			return fmt.Errorf("create index failed: %w", err)
		}
		defer createResp.Body.Close()

		if createResp.StatusCode < 200 || createResp.StatusCode >= 300 {
			body, _ := io.ReadAll(createResp.Body)
			return fmt.Errorf("create index returned status %d: %s", createResp.StatusCode, string(body))
		}
		return nil
	}

	return fmt.Errorf("unexpected status %d checking index %s", resp.StatusCode, c.indexName)
}

// IndexProduct indexes or updates a single product document.
func (c *OpenSearchClient) IndexProduct(ctx context.Context, doc ProductDocument) error {
	bodyBytes, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("failed to marshal product document: %w", err)
	}

	docURL := fmt.Sprintf("%s/%s/_doc/%s", c.baseURL, c.indexName, doc.ID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, docURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return fmt.Errorf("failed to create index product request: %w", err)
	}
	c.setAuthAndHeaders(req, "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to index product: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("index product returned status %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

// DeleteProduct removes a product from the search index.
func (c *OpenSearchClient) DeleteProduct(ctx context.Context, id string) error {
	docURL := fmt.Sprintf("%s/%s/_doc/%s", c.baseURL, c.indexName, id)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, docURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create delete product request: %w", err)
	}
	c.setAuthAndHeaders(req, "")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to delete product: %w", err)
	}
	defer resp.Body.Close()

	if (resp.StatusCode >= 200 && resp.StatusCode < 300) || resp.StatusCode == http.StatusNotFound {
		return nil
	}
	body, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("delete product returned status %d: %s", resp.StatusCode, string(body))
}

// BulkIndex indexes multiple product documents in a single bulk request.
func (c *OpenSearchClient) BulkIndex(ctx context.Context, docs []ProductDocument) error {
	if len(docs) == 0 {
		return nil
	}

	var buf bytes.Buffer
	for _, doc := range docs {
		actionLine := map[string]map[string]string{
			"index": {
				"_index": c.indexName,
				"_id":    doc.ID,
			},
		}
		actionBytes, err := json.Marshal(actionLine)
		if err != nil {
			return fmt.Errorf("failed to marshal bulk action header: %w", err)
		}
		docBytes, err := json.Marshal(doc)
		if err != nil {
			return fmt.Errorf("failed to marshal bulk doc payload: %w", err)
		}

		buf.Write(actionBytes)
		buf.WriteByte('\n')
		buf.Write(docBytes)
		buf.WriteByte('\n')
	}

	bulkURL := fmt.Sprintf("%s/_bulk", c.baseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, bulkURL, &buf)
	if err != nil {
		return fmt.Errorf("failed to create bulk index request: %w", err)
	}
	c.setAuthAndHeaders(req, "application/x-ndjson")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("bulk index failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("bulk index returned status %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

// Search executes a search query against OpenSearch.
func (c *OpenSearchClient) Search(ctx context.Context, q Query) (*SearchResult, error) {
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
	from := (page - 1) * pageSize

	// Construct OpenSearch bool query
	mustClauses := make([]map[string]any, 0)
	queryText := strings.TrimSpace(q.Text)
	if queryText != "" {
		mustClauses = append(mustClauses, map[string]any{
			"multi_match": map[string]any{
				"query":  queryText,
				"fields": []string{"title^3", "sku^2", "description"},
				"type":   "best_fields",
			},
		})
	} else {
		mustClauses = append(mustClauses, map[string]any{
			"match_all": map[string]any{},
		})
	}

	filterClauses := make([]map[string]any, 0)
	if q.CategoryID != nil {
		filterClauses = append(filterClauses, map[string]any{
			"term": map[string]any{
				"category_id": *q.CategoryID,
			},
		})
	}

	if q.MinPriceMinor != nil || q.MaxPriceMinor != nil {
		rangeMap := make(map[string]any)
		if q.MinPriceMinor != nil {
			rangeMap["gte"] = *q.MinPriceMinor
		}
		if q.MaxPriceMinor != nil {
			rangeMap["lte"] = *q.MaxPriceMinor
		}
		filterClauses = append(filterClauses, map[string]any{
			"range": map[string]any{
				"price_minor": rangeMap,
			},
		})
	}

	if q.InStockOnly != nil {
		filterClauses = append(filterClauses, map[string]any{
			"term": map[string]any{
				"in_stock": *q.InStockOnly,
			},
		})
	}

	searchBody := map[string]any{
		"from": from,
		"size": pageSize,
		"query": map[string]any{
			"bool": map[string]any{
				"must":   mustClauses,
				"filter": filterClauses,
			},
		},
		"aggs": map[string]any{
			"categories": map[string]any{
				"terms": map[string]any{
					"field": "category_name",
					"size":  50,
				},
			},
		},
	}

	reqBytes, err := json.Marshal(searchBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal search query: %w", err)
	}

	searchURL := fmt.Sprintf("%s/%s/_search", c.baseURL, c.indexName)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, searchURL, bytes.NewReader(reqBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to create search request: %w", err)
	}
	c.setAuthAndHeaders(req, "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("search request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("search returned status %d: %s", resp.StatusCode, string(body))
	}

	var osResp struct {
		Hits struct {
			Total any `json:"total"`
			Hits  []struct {
				Source ProductDocument `json:"_source"`
			} `json:"hits"`
		} `json:"hits"`
		Aggregations struct {
			Categories struct {
				Buckets []struct {
					Key      string `json:"key"`
					DocCount int64  `json:"doc_count"`
				} `json:"buckets"`
			} `json:"categories"`
		} `json:"aggregations"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&osResp); err != nil {
		return nil, fmt.Errorf("failed to decode search response: %w", err)
	}

	var totalHits int64
	switch v := osResp.Hits.Total.(type) {
	case float64:
		totalHits = int64(v)
	case map[string]any:
		if val, ok := v["value"].(float64); ok {
			totalHits = int64(val)
		}
	}

	products := make([]ProductDocument, len(osResp.Hits.Hits))
	for i, hit := range osResp.Hits.Hits {
		products[i] = hit.Source
	}

	facetBuckets := make([]FacetBucket, len(osResp.Aggregations.Categories.Buckets))
	for i, b := range osResp.Aggregations.Categories.Buckets {
		facetBuckets[i] = FacetBucket{
			Key:   b.Key,
			Count: b.DocCount,
		}
	}

	totalPages := 0
	if totalHits > 0 {
		totalPages = int((totalHits + int64(pageSize) - 1) / int64(pageSize))
	}

	return &SearchResult{
		TotalHits:  totalHits,
		Page:       page,
		PageSize:   pageSize,
		TotalPages: totalPages,
		Products:   products,
		Facets: Facets{
			Categories: facetBuckets,
		},
	}, nil
}
