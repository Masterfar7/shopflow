package catalog_test

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"shopflow/internal/domain/catalog"
	"shopflow/internal/domain/money"
	"shopflow/internal/platform/search"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type memoryCatalogRepo struct {
	mu         sync.RWMutex
	products   map[uuid.UUID]*catalog.Product
	skuIndex   map[string]uuid.UUID
	categories map[uuid.UUID]*catalog.Category
	slugIndex  map[string]uuid.UUID
}

func newMemoryCatalogRepo() *memoryCatalogRepo {
	return &memoryCatalogRepo{
		products:   make(map[uuid.UUID]*catalog.Product),
		skuIndex:   make(map[string]uuid.UUID),
		categories: make(map[uuid.UUID]*catalog.Category),
		slugIndex:  make(map[string]uuid.UUID),
	}
}

func (m *memoryCatalogRepo) CreateProduct(ctx context.Context, p *catalog.Product) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.skuIndex[p.SKU]; exists {
		return catalog.ErrDuplicateSKU
	}
	if p.CategoryID != nil {
		if _, exists := m.categories[*p.CategoryID]; !exists {
			return catalog.ErrCategoryNotFound
		}
	}

	p.CreatedAt = time.Now().UTC()
	p.UpdatedAt = p.CreatedAt
	p.PriceMinor = p.Price.Amount
	p.Currency = p.Price.Currency

	m.products[p.ID] = p
	m.skuIndex[p.SKU] = p.ID
	return nil
}

func (m *memoryCatalogRepo) GetProductByID(ctx context.Context, id uuid.UUID) (*catalog.Product, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	p, exists := m.products[id]
	if !exists {
		return nil, catalog.ErrProductNotFound
	}
	cp := *p
	return &cp, nil
}

func (m *memoryCatalogRepo) GetProductBySKU(ctx context.Context, sku string) (*catalog.Product, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	id, exists := m.skuIndex[sku]
	if !exists {
		return nil, catalog.ErrProductNotFound
	}
	p := m.products[id]
	cp := *p
	return &cp, nil
}

func (m *memoryCatalogRepo) GetProductsBySKUs(ctx context.Context, skus []string) ([]catalog.Product, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var result []catalog.Product
	for _, sku := range skus {
		if id, exists := m.skuIndex[sku]; exists {
			result = append(result, *m.products[id])
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].SKU < result[j].SKU
	})
	return result, nil
}

func (m *memoryCatalogRepo) ListProducts(ctx context.Context, params catalog.ListProductsParams) ([]catalog.Product, string, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var list []catalog.Product
	for _, p := range m.products {
		if params.CategoryID != nil && (p.CategoryID == nil || *p.CategoryID != *params.CategoryID) {
			continue
		}
		if params.IsActive != nil && p.IsActive != *params.IsActive {
			continue
		}
		list = append(list, *p)
	}

	sort.Slice(list, func(i, j int) bool {
		if list[i].CreatedAt.Equal(list[j].CreatedAt) {
			return list[i].ID.String() > list[j].ID.String()
		}
		return list[i].CreatedAt.After(list[j].CreatedAt)
	})

	limit := params.Limit
	if limit <= 0 {
		limit = 20
	}
	hasMore := len(list) > limit
	if hasMore {
		list = list[:limit]
	}
	return list, "", hasMore, nil
}

func (m *memoryCatalogRepo) UpdateProduct(ctx context.Context, id uuid.UUID, title, description string, categoryID *uuid.UUID, isActive bool, expectedVersion int64) (*catalog.Product, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	p, exists := m.products[id]
	if !exists {
		return nil, catalog.ErrProductNotFound
	}
	if p.Version != expectedVersion {
		return nil, catalog.ErrOptimisticLockConflict
	}
	if categoryID != nil {
		if _, catExists := m.categories[*categoryID]; !catExists {
			return nil, catalog.ErrCategoryNotFound
		}
	}

	p.Title = title
	p.Description = description
	p.CategoryID = categoryID
	p.IsActive = isActive
	p.Version++
	p.UpdatedAt = time.Now().UTC()

	cp := *p
	return &cp, nil
}

