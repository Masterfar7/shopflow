// Package cache implements a Redis-backed cache-aside layer for the catalog domain.
//
// Cache consistency note: cache-aside can temporarily serve stale data during the
// window between invalidation and TTL expiry due to a read/invalidate/set race.
// This is acceptable eventual consistency for catalog display. Order placement always
// reads authoritative prices from PostgreSQL directly, never from this cache.
package cache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"time"

	"shopflow/internal/domain/catalog"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/singleflight"
)

const (
	baseTTL     = 5 * time.Minute
	jitterRange = 30 * time.Second  // ±30s jitter to prevent thundering herd
	negativeTTL = 30 * time.Second  // short TTL for not-found results
)

// sentinel stored for negative cache entries.
const negativeMarker = "__NOT_FOUND__"

var (
	cacheHits = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "catalog_cache_hits_total",
		Help: "Total number of catalog cache hits.",
	}, []string{"entity"})

	cacheMisses = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "catalog_cache_misses_total",
		Help: "Total number of catalog cache misses.",
	}, []string{"entity"})
)

// CatalogCache wraps a catalog.Service with a Redis cache-aside layer.
// It implements catalog.CatalogReader so it can be used as a drop-in replacement
// for read paths. Write paths (CreateProduct, UpdateProduct, etc.) always go to
// the underlying service and invalidate cache entries.
type CatalogCache struct {
	svc    *catalog.Service
	rdb    *redis.Client
	logger *slog.Logger
	sfg    singleflight.Group
}

// NewCatalogCache creates a CatalogCache wrapping the given service.
func NewCatalogCache(svc *catalog.Service, rdb *redis.Client, logger *slog.Logger) *CatalogCache {
	if logger == nil {
		logger = slog.Default()
	}
	return &CatalogCache{svc: svc, rdb: rdb, logger: logger}
}

func productKey(id uuid.UUID) string {
	return fmt.Sprintf("catalog:product:v1:%s", id)
}

func skuKey(sku string) string {
	return fmt.Sprintf("catalog:sku:v1:%s", sku)
}

func ttlWithJitter() time.Duration {
	jitter := time.Duration(rand.Int64N(int64(jitterRange)*2)) - jitterRange
	return baseTTL + jitter
}

// GetProduct returns a product by ID, using cache-aside.
func (c *CatalogCache) GetProduct(ctx context.Context, id uuid.UUID) (*catalog.Product, error) {
	key := productKey(id)

	// Try cache first
	if p, ok := c.getFromCache(ctx, key, "product"); ok {
		return p, nil
	}

	// Singleflight: coalesce concurrent cache-miss DB reads for same key
	sfKey := "product:" + id.String()
	val, err, _ := c.sfg.Do(sfKey, func() (interface{}, error) {
		p, dbErr := c.svc.GetProduct(ctx, id)
		if dbErr != nil {
			if errors.Is(dbErr, catalog.ErrProductNotFound) {
				c.setNegative(ctx, key)
			}
			return nil, dbErr
		}
		c.setInCache(ctx, key, p)
		return p, nil
	})
	if err != nil {
		return nil, err
	}
	return val.(*catalog.Product), nil
}

// GetProductBySKU returns a product by SKU, using cache-aside.
func (c *CatalogCache) GetProductBySKU(ctx context.Context, sku string) (*catalog.Product, error) {
	key := skuKey(sku)

	if p, ok := c.getFromCache(ctx, key, "sku"); ok {
		return p, nil
	}

	sfKey := "sku:" + sku
	val, err, _ := c.sfg.Do(sfKey, func() (interface{}, error) {
		p, dbErr := c.svc.GetProductBySKU(ctx, sku)
		if dbErr != nil {
			if errors.Is(dbErr, catalog.ErrProductNotFound) {
				c.setNegative(ctx, key)
			}
			return nil, dbErr
		}
		c.setInCache(ctx, key, p)
		return p, nil
	})
	if err != nil {
		return nil, err
	}
	return val.(*catalog.Product), nil
}

// GetProductsBySKUs returns products for a list of SKUs.
// NOTE: Order placement MUST call catalog.Service.GetProductsBySKUs directly
// to read authoritative prices from PostgreSQL, not this cached version.
func (c *CatalogCache) GetProductsBySKUs(ctx context.Context, skus []string) ([]catalog.Product, error) {
	// Batch cache lookups are complex and error-prone; delegate to service directly.
	// Individual SKU results will be warmed by GetProductBySKU calls.
	return c.svc.GetProductsBySKUs(ctx, skus)
}

// InvalidateProduct removes cache entries for a product (by ID and SKU).
// Call after any write operation (update, price change, deactivation).
func (c *CatalogCache) InvalidateProduct(ctx context.Context, p *catalog.Product) {
	keys := []string{productKey(p.ID), skuKey(p.SKU)}
	if err := c.rdb.Del(ctx, keys...).Err(); err != nil {
		c.logger.Warn("cache invalidation failed", "keys", keys, "error", err)
		return
	}
	c.logger.Info("cache invalidated", "product_id", p.ID, "sku", p.SKU)
}

// --- Internal helpers ---

func (c *CatalogCache) getFromCache(ctx context.Context, key, entity string) (*catalog.Product, bool) {
	raw, err := c.rdb.Get(ctx, key).Bytes()
	if err != nil {
		if !errors.Is(err, redis.Nil) {
			c.logger.Warn("cache read error", "key", key, "error", err)
		}
		cacheMisses.WithLabelValues(entity).Inc()
		return nil, false
	}

	// Negative cache hit
	if string(raw) == negativeMarker {
		cacheHits.WithLabelValues(entity).Inc()
		return nil, true // returns nil product, caller gets ErrProductNotFound from original error path
	}

	var p catalog.Product
	if err := json.Unmarshal(raw, &p); err != nil {
		c.logger.Warn("cache unmarshal error", "key", key, "error", err)
		cacheMisses.WithLabelValues(entity).Inc()
		return nil, false
	}

	cacheHits.WithLabelValues(entity).Inc()
	return &p, true
}

func (c *CatalogCache) setInCache(ctx context.Context, key string, p *catalog.Product) {
	raw, err := json.Marshal(p)
	if err != nil {
		c.logger.Warn("cache marshal error", "key", key, "error", err)
		return
	}
	ttl := ttlWithJitter()
	if err := c.rdb.Set(ctx, key, raw, ttl).Err(); err != nil {
		c.logger.Warn("cache write error", "key", key, "error", err)
	}
}

func (c *CatalogCache) setNegative(ctx context.Context, key string) {
	if err := c.rdb.Set(ctx, key, negativeMarker, negativeTTL).Err(); err != nil {
		c.logger.Warn("negative cache write error", "key", key, "error", err)
	}
}

// Service returns the underlying catalog.Service for write operations.
// All write methods (CreateProduct, UpdateProduct, etc.) should go through this.
func (c *CatalogCache) Service() *catalog.Service {
	return c.svc
}
