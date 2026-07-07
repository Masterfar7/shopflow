// internal/domain/catalog/repository.go
package catalog

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"shopflow/internal/domain/money"
	"shopflow/internal/platform/database"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Repository interface {
	CreateProduct(ctx context.Context, p *Product) error
	GetProductByID(ctx context.Context, id uuid.UUID) (*Product, error)
	GetProductBySKU(ctx context.Context, sku string) (*Product, error)
	GetProductsBySKUs(ctx context.Context, skus []string) ([]Product, error)
	ListProducts(ctx context.Context, params ListProductsParams) ([]Product, string, bool, error)
	UpdateProduct(ctx context.Context, id uuid.UUID, title, description string, categoryID *uuid.UUID, isActive bool, expectedVersion int64) (*Product, error)
	UpdateProductPrice(ctx context.Context, id uuid.UUID, priceMinor int64, currency string, expectedVersion int64) (*Product, error)

	CreateCategory(ctx context.Context, c *Category) error
	GetCategoryByID(ctx context.Context, id uuid.UUID) (*Category, error)
	ListCategories(ctx context.Context) ([]Category, error)

	AddProductImage(ctx context.Context, img *ProductImage) error
	GetProductImages(ctx context.Context, productID uuid.UUID) ([]ProductImage, error)
	GetProductImageByID(ctx context.Context, productID, imageID uuid.UUID) (*ProductImage, error)
	DeleteProductImage(ctx context.Context, productID, imageID uuid.UUID) (*ProductImage, error)
}

type PostgresRepository struct {
	pool database.DBTX
}

func NewPostgresRepository(pool *pgxpool.Pool) *PostgresRepository {
	return &PostgresRepository{pool: pool}
}

// NewPostgresRepositoryWithDBTX allows injecting a DBTX interface for testing or transaction contexts.
func NewPostgresRepositoryWithDBTX(dbtx database.DBTX) *PostgresRepository {
	return &PostgresRepository{pool: dbtx}
}

func (r *PostgresRepository) CreateProduct(ctx context.Context, p *Product) error {
	if p.ID == uuid.Nil {
		p.ID = uuid.New()
	}
	p.PriceMinor = p.Price.Amount
	p.Currency = p.Price.Currency
	p.Version = 1

	query := `
		INSERT INTO products (id, category_id, sku, title, description, price_minor, currency, is_active, version, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 1, NOW(), NOW())
		RETURNING created_at, updated_at;
	`
	err := r.pool.QueryRow(ctx, query,
		p.ID, p.CategoryID, p.SKU, p.Title, p.Description,
		p.PriceMinor, p.Currency, p.IsActive,
	).Scan(&p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			if pgErr.Code == "23505" {
				return ErrDuplicateSKU
			}
			if pgErr.Code == "23503" {
				return ErrCategoryNotFound
			}
		}
		return fmt.Errorf("create product: %w", err)
	}
	return nil
}

func (r *PostgresRepository) GetProductByID(ctx context.Context, id uuid.UUID) (*Product, error) {
	query := `
		SELECT id, category_id, sku, title, description, price_minor, currency, is_active, version, created_at, updated_at
		FROM products
		WHERE id = $1;
	`
	p := &Product{}
	err := r.pool.QueryRow(ctx, query, id).Scan(
		&p.ID, &p.CategoryID, &p.SKU, &p.Title, &p.Description,
		&p.PriceMinor, &p.Currency, &p.IsActive, &p.Version, &p.CreatedAt, &p.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrProductNotFound
		}
		return nil, fmt.Errorf("get product by id: %w", err)
	}
	p.Price = money.Money{Amount: p.PriceMinor, Currency: p.Currency}
	return p, nil
}

func (r *PostgresRepository) GetProductBySKU(ctx context.Context, sku string) (*Product, error) {
	query := `
		SELECT id, category_id, sku, title, description, price_minor, currency, is_active, version, created_at, updated_at
		FROM products
		WHERE sku = $1;
	`
	p := &Product{}
	err := r.pool.QueryRow(ctx, query, sku).Scan(
		&p.ID, &p.CategoryID, &p.SKU, &p.Title, &p.Description,
		&p.PriceMinor, &p.Currency, &p.IsActive, &p.Version, &p.CreatedAt, &p.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrProductNotFound
		}
		return nil, fmt.Errorf("get product by sku: %w", err)
	}
	p.Price = money.Money{Amount: p.PriceMinor, Currency: p.Currency}
	return p, nil
}

func (r *PostgresRepository) GetProductsBySKUs(ctx context.Context, skus []string) ([]Product, error) {
	if len(skus) == 0 {
		return []Product{}, nil
	}
	query := `
		SELECT id, category_id, sku, title, description, price_minor, currency, is_active, version, created_at, updated_at
		FROM products
		WHERE sku = ANY($1)
		ORDER BY sku ASC;
	`
	rows, err := r.pool.Query(ctx, query, skus)
	if err != nil {
		return nil, fmt.Errorf("get products by skus: %w", err)
	}
	defer rows.Close()

	var products []Product
	for rows.Next() {
		var p Product
		if err := rows.Scan(
			&p.ID, &p.CategoryID, &p.SKU, &p.Title, &p.Description,
			&p.PriceMinor, &p.Currency, &p.IsActive, &p.Version, &p.CreatedAt, &p.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan product: %w", err)
		}
		p.Price = money.Money{Amount: p.PriceMinor, Currency: p.Currency}
		products = append(products, p)
	}
	return products, nil
}