func (m *memoryCatalogRepo) UpdateProductPrice(ctx context.Context, id uuid.UUID, priceMinor int64, currency string, expectedVersion int64) (*catalog.Product, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	p, exists := m.products[id]
	if !exists {
		return nil, catalog.ErrProductNotFound
	}
	if p.Version != expectedVersion {
		return nil, catalog.ErrOptimisticLockConflict
	}

	p.PriceMinor = priceMinor
	p.Currency = currency
	p.Price = money.Money{Amount: priceMinor, Currency: currency}
	p.Version++
	p.UpdatedAt = time.Now().UTC()

	cp := *p
	return &cp, nil
}

func (m *memoryCatalogRepo) CreateCategory(ctx context.Context, c *catalog.Category) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.slugIndex[c.Slug]; exists {
		return catalog.ErrDuplicateSlug
	}
	c.CreatedAt = time.Now().UTC()
	c.UpdatedAt = c.CreatedAt
	m.categories[c.ID] = c
	m.slugIndex[c.Slug] = c.ID
	return nil
}

func (m *memoryCatalogRepo) GetCategoryByID(ctx context.Context, id uuid.UUID) (*catalog.Category, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	c, exists := m.categories[id]
	if !exists {
		return nil, catalog.ErrCategoryNotFound
	}
	cp := *c
	return &cp, nil
}

func (m *memoryCatalogRepo) ListCategories(ctx context.Context) ([]catalog.Category, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var result []catalog.Category
	for _, c := range m.categories {
		result = append(result, *c)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Name < result[j].Name
	})
	return result, nil
}

func (m *memoryCatalogRepo) AddProductImage(ctx context.Context, img *catalog.ProductImage) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	p, exists := m.products[img.ProductID]
	if !exists {
		return catalog.ErrProductNotFound
	}
	if img.ID == uuid.Nil {
		img.ID = uuid.New()
	}
	if img.IsPrimary {
		for i := range p.Images {
			p.Images[i].IsPrimary = false
		}
	}
	img.CreatedAt = time.Now().UTC()
	p.Images = append(p.Images, *img)
	return nil
}

func (m *memoryCatalogRepo) GetProductImages(ctx context.Context, productID uuid.UUID) ([]catalog.ProductImage, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	p, exists := m.products[productID]
	if !exists {
		return nil, catalog.ErrProductNotFound
	}
	res := make([]catalog.ProductImage, len(p.Images))
	copy(res, p.Images)
	sort.Slice(res, func(i, j int) bool {
		if res[i].SortOrder == res[j].SortOrder {
			return res[i].CreatedAt.Before(res[j].CreatedAt)
		}
		return res[i].SortOrder < res[j].SortOrder
	})
	return res, nil
}

func (m *memoryCatalogRepo) GetProductImageByID(ctx context.Context, productID, imageID uuid.UUID) (*catalog.ProductImage, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	p, exists := m.products[productID]
	if !exists {
		return nil, catalog.ErrProductNotFound
	}
	for _, img := range p.Images {
		if img.ID == imageID {
			cp := img
			return &cp, nil
		}
	}
	return nil, catalog.ErrProductImageNotFound
}

func (m *memoryCatalogRepo) DeleteProductImage(ctx context.Context, productID, imageID uuid.UUID) (*catalog.ProductImage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	p, exists := m.products[productID]
	if !exists {
		return nil, catalog.ErrProductNotFound
	}
	for i, img := range p.Images {
		if img.ID == imageID {
			deleted := img
			p.Images = append(p.Images[:i], p.Images[i+1:]...)
			return &deleted, nil
		}
	}
	return nil, catalog.ErrProductImageNotFound
}


func TestCatalogService_CreateProduct_Success(t *testing.T) {
	repo := newMemoryCatalogRepo()
	svc := catalog.NewService(repo, nil)

	price, err := money.New(2999, "USD")
	require.NoError(t, err)

	p, err := svc.CreateProduct(context.Background(), catalog.CreateProductRequest{
		SKU:         "SKU-SHIRT-BLUE",
		Title:       "Blue Cotton Shirt",
		Description: "Comfortable organic cotton shirt",
		Price:       price,
	})
	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, p.ID)
	assert.Equal(t, "SKU-SHIRT-BLUE", p.SKU)
	assert.Equal(t, int64(2999), p.Price.Amount)
	assert.Equal(t, "USD", p.Price.Currency)
	assert.Equal(t, int64(1), p.Version)
	assert.True(t, p.IsActive)
}

