// internal/domain/catalog/models.go
package catalog

import (
	"context"
	"errors"
	"time"

	"shopflow/internal/domain/money"

	"github.com/google/uuid"
)

var (
	ErrProductNotFound        = errors.New("product not found")
	ErrCategoryNotFound       = errors.New("category not found")
	ErrDuplicateSKU           = errors.New("product SKU already exists")
	ErrDuplicateSlug          = errors.New("category slug already exists")
	ErrOptimisticLockConflict = errors.New("optimistic lock conflict: entity version mismatch")
	ErrInvalidSKU             = errors.New("SKU must be between 3 and 64 alphanumeric characters")
	ErrInvalidTitle           = errors.New("title cannot be empty or exceed 255 characters")
	ErrInvalidPrice           = errors.New("price must be greater than zero")
	ErrInactiveProduct        = errors.New("product is inactive")
	ErrProductImageNotFound   = errors.New("product image not found")
)

type Category struct {
	ID          uuid.UUID `json:"id"`
	Slug        string    `json:"slug"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type Product struct {
	ID          uuid.UUID   `json:"id"`
	CategoryID  *uuid.UUID  `json:"category_id,omitempty"`
	SKU         string      `json:"sku"`
	Title       string      `json:"title"`
	Description string      `json:"description"`
	PriceMinor  int64       `json:"-"` // column price_minor BIGINT
	Currency    string      `json:"-"` // column currency VARCHAR(3)
	Price       money.Money `json:"price"`
	IsActive    bool        `json:"is_active"`
	Version     int64       `json:"version"`
	Images      []ProductImage `json:"images,omitempty"`
	CreatedAt   time.Time   `json:"created_at"`
	UpdatedAt   time.Time   `json:"updated_at"`
}

type ProductImage struct {
	ID          uuid.UUID `json:"id"`
	ProductID   uuid.UUID `json:"product_id"`
	StorageKey  string    `json:"storage_key"`
	URL         string    `json:"url"`
	ContentType string    `json:"content_type"`
	SizeBytes   int64     `json:"size_bytes"`
	IsPrimary   bool      `json:"is_primary"`
	SortOrder   int       `json:"sort_order"`
	CreatedAt   time.Time `json:"created_at"`
}

// CatalogReader is the exported read-only interface consumed by Cart and Order domains.
// Enables in-process synchronous reads with zero cross-domain DB queries.
type CatalogReader interface {
	GetProduct(ctx context.Context, id uuid.UUID) (*Product, error)
	GetProductBySKU(ctx context.Context, sku string) (*Product, error)
	GetProductsBySKUs(ctx context.Context, skus []string) ([]Product, error)
}

// Request and Response DTOs

type CreateProductRequest struct {
	SKU         string      `json:"sku"`
	Title       string      `json:"title"`
	Description string      `json:"description"`
	CategoryID  *uuid.UUID  `json:"category_id,omitempty"`
	Price       money.Money `json:"price"`
}

type UpdateProductRequest struct {
	Title       string     `json:"title"`
	Description string     `json:"description"`
	CategoryID  *uuid.UUID `json:"category_id,omitempty"`
	IsActive    bool       `json:"is_active"`
}

type UpdatePriceRequest struct {
	Price money.Money `json:"price"`
}

type CreateCategoryRequest struct {
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type ListProductsParams struct {
	Limit      int
	Cursor     string // base64 encoded "created_at,id"
	CategoryID *uuid.UUID
	IsActive   *bool
}

type ProductListResponse struct {
	Items      []Product `json:"items"`
	NextCursor string    `json:"next_cursor,omitempty"`
	HasMore    bool      `json:"has_more"`
}