func (r *PostgresRepository) ListProducts(ctx context.Context, params ListProductsParams) ([]Product, string, bool, error) {
	limit := params.Limit
	if limit <= 0 {
		limit = 20
	} else if limit > 100 {
		limit = 100
	}

	fetchLimit := limit + 1

	var cursorTime *time.Time
	var cursorID *uuid.UUID

	if params.Cursor != "" {
		raw, err := base64.StdEncoding.DecodeString(params.Cursor)
		if err == nil {
			parts := strings.Split(string(raw), ",")
			if len(parts) == 2 {
				t, errT := time.Parse(time.RFC3339Nano, parts[0])
				u, errU := uuid.Parse(parts[1])
				if errT == nil && errU == nil {
					cursorTime = &t
					cursorID = &u
				}
			}
		}
	}

	query := `
		SELECT id, category_id, sku, title, description, price_minor, currency, is_active, version, created_at, updated_at
		FROM products
		WHERE ($1::uuid IS NULL OR category_id = $1)
		  AND ($2::boolean IS NULL OR is_active = $2)
		  AND ($3::timestamptz IS NULL OR created_at < $3 OR (created_at = $3 AND id < $4))
		ORDER BY created_at DESC, id DESC
		LIMIT $5;
	`
	rows, err := r.pool.Query(ctx, query, params.CategoryID, params.IsActive, cursorTime, cursorID, fetchLimit)
	if err != nil {
		return nil, "", false, fmt.Errorf("list products: %w", err)
	}
	defer rows.Close()

	var items []Product
	for rows.Next() {
		var p Product
		if err := rows.Scan(
			&p.ID, &p.CategoryID, &p.SKU, &p.Title, &p.Description,
			&p.PriceMinor, &p.Currency, &p.IsActive, &p.Version, &p.CreatedAt, &p.UpdatedAt,
		); err != nil {
			return nil, "", false, fmt.Errorf("scan product: %w", err)
		}
		p.Price = money.Money{Amount: p.PriceMinor, Currency: p.Currency}
		items = append(items, p)
	}

	hasMore := len(items) > limit
	var nextCursor string
	if hasMore {
		items = items[:limit]
		last := items[len(items)-1]
		payload := fmt.Sprintf("%s,%s", last.CreatedAt.Format(time.RFC3339Nano), last.ID.String())
		nextCursor = base64.StdEncoding.EncodeToString([]byte(payload))
	}

	return items, nextCursor, hasMore, nil
}

func (r *PostgresRepository) UpdateProduct(ctx context.Context, id uuid.UUID, title, description string, categoryID *uuid.UUID, isActive bool, expectedVersion int64) (*Product, error) {
	query := `
		UPDATE products
		SET title = $1, description = $2, category_id = $3, is_active = $4,
		    version = version + 1, updated_at = NOW()
		WHERE id = $5 AND version = $6
		RETURNING id, category_id, sku, title, description, price_minor, currency, is_active, version, created_at, updated_at;
	`
	p := &Product{}
	err := r.pool.QueryRow(ctx, query, title, description, categoryID, isActive, id, expectedVersion).Scan(
		&p.ID, &p.CategoryID, &p.SKU, &p.Title, &p.Description,
		&p.PriceMinor, &p.Currency, &p.IsActive, &p.Version, &p.CreatedAt, &p.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			var exists bool
			_ = r.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM products WHERE id = $1)`, id).Scan(&exists)
			if exists {
				return nil, ErrOptimisticLockConflict
			}
			return nil, ErrProductNotFound
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" {
			return nil, ErrCategoryNotFound
		}
		return nil, fmt.Errorf("update product: %w", err)
	}
	p.Price = money.Money{Amount: p.PriceMinor, Currency: p.Currency}
	return p, nil
}

func (r *PostgresRepository) UpdateProductPrice(ctx context.Context, id uuid.UUID, priceMinor int64, currency string, expectedVersion int64) (*Product, error) {
	query := `
		UPDATE products
		SET price_minor = $1, currency = $2,
		    version = version + 1, updated_at = NOW()
		WHERE id = $3 AND version = $4
		RETURNING id, category_id, sku, title, description, price_minor, currency, is_active, version, created_at, updated_at;
	`
	p := &Product{}
	err := r.pool.QueryRow(ctx, query, priceMinor, currency, id, expectedVersion).Scan(
		&p.ID, &p.CategoryID, &p.SKU, &p.Title, &p.Description,
		&p.PriceMinor, &p.Currency, &p.IsActive, &p.Version, &p.CreatedAt, &p.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			var exists bool
			_ = r.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM products WHERE id = $1)`, id).Scan(&exists)
			if exists {
				return nil, ErrOptimisticLockConflict
			}
			return nil, ErrProductNotFound
		}
		return nil, fmt.Errorf("update product price: %w", err)
	}
	p.Price = money.Money{Amount: p.PriceMinor, Currency: p.Currency}
	return p, nil
}

