// internal/domain/catalog/service.go
package catalog

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"shopflow/internal/domain/money"
	"shopflow/internal/platform/search"

	"github.com/google/uuid"
)

type Service struct {
	repo         Repository
	searchClient search.Client
	logger       *slog.Logger
}

func NewService(repo Repository, logger *slog.Logger) *Service {
	return NewServiceWithSearch(repo, nil, logger)
}

func NewServiceWithSearch(repo Repository, searchClient search.Client, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		repo:         repo,
		searchClient: searchClient,
		logger:       logger,
	}
}

func (s *Service) SetSearchClient(client search.Client) {
	s.searchClient = client
}

func (s *Service) validateSKU(sku string) error {
	trimmed := strings.TrimSpace(sku)
	if len(trimmed) < 3 || len(trimmed) > 64 {
		return ErrInvalidSKU
	}
	return nil
}

func (s *Service) validateTitle(title string) error {
	trimmed := strings.TrimSpace(title)
	if len(trimmed) == 0 || len(title) > 255 {
		return ErrInvalidTitle
	}
	return nil
}

func (s *Service) validatePrice(p money.Money) error {
	if p.Amount <= 0 {
		return ErrInvalidPrice
	}
	return nil
}

func (s *Service) CreateProduct(ctx context.Context, req CreateProductRequest) (*Product, error) {
	if err := s.validateSKU(req.SKU); err != nil {
		return nil, err
	}
	if err := s.validateTitle(req.Title); err != nil {
		return nil, err
	}
	if err := s.validatePrice(req.Price); err != nil {
		return nil, err
	}

	p := &Product{
		ID:          uuid.New(),
		CategoryID:  req.CategoryID,
		SKU:         req.SKU,
		Title:       req.Title,
		Description: req.Description,
		PriceMinor:  req.Price.Amount,
		Currency:    req.Price.Currency,
		Price:       req.Price,
		IsActive:    true,
		Version:     1,
	}

	if err := s.repo.CreateProduct(ctx, p); err != nil {
		return nil, err
	}
	// Post-commit search indexing hook outside DB transaction
	if s.searchClient != nil {
		doc := s.buildProductDocument(ctx, p)
		if err := s.searchClient.IndexProduct(ctx, doc); err != nil {
			s.logger.Warn("failed to sync product to search index", "product_id", p.ID, "err", err)
		}
	}
	return p, nil
}

// GetProduct implements CatalogReader.
func (s *Service) GetProduct(ctx context.Context, id uuid.UUID) (*Product, error) {
	return s.repo.GetProductByID(ctx, id)
}

// GetProductBySKU implements CatalogReader.
func (s *Service) GetProductBySKU(ctx context.Context, sku string) (*Product, error) {
	if err := s.validateSKU(sku); err != nil {
		return nil, err
	}
	return s.repo.GetProductBySKU(ctx, sku)
}

// GetProductsBySKUs implements CatalogReader with deterministic ascending SKU sort.
func (s *Service) GetProductsBySKUs(ctx context.Context, skus []string) ([]Product, error) {
	if len(skus) == 0 {
		return []Product{}, nil
	}
	// Invariant: Sort SKUs lexicographically ascending before querying
	sorted := make([]string, len(skus))
	copy(sorted, skus)
	sort.Strings(sorted)

	return s.repo.GetProductsBySKUs(ctx, sorted)
}

func (s *Service) ListProducts(ctx context.Context, params ListProductsParams) (*ProductListResponse, error) {
	items, nextCursor, hasMore, err := s.repo.ListProducts(ctx, params)
	if err != nil {
		return nil, err
	}
	return &ProductListResponse{
		Items:      items,
		NextCursor: nextCursor,
		HasMore:    hasMore,
	}, nil
}

func (s *Service) UpdateProduct(ctx context.Context, id uuid.UUID, req UpdateProductRequest, expectedVersion int64) (*Product, error) {
	if err := s.validateTitle(req.Title); err != nil {
		return nil, err
	}
	if expectedVersion <= 0 {
		return nil, errors.New("expected version must be positive")
	}

	p, err := s.repo.UpdateProduct(ctx, id, req.Title, req.Description, req.CategoryID, req.IsActive, expectedVersion)
	if err != nil {
		return nil, err
	}
	// Post-commit search indexing hook outside DB transaction
	if s.searchClient != nil {
		doc := s.buildProductDocument(ctx, p)
		if err := s.searchClient.IndexProduct(ctx, doc); err != nil {
			s.logger.Warn("failed to sync product to search index on update", "product_id", p.ID, "err", err)
		}
	}
	return p, nil
}

func (s *Service) UpdateProductPrice(ctx context.Context, id uuid.UUID, req UpdatePriceRequest, expectedVersion int64) (*Product, error) {
	if err := s.validatePrice(req.Price); err != nil {
		return nil, err
	}
	if expectedVersion <= 0 {
		return nil, errors.New("expected version must be positive")
	}

	p, err := s.repo.UpdateProductPrice(ctx, id, req.Price.Amount, req.Price.Currency, expectedVersion)
	if err != nil {
		return nil, err
	}
	// Post-commit search indexing hook outside DB transaction
	if s.searchClient != nil {
		doc := s.buildProductDocument(ctx, p)
		if err := s.searchClient.IndexProduct(ctx, doc); err != nil {
			s.logger.Warn("failed to sync product to search index on price update", "product_id", p.ID, "err", err)
		}
	}
	return p, nil
}