func TestCatalogService_CreateProduct_DuplicateSKU(t *testing.T) {
	repo := newMemoryCatalogRepo()
	svc := catalog.NewService(repo, nil)

	price, _ := money.New(1000, "USD")
	req := catalog.CreateProductRequest{
		SKU:   "SKU-UNIQUE-1",
		Title: "Item 1",
		Price: price,
	}

	_, err := svc.CreateProduct(context.Background(), req)
	require.NoError(t, err)

	_, err = svc.CreateProduct(context.Background(), req)
	require.ErrorIs(t, err, catalog.ErrDuplicateSKU)
}

func TestCatalogService_CreateProduct_Validation(t *testing.T) {
	repo := newMemoryCatalogRepo()
	svc := catalog.NewService(repo, nil)
	validPrice, _ := money.New(100, "USD")

	// Invalid SKU
	_, err := svc.CreateProduct(context.Background(), catalog.CreateProductRequest{
		SKU:   "AB", // too short
		Title: "Item",
		Price: validPrice,
	})
	require.ErrorIs(t, err, catalog.ErrInvalidSKU)

	// Invalid Title
	_, err = svc.CreateProduct(context.Background(), catalog.CreateProductRequest{
		SKU:   "SKU-VALID",
		Title: "   ",
		Price: validPrice,
	})
	require.ErrorIs(t, err, catalog.ErrInvalidTitle)

	// Invalid Price (zero amount)
	zeroPrice := money.Money{Amount: 0, Currency: "USD"}
	_, err = svc.CreateProduct(context.Background(), catalog.CreateProductRequest{
		SKU:   "SKU-VALID",
		Title: "Valid Title",
		Price: zeroPrice,
	})
	require.ErrorIs(t, err, catalog.ErrInvalidPrice)
}