func (r *PostgresRepository) CreateCategory(ctx context.Context, c *Category) error {
	if c.ID == uuid.Nil {
		c.ID = uuid.New()
	}
	query := `
		INSERT INTO categories (id, slug, name, description, created_at, updated_at)
		VALUES ($1, $2, $3, $4, NOW(), NOW())
		RETURNING created_at, updated_at;
	`
	err := r.pool.QueryRow(ctx, query, c.ID, c.Slug, c.Name, c.Description).Scan(&c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return ErrDuplicateSlug
		}
		return fmt.Errorf("create category: %w", err)
	}
	return nil
}

func (r *PostgresRepository) GetCategoryByID(ctx context.Context, id uuid.UUID) (*Category, error) {
	query := `
		SELECT id, slug, name, description, created_at, updated_at
		FROM categories
		WHERE id = $1;
	`
	c := &Category{}
	err := r.pool.QueryRow(ctx, query, id).Scan(&c.ID, &c.Slug, &c.Name, &c.Description, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrCategoryNotFound
		}
		return nil, fmt.Errorf("get category by id: %w", err)
	}
	return c, nil
}

func (r *PostgresRepository) ListCategories(ctx context.Context) ([]Category, error) {
	query := `
		SELECT id, slug, name, description, created_at, updated_at
		FROM categories
		ORDER BY name ASC;
	`
	rows, err := r.pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("list categories: %w", err)
	}
	defer rows.Close()

	var categories []Category
	for rows.Next() {
		var c Category
		if err := rows.Scan(&c.ID, &c.Slug, &c.Name, &c.Description, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan category: %w", err)
		}
		categories = append(categories, c)
	}
	return categories, nil
}

func (r *PostgresRepository) AddProductImage(ctx context.Context, img *ProductImage) error {
	if img.ID == uuid.Nil {
		img.ID = uuid.New()
	}
	if img.IsPrimary {
		_, _ = r.pool.Exec(ctx, "UPDATE product_images SET is_primary = FALSE WHERE product_id = $1;", img.ProductID)
	}
	query := `
		INSERT INTO product_images (id, product_id, storage_key, url, content_type, size_bytes, is_primary, sort_order, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NOW())
		RETURNING created_at;
	`
	err := r.pool.QueryRow(ctx, query,
		img.ID, img.ProductID, img.StorageKey, img.URL, img.ContentType, img.SizeBytes, img.IsPrimary, img.SortOrder,
	).Scan(&img.CreatedAt)
	if err != nil {
		return fmt.Errorf("add product image: %w", err)
	}
	return nil
}

func (r *PostgresRepository) GetProductImages(ctx context.Context, productID uuid.UUID) ([]ProductImage, error) {
	query := `
		SELECT id, product_id, storage_key, url, content_type, size_bytes, is_primary, sort_order, created_at
		FROM product_images
		WHERE product_id = $1
		ORDER BY sort_order ASC, created_at ASC;
	`
	rows, err := r.pool.Query(ctx, query, productID)
	if err != nil {
		return nil, fmt.Errorf("get product images: %w", err)
	}
	defer rows.Close()

	var images []ProductImage
	for rows.Next() {
		var img ProductImage
		if err := rows.Scan(
			&img.ID, &img.ProductID, &img.StorageKey, &img.URL, &img.ContentType,
			&img.SizeBytes, &img.IsPrimary, &img.SortOrder, &img.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan product image: %w", err)
		}
		images = append(images, img)
	}
	return images, nil
}

func (r *PostgresRepository) GetProductImageByID(ctx context.Context, productID, imageID uuid.UUID) (*ProductImage, error) {
	query := `
		SELECT id, product_id, storage_key, url, content_type, size_bytes, is_primary, sort_order, created_at
		FROM product_images
		WHERE id = $1 AND product_id = $2;
	`
	var img ProductImage
	err := r.pool.QueryRow(ctx, query, imageID, productID).Scan(
		&img.ID, &img.ProductID, &img.StorageKey, &img.URL, &img.ContentType,
		&img.SizeBytes, &img.IsPrimary, &img.SortOrder, &img.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrProductImageNotFound
		}
		return nil, fmt.Errorf("get product image by id: %w", err)
	}
	return &img, nil
}

func (r *PostgresRepository) DeleteProductImage(ctx context.Context, productID, imageID uuid.UUID) (*ProductImage, error) {
	query := `
		DELETE FROM product_images
		WHERE id = $1 AND product_id = $2
		RETURNING id, product_id, storage_key, url, content_type, size_bytes, is_primary, sort_order, created_at;
	`
	var img ProductImage
	err := r.pool.QueryRow(ctx, query, imageID, productID).Scan(
		&img.ID, &img.ProductID, &img.StorageKey, &img.URL, &img.ContentType,
		&img.SizeBytes, &img.IsPrimary, &img.SortOrder, &img.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrProductImageNotFound
		}
		return nil, fmt.Errorf("delete product image: %w", err)
	}
	return &img, nil
}
