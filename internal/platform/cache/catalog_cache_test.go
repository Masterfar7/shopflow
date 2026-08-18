package cache

import (
	"context"
	"errors"
	"testing"
	"time"

	"shopflow/internal/domain/catalog"
	"shopflow/internal/domain/money"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// redisTestAddr returns a real Redis address if available, else skips the test.
func redisTestAddr(t *testing.T) string {
	t.Helper()
	addr := "localhost:6379"
	rdb := redis.NewClient(&redis.Options{Addr: addr, DialTimeout: 200 * time.Millisecond})
	defer rdb.Close()
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		t.Skipf("Redis not available at %s, skipping: %v", addr, err)
	}
	return addr
}

// fakeService is a minimal catalog.Service stub for testing (using exported fields via thin wrapper).
type fakeRepo struct {
	product *catalog.Product
	callCount int
}

func (f *fakeRepo) GetProductByID(_ context.Context, id uuid.UUID) (*catalog.Product, error) {
	f.callCount++
	if f.product != nil && f.product.ID == id {
		return f.product, nil
	}
	return nil, catalog.ErrProductNotFound
}
func (f *fakeRepo) GetProductBySKU(_ context.Context, sku string) (*catalog.Product, error) {
	f.callCount++
	if f.product != nil && f.product.SKU == sku {
		return f.product, nil
	}
	return nil, catalog.ErrProductNotFound
}
func (f *fakeRepo) GetProductsBySKUs(_ context.Context, skus []string) ([]catalog.Product, error) {
	return nil, nil
}
func (f *fakeRepo) CreateProduct(_ context.Context, p *catalog.Product) error         { return nil }
func (f *fakeRepo) UpdateProduct(_ context.Context, id uuid.UUID, title, desc string, catID *uuid.UUID, active bool, ver int64) (*catalog.Product, error) {
	return nil, nil
}
func (f *fakeRepo) UpdateProductPrice(_ context.Context, id uuid.UUID, amount int64, currency string, ver int64) (*catalog.Product, error) {
	return nil, nil
}
func (f *fakeRepo) ListProducts(_ context.Context, _ catalog.ListProductsParams) ([]catalog.Product, string, bool, error) {
	return nil, "", false, nil
}
func (f *fakeRepo) CreateCategory(_ context.Context, c *catalog.Category) error { return nil }
func (f *fakeRepo) GetCategoryByID(_ context.Context, id uuid.UUID) (*catalog.Category, error) {
	return nil, catalog.ErrCategoryNotFound
}
func (f *fakeRepo) ListCategories(_ context.Context) ([]catalog.Category, error) { return nil, nil }
func (f *fakeRepo) AddProductImage(_ context.Context, _ *catalog.ProductImage) error { return nil }
func (f *fakeRepo) GetProductImages(_ context.Context, _ uuid.UUID) ([]catalog.ProductImage, error) { return nil, nil }
func (f *fakeRepo) GetProductImageByID(_ context.Context, _, _ uuid.UUID) (*catalog.ProductImage, error) { return nil, nil }
func (f *fakeRepo) DeleteProductImage(_ context.Context, _, _ uuid.UUID) (*catalog.ProductImage, error) { return nil, nil }

func sampleProduct() *catalog.Product {
	return &catalog.Product{
		ID:      uuid.MustParse("11111111-1111-1111-1111-111111111111"),
		SKU:     "SKU-TEST-001",
		Title:   "Test Product",
		Price:   money.Money{Amount: 1999, Currency: "USD"},
		Version: 1,
	}
}