func (s *Service) CreateCategory(ctx context.Context, req CreateCategoryRequest) (*Category, error) {
	slug := strings.TrimSpace(req.Slug)
	if slug == "" {
		return nil, errors.New("slug cannot be empty")
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return nil, errors.New("name cannot be empty")
	}

	c := &Category{
		ID:          uuid.New(),
		Slug:        slug,
		Name:        name,
		Description: req.Description,
	}

	if err := s.repo.CreateCategory(ctx, c); err != nil {
		return nil, err
	}
	return c, nil
}

func (s *Service) GetCategory(ctx context.Context, id uuid.UUID) (*Category, error) {
	return s.repo.GetCategoryByID(ctx, id)
}

func (s *Service) ListCategories(ctx context.Context) ([]Category, error) {
	return s.repo.ListCategories(ctx)
}

func (s *Service) AddProductImage(ctx context.Context, productID uuid.UUID, storageKey, url, contentType string, sizeBytes int64, isPrimary bool, sortOrder int) (*ProductImage, error) {
	// Verify product exists
	if _, err := s.repo.GetProductByID(ctx, productID); err != nil {
		return nil, err
	}

	img := &ProductImage{
		ID:          uuid.New(),
		ProductID:   productID,
		StorageKey:  storageKey,
		URL:         url,
		ContentType: contentType,
		SizeBytes:   sizeBytes,
		IsPrimary:   isPrimary,
		SortOrder:   sortOrder,
	}

	if err := s.repo.AddProductImage(ctx, img); err != nil {
		return nil, err
	}
	return img, nil
}

func (s *Service) GetProductImages(ctx context.Context, productID uuid.UUID) ([]ProductImage, error) {
	if _, err := s.repo.GetProductByID(ctx, productID); err != nil {
		return nil, err
	}
	return s.repo.GetProductImages(ctx, productID)
}

func (s *Service) GetProductImageByID(ctx context.Context, productID, imageID uuid.UUID) (*ProductImage, error) {
	if _, err := s.repo.GetProductByID(ctx, productID); err != nil {
		return nil, err
	}
	return s.repo.GetProductImageByID(ctx, productID, imageID)
}

func (s *Service) DeleteProductImage(ctx context.Context, productID, imageID uuid.UUID) (*ProductImage, error) {
	if _, err := s.repo.GetProductByID(ctx, productID); err != nil {
		return nil, err
	}
	return s.repo.DeleteProductImage(ctx, productID, imageID)
}

func (s *Service) buildProductDocument(ctx context.Context, p *Product) search.ProductDocument {
	var catIDStr *string
	var catName string
	if p.CategoryID != nil {
		idStr := p.CategoryID.String()
		catIDStr = &idStr
		if cat, err := s.repo.GetCategoryByID(ctx, *p.CategoryID); err == nil && cat != nil {
			catName = cat.Name
		}
	}
	return search.ProductDocument{
		ID:           p.ID.String(),
		SKU:          p.SKU,
		Title:        p.Title,
		Description:  p.Description,
		CategoryID:   catIDStr,
		CategoryName: catName,
		PriceMinor:   p.PriceMinor,
		Currency:     p.Currency,
		InStock:      p.IsActive,
	}
}

// SearchProducts delegates the search query to the injected search client.
func (s *Service) SearchProducts(ctx context.Context, q search.Query) (*search.SearchResult, error) {
	if s.searchClient == nil {
		return nil, errors.New("search client not configured")
	}
	return s.searchClient.Search(ctx, q)
}

// ReindexAll scans all products from the repository and bulk indexes them into the search engine.
func (s *Service) ReindexAll(ctx context.Context) (int, error) {
	if s.searchClient == nil {
		return 0, errors.New("search client not configured")
	}

	totalIndexed := 0
	cursor := ""
	limit := 100

	for {
		if err := ctx.Err(); err != nil {
			return totalIndexed, err
		}

		products, nextCursor, hasMore, err := s.repo.ListProducts(ctx, ListProductsParams{
			Limit:  limit,
			Cursor: cursor,
		})
		if err != nil {
			return totalIndexed, fmt.Errorf("failed to list products for reindexing: %w", err)
		}

		if len(products) > 0 {
			docs := make([]search.ProductDocument, len(products))
			for i := range products {
				docs[i] = s.buildProductDocument(ctx, &products[i])
			}

			if err := s.searchClient.BulkIndex(ctx, docs); err != nil {
				return totalIndexed, fmt.Errorf("bulk indexing failed: %w", err)
			}
			totalIndexed += len(docs)
		}

		if !hasMore || nextCursor == "" {
			break
		}
		cursor = nextCursor
	}

	return totalIndexed, nil
}