func TestCatalogService_UpdateProduct_OCC(t *testing.T) {
	repo := newMemoryCatalogRepo()
	svc := catalog.NewService(repo, nil)

	price, _ := money.New(1500, "USD")
	p, err := svc.CreateProduct(context.Background(), catalog.CreateProductRequest{
		SKU:   "SKU-OCC-TEST",
		Title: "Original Title",
		Price: price,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(1), p.Version)

	// Successful update with matching version
	updated, err := svc.UpdateProduct(context.Background(), p.ID, catalog.UpdateProductRequest{
		Title:       "Updated Title",
		Description: "Updated Description",
		IsActive:    true,
	}, 1)
	require.NoError(t, err)
	assert.Equal(t, int64(2), updated.Version)
	assert.Equal(t, "Updated Title", updated.Title)

	// Conflicting update with stale version
	_, err = svc.UpdateProduct(context.Background(), p.ID, catalog.UpdateProductRequest{
		Title: "Stale Title",
	}, 1)
	require.ErrorIs(t, err, catalog.ErrOptimisticLockConflict)
}

func TestCatalogService_UpdateProductPrice_OCC(t *testing.T) {
	repo := newMemoryCatalogRepo()
	svc := catalog.NewService(repo, nil)

	p, err := svc.CreateProduct(context.Background(), catalog.CreateProductRequest{
		SKU:   "SKU-PRICE-TEST",
		Title: "Price Item",
		Price: money.Money{Amount: 500, Currency: "USD"},
	})
	require.NoError(t, err)

	newPrice, _ := money.New(750, "USD")
	updated, err := svc.UpdateProductPrice(context.Background(), p.ID, catalog.UpdatePriceRequest{
		Price: newPrice,
	}, 1)
	require.NoError(t, err)
	assert.Equal(t, int64(750), updated.Price.Amount)
	assert.Equal(t, int64(2), updated.Version)

	// Stale version
	_, err = svc.UpdateProductPrice(context.Background(), p.ID, catalog.UpdatePriceRequest{
		Price: newPrice,
	}, 1)
	require.ErrorIs(t, err, catalog.ErrOptimisticLockConflict)
}

func TestCatalogService_GetProductsBySKUs_SortedAscending(t *testing.T) {
	repo := newMemoryCatalogRepo()
	svc := catalog.NewService(repo, nil)

	skus := []string{"SKU-Z", "SKU-A", "SKU-M", "SKU-B"}
	for _, sku := range skus {
		_, err := svc.CreateProduct(context.Background(), catalog.CreateProductRequest{
			SKU:   sku,
			Title: fmt.Sprintf("Title for %s", sku),
			Price: money.Money{Amount: 1000, Currency: "USD"},
		})
		require.NoError(t, err)
	}

	products, err := svc.GetProductsBySKUs(context.Background(), []string{"SKU-Z", "SKU-A", "SKU-B"})
	require.NoError(t, err)
	require.Len(t, products, 3)
	assert.Equal(t, "SKU-A", products[0].SKU)
	assert.Equal(t, "SKU-B", products[1].SKU)
	assert.Equal(t, "SKU-Z", products[2].SKU)
}

func TestCatalogService_ProductImages(t *testing.T) {
	ctx := context.Background()
	repo := newMemoryCatalogRepo()
	svc := catalog.NewService(repo, nil)

	// Attempt adding image to non-existent product
	_, err := svc.AddProductImage(ctx, uuid.New(), "key1", "http://url1", "image/jpeg", 1024, true, 0)
	assert.ErrorIs(t, err, catalog.ErrProductNotFound)

	// Create real product
	p, err := svc.CreateProduct(ctx, catalog.CreateProductRequest{
		SKU:   "SKU-IMG-SVC",
		Title: "Image Test Product",
		Price: money.Money{Amount: 1000, Currency: "USD"},
	})
	require.NoError(t, err)

	// Add primary image
	img1, err := svc.AddProductImage(ctx, p.ID, "key1", "http://url1", "image/jpeg", 1024, true, 2)
	require.NoError(t, err)
	assert.True(t, img1.IsPrimary)
	assert.Equal(t, p.ID, img1.ProductID)

	// Add second image (primary = true, should unset first image)
	img2, err := svc.AddProductImage(ctx, p.ID, "key2", "http://url2", "image/png", 2048, true, 1)
	require.NoError(t, err)
	assert.True(t, img2.IsPrimary)

	// List images (should be sorted by sort_order ASC: img2 (1) before img1 (2))
	images, err := svc.GetProductImages(ctx, p.ID)
	require.NoError(t, err)
	require.Len(t, images, 2)
	assert.Equal(t, img2.ID, images[0].ID)
	assert.Equal(t, img1.ID, images[1].ID)
	assert.False(t, images[1].IsPrimary) // unset when img2 was set primary

	// Get image by ID
	fetched, err := svc.GetProductImageByID(ctx, p.ID, img1.ID)
	require.NoError(t, err)
	assert.Equal(t, img1.StorageKey, fetched.StorageKey)

	// Delete image
	deleted, err := svc.DeleteProductImage(ctx, p.ID, img1.ID)
	require.NoError(t, err)
	assert.Equal(t, img1.ID, deleted.ID)

	// Delete again returns ErrProductImageNotFound
	_, err = svc.DeleteProductImage(ctx, p.ID, img1.ID)
	assert.ErrorIs(t, err, catalog.ErrProductImageNotFound)

	// List after delete has 1 image
	images, err = svc.GetProductImages(ctx, p.ID)
	require.NoError(t, err)
	require.Len(t, images, 1)
	assert.Equal(t, img2.ID, images[0].ID)
}

func TestService_SearchAndSync(t *testing.T) {
	ctx := context.Background()
	repo := newMemoryCatalogRepo()
	searchClient := search.NewMemoryClient()
	svc := catalog.NewServiceWithSearch(repo, searchClient, nil)

	// Create category
	cat, err := svc.CreateCategory(ctx, catalog.CreateCategoryRequest{
		Slug: "keyboards",
		Name: "Keyboards",
	})
	require.NoError(t, err)

	// Create product -> verify synced to search
	p, err := svc.CreateProduct(ctx, catalog.CreateProductRequest{
		SKU:         "SKU-KB-1",
		Title:       "Mechanical Keyboard RGB",
		Description: "Compact mechanical keyboard with blue switches",
		CategoryID:  &cat.ID,
		Price:       money.Money{Amount: 8900, Currency: "USD"},
	})
	require.NoError(t, err)

	// Search product
	res, err := svc.SearchProducts(ctx, search.Query{Text: "Mechanical"})
	require.NoError(t, err)
	require.Equal(t, int64(1), res.TotalHits)
	assert.Equal(t, p.ID.String(), res.Products[0].ID)
	assert.Equal(t, "Keyboards", res.Products[0].CategoryName)
	assert.Equal(t, int64(8900), res.Products[0].PriceMinor)

	// Update product -> verify search updated
	updatedP, err := svc.UpdateProduct(ctx, p.ID, catalog.UpdateProductRequest{
		Title:       "Wireless Mechanical Keyboard RGB",
		Description: "Bluetooth mechanical keyboard",
		CategoryID:  &cat.ID,
		IsActive:    true,
	}, p.Version)
	require.NoError(t, err)

	resUpdated, err := svc.SearchProducts(ctx, search.Query{Text: "Wireless"})
	require.NoError(t, err)
	require.Equal(t, int64(1), resUpdated.TotalHits)
	assert.Equal(t, "Wireless Mechanical Keyboard RGB", resUpdated.Products[0].Title)

	// Update price -> verify search updated
	_, err = svc.UpdateProductPrice(ctx, p.ID, catalog.UpdatePriceRequest{
		Price: money.Money{Amount: 9900, Currency: "USD"},
	}, updatedP.Version)
	require.NoError(t, err)

	resPrice, err := svc.SearchProducts(ctx, search.Query{Text: "Wireless"})
	require.NoError(t, err)
	require.Equal(t, int64(1), resPrice.TotalHits)
	assert.Equal(t, int64(9900), resPrice.Products[0].PriceMinor)
}

func TestService_ReindexAll(t *testing.T) {
	ctx := context.Background()
	repo := newMemoryCatalogRepo()
	searchClient := search.NewMemoryClient()
	svc := catalog.NewServiceWithSearch(repo, searchClient, nil)

	cat, err := svc.CreateCategory(ctx, catalog.CreateCategoryRequest{
		Slug: "monitors",
		Name: "Monitors",
	})
	require.NoError(t, err)

	// Create 5 products
	for i := 1; i <= 5; i++ {
		_, err := svc.CreateProduct(ctx, catalog.CreateProductRequest{
			SKU:         fmt.Sprintf("SKU-MON-%d", i),
			Title:       fmt.Sprintf("4K Monitor %d", i),
			Description: "Ultra HD gaming monitor",
			CategoryID:  &cat.ID,
			Price:       money.Money{Amount: int64(20000 + i*1000), Currency: "USD"},
		})
		require.NoError(t, err)
	}

	// Now wipe search client to test ReindexAll
	freshSearchClient := search.NewMemoryClient()
	svc.SetSearchClient(freshSearchClient)

	// Verify freshSearchClient is empty
	resEmpty, err := svc.SearchProducts(ctx, search.Query{})
	require.NoError(t, err)
	assert.Equal(t, int64(0), resEmpty.TotalHits)

	// Run ReindexAll
	indexedCount, err := svc.ReindexAll(ctx)
	require.NoError(t, err)
	assert.Equal(t, 5, indexedCount)

	// Verify all 5 products are present in search
	resAll, err := svc.SearchProducts(ctx, search.Query{})
	require.NoError(t, err)
	assert.Equal(t, int64(5), resAll.TotalHits)
	require.Len(t, resAll.Facets.Categories, 1)
	assert.Equal(t, "Monitors", resAll.Facets.Categories[0].Key)
	assert.Equal(t, int64(5), resAll.Facets.Categories[0].Count)
}