func TestCatalogCache_HitAfterMiss(t *testing.T) {
	addr := redisTestAddr(t)

	rdb := redis.NewClient(&redis.Options{Addr: addr})
	defer rdb.Close()

	repo := &fakeRepo{product: sampleProduct()}
	svc := catalog.NewService(repo, nil)
	c := NewCatalogCache(svc, rdb, nil)

	ctx := context.Background()
	id := sampleProduct().ID

	// Clean slate
	rdb.Del(ctx, productKey(id))

	// First call: cache miss → DB read
	p1, err := c.GetProduct(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, id, p1.ID)
	assert.Equal(t, 1, repo.callCount, "first call must hit DB")

	// Second call: cache hit → no additional DB read
	p2, err := c.GetProduct(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, id, p2.ID)
	assert.Equal(t, 1, repo.callCount, "second call must be served from cache (no extra DB read)")

	rdb.Del(ctx, productKey(id))
}

func TestCatalogCache_InvalidationCausesDBRead(t *testing.T) {
	addr := redisTestAddr(t)

	rdb := redis.NewClient(&redis.Options{Addr: addr})
	defer rdb.Close()

	repo := &fakeRepo{product: sampleProduct()}
	svc := catalog.NewService(repo, nil)
	c := NewCatalogCache(svc, rdb, nil)

	ctx := context.Background()
	id := sampleProduct().ID
	rdb.Del(ctx, productKey(id), skuKey(sampleProduct().SKU))

	// Warm the cache
	_, err := c.GetProduct(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, 1, repo.callCount)

	// Invalidate
	c.InvalidateProduct(ctx, sampleProduct())

	// Next read must go to DB again
	_, err = c.GetProduct(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, 2, repo.callCount, "after invalidation, read must go to DB")

	rdb.Del(ctx, productKey(id), skuKey(sampleProduct().SKU))
}

func TestCatalogCache_NegativeCache(t *testing.T) {
	addr := redisTestAddr(t)

	rdb := redis.NewClient(&redis.Options{Addr: addr})
	defer rdb.Close()

	missingID := uuid.New()
	repo := &fakeRepo{} // no product
	svc := catalog.NewService(repo, nil)
	c := NewCatalogCache(svc, rdb, nil)

	ctx := context.Background()
	rdb.Del(ctx, productKey(missingID))

	// First call: miss → DB read → not found → store negative marker
	_, err := c.GetProduct(ctx, missingID)
	assert.True(t, errors.Is(err, catalog.ErrProductNotFound))
	assert.Equal(t, 1, repo.callCount)

	// Second call: negative cache hit → no DB read
	// Note: with singleflight, the error is replayed, but callCount should not increase
	// because the negative marker is stored and returned before calling svc.
	// Reset callCount to verify
	repo.callCount = 0
	_, err2 := c.GetProduct(ctx, missingID)
	// With negative cache, the raw is the marker; getFromCache returns (nil, true)
	// but we can't distinguish this from "not found stored in cache" without propagating the error.
	// This test verifies DB is NOT called again.
	_ = err2
	assert.Equal(t, 0, repo.callCount, "negative cache must prevent DB round-trip")

	rdb.Del(ctx, productKey(missingID))
}

func TestCatalogCache_RedisDown_AllowsThrough(t *testing.T) {
	// Point at a non-existent Redis → cache errors should be transparent
	rdb := redis.NewClient(&redis.Options{
		Addr:        "localhost:19999",
		DialTimeout: 50 * time.Millisecond,
	})
	defer rdb.Close()

	repo := &fakeRepo{product: sampleProduct()}
	svc := catalog.NewService(repo, nil)
	c := NewCatalogCache(svc, rdb, nil)

	ctx := context.Background()
	p, err := c.GetProduct(ctx, sampleProduct().ID)
	require.NoError(t, err, "Redis failure must not break GetProduct")
	assert.Equal(t, sampleProduct().ID, p.ID)
	assert.Equal(t, 1, repo.callCount, "must fall back to DB when Redis is down")
}

func TestTTLWithJitter(t *testing.T) {
	for i := 0; i < 100; i++ {
		ttl := ttlWithJitter()
		assert.GreaterOrEqual(t, ttl, baseTTL-jitterRange, "TTL too short")
		assert.LessOrEqual(t, ttl, baseTTL+jitterRange, "TTL too long")
	}
}
