// internal/domain/catalog/handler.go
package catalog

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"shopflow/internal/platform/search"
	"shopflow/internal/platform/storage"
	"shopflow/internal/platform/web"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type Storage interface {
	Put(ctx context.Context, key string, data io.Reader, sizeBytes int64, contentType string) (*storage.StoredObject, error)
	Get(ctx context.Context, key string) (io.ReadCloser, *storage.StoredObject, error)
	Delete(ctx context.Context, key string) error
	GetURL(key string) string
}

type Handler struct {
	service      *Service
	storage      storage.BlobStorage
	searchClient search.Client
	logger       *slog.Logger
}

func NewHandler(service *Service, logger *slog.Logger) *Handler {
	return NewHandlerWithStorage(service, nil, logger)
}

func NewHandlerWithStorage(service *Service, store storage.BlobStorage, logger *slog.Logger) *Handler {
	return NewHandlerWithStorageAndSearch(service, store, nil, logger)
}

func NewHandlerWithStorageAndSearch(service *Service, store storage.BlobStorage, searchClient search.Client, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{
		service:      service,
		storage:      store,
		searchClient: searchClient,
		logger:       logger,
	}
}

// Routes mounts all catalog routes under /catalog (e.g. /catalog/products, /catalog/categories, /catalog/search).
func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Get("/search", h.searchProducts)
	r.Mount("/products", h.ProductRoutes())
	r.Mount("/categories", h.CategoryRoutes())
	return r
}

// ProductRoutes mounts REST endpoints for products.
func (h *Handler) ProductRoutes() chi.Router {
	r := chi.NewRouter()
	r.Post("/", h.createProduct)
	r.Get("/", h.listProducts)
	r.Get("/search", h.searchProducts)
	r.Get("/{id}", h.getProduct)
	r.Put("/{id}", h.updateProduct)
	r.Patch("/{id}/price", h.updateProductPrice)
	r.Post("/{id}/images", h.uploadProductImage)
	r.Get("/{id}/images", h.listProductImages)
	r.Delete("/{id}/images/{imageId}", h.deleteProductImage)
	return r
}

// CategoryRoutes mounts REST endpoints for categories.
func (h *Handler) CategoryRoutes() chi.Router {
	r := chi.NewRouter()
	r.Post("/", h.createCategory)
	r.Get("/", h.listCategories)
	r.Get("/{id}", h.getCategory)
	return r
}

func (h *Handler) createProduct(w http.ResponseWriter, r *http.Request) {
	var req CreateProductRequest
	if err := web.DecodeJSON(r, &req); err != nil {
		web.RespondProblem(w, r, http.StatusBadRequest, "INVALID_REQUEST_BODY", "Invalid Request Body", err.Error())
		return
	}

	product, err := h.service.CreateProduct(r.Context(), req)
	if err != nil {
		switch {
		case errors.Is(err, ErrDuplicateSKU):
			web.RespondProblem(w, r, http.StatusConflict, "DUPLICATE_SKU", "Duplicate SKU", err.Error())
		case errors.Is(err, ErrCategoryNotFound):
			web.RespondProblem(w, r, http.StatusNotFound, "CATEGORY_NOT_FOUND", "Category Not Found", err.Error())
		case errors.Is(err, ErrInvalidSKU), errors.Is(err, ErrInvalidTitle), errors.Is(err, ErrInvalidPrice):
			web.RespondProblem(w, r, http.StatusBadRequest, "VALIDATION_FAILED", "Validation Failed", err.Error())
		default:
			h.logger.Error("failed to create product", "err", err)
			web.RespondProblem(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "Internal Server Error", "An unexpected error occurred")
		}
		return
	}

	web.SetETag(w, product.Version)
	w.Header().Set("Location", fmt.Sprintf("/api/v1/products/%s", product.ID.String()))
	web.RespondJSON(w, http.StatusCreated, product)
}

func (h *Handler) listProducts(w http.ResponseWriter, r *http.Request) {
	var params ListProductsParams
	q := r.URL.Query()

	if limitStr := q.Get("limit"); limitStr != "" {
		if limit, err := strconv.Atoi(limitStr); err == nil {
			params.Limit = limit
		}
	}
	params.Cursor = q.Get("cursor")

	if catIDStr := q.Get("category_id"); catIDStr != "" {
		if catID, err := uuid.Parse(catIDStr); err == nil {
			params.CategoryID = &catID
		}
	}

	if activeStr := q.Get("is_active"); activeStr != "" {
		if active, err := strconv.ParseBool(activeStr); err == nil {
			params.IsActive = &active
		}
	}

	resp, err := h.service.ListProducts(r.Context(), params)
	if err != nil {
		h.logger.Error("failed to list products", "err", err)
		web.RespondProblem(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "Internal Server Error", "Failed to list products")
		return
	}

	web.RespondJSON(w, http.StatusOK, resp)
}

func (h *Handler) getProduct(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		web.RespondProblem(w, r, http.StatusBadRequest, "INVALID_UUID", "Invalid UUID", "The product ID must be a valid UUID")
		return
	}

	product, err := h.service.GetProduct(r.Context(), id)
	if err != nil {
		if errors.Is(err, ErrProductNotFound) {
			web.RespondProblem(w, r, http.StatusNotFound, "PRODUCT_NOT_FOUND", "Product Not Found", fmt.Sprintf("Product %s not found", idStr))
			return
		}
		h.logger.Error("failed to get product", "err", err, "id", idStr)
		web.RespondProblem(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "Internal Server Error", "Failed to retrieve product")
		return
	}

	web.SetETag(w, product.Version)
	web.RespondJSON(w, http.StatusOK, product)
}

func (h *Handler) updateProduct(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		web.RespondProblem(w, r, http.StatusBadRequest, "INVALID_UUID", "Invalid UUID", "The product ID must be a valid UUID")
		return
	}

	expectedVersion, err := web.ExtractIfMatch(r)
	if err != nil {
		web.RespondProblem(w, r, http.StatusBadRequest, "PRECONDITION_REQUIRED", "Precondition Required", "A valid If-Match header is required for updates")
		return
	}

	var req UpdateProductRequest
	if err := web.DecodeJSON(r, &req); err != nil {
		web.RespondProblem(w, r, http.StatusBadRequest, "INVALID_REQUEST_BODY", "Invalid Request Body", err.Error())
		return
	}

	product, err := h.service.UpdateProduct(r.Context(), id, req, expectedVersion)
	if err != nil {
		switch {
		case errors.Is(err, ErrOptimisticLockConflict):
			web.RespondProblem(w, r, http.StatusPreconditionFailed, "PRECONDITION_FAILED", "Precondition Failed", "The resource version does not match the If-Match header")
		case errors.Is(err, ErrProductNotFound):
			web.RespondProblem(w, r, http.StatusNotFound, "PRODUCT_NOT_FOUND", "Product Not Found", fmt.Sprintf("Product %s not found", idStr))
		case errors.Is(err, ErrCategoryNotFound):
			web.RespondProblem(w, r, http.StatusNotFound, "CATEGORY_NOT_FOUND", "Category Not Found", "Specified category does not exist")
		case errors.Is(err, ErrInvalidTitle):
			web.RespondProblem(w, r, http.StatusBadRequest, "VALIDATION_FAILED", "Validation Failed", err.Error())
		default:
			h.logger.Error("failed to update product", "err", err, "id", idStr)
			web.RespondProblem(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "Internal Server Error", "Failed to update product")
		}
		return
	}

	web.SetETag(w, product.Version)
	web.RespondJSON(w, http.StatusOK, product)
}

func (h *Handler) updateProductPrice(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		web.RespondProblem(w, r, http.StatusBadRequest, "INVALID_UUID", "Invalid UUID", "The product ID must be a valid UUID")
		return
	}

	expectedVersion, err := web.ExtractIfMatch(r)
	if err != nil {
		web.RespondProblem(w, r, http.StatusBadRequest, "PRECONDITION_REQUIRED", "Precondition Required", "A valid If-Match header is required for price updates")
		return
	}

	var req UpdatePriceRequest
	if err := web.DecodeJSON(r, &req); err != nil {
		web.RespondProblem(w, r, http.StatusBadRequest, "INVALID_REQUEST_BODY", "Invalid Request Body", err.Error())
		return
	}

	product, err := h.service.UpdateProductPrice(r.Context(), id, req, expectedVersion)
	if err != nil {
		switch {
		case errors.Is(err, ErrOptimisticLockConflict):
			web.RespondProblem(w, r, http.StatusPreconditionFailed, "PRECONDITION_FAILED", "Precondition Failed", "The resource version does not match the If-Match header")
		case errors.Is(err, ErrProductNotFound):
			web.RespondProblem(w, r, http.StatusNotFound, "PRODUCT_NOT_FOUND", "Product Not Found", fmt.Sprintf("Product %s not found", idStr))
		case errors.Is(err, ErrInvalidPrice):
			web.RespondProblem(w, r, http.StatusBadRequest, "VALIDATION_FAILED", "Validation Failed", err.Error())
		default:
			h.logger.Error("failed to update product price", "err", err, "id", idStr)
			web.RespondProblem(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "Internal Server Error", "Failed to update product price")
		}
		return
	}

	web.SetETag(w, product.Version)
	web.RespondJSON(w, http.StatusOK, product)
}

func (h *Handler) createCategory(w http.ResponseWriter, r *http.Request) {
	var req CreateCategoryRequest
	if err := web.DecodeJSON(r, &req); err != nil {
		web.RespondProblem(w, r, http.StatusBadRequest, "INVALID_REQUEST_BODY", "Invalid Request Body", err.Error())
		return
	}

	category, err := h.service.CreateCategory(r.Context(), req)
	if err != nil {
		if errors.Is(err, ErrDuplicateSlug) {
			web.RespondProblem(w, r, http.StatusConflict, "DUPLICATE_SLUG", "Duplicate Slug", err.Error())
			return
		}
		web.RespondProblem(w, r, http.StatusBadRequest, "VALIDATION_FAILED", "Validation Failed", err.Error())
		return
	}

	web.RespondJSON(w, http.StatusCreated, category)
}

func (h *Handler) listCategories(w http.ResponseWriter, r *http.Request) {
	categories, err := h.service.ListCategories(r.Context())
	if err != nil {
		h.logger.Error("failed to list categories", "err", err)
		web.RespondProblem(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "Internal Server Error", "Failed to list categories")
		return
	}

	web.RespondJSON(w, http.StatusOK, categories)
}

func (h *Handler) getCategory(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		web.RespondProblem(w, r, http.StatusBadRequest, "INVALID_UUID", "Invalid UUID", "The category ID must be a valid UUID")
		return
	}

	category, err := h.service.GetCategory(r.Context(), id)
	if err != nil {
		if errors.Is(err, ErrCategoryNotFound) {
			web.RespondProblem(w, r, http.StatusNotFound, "CATEGORY_NOT_FOUND", "Category Not Found", fmt.Sprintf("Category %s not found", idStr))
			return
		}
		h.logger.Error("failed to get category", "err", err, "id", idStr)
		web.RespondProblem(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "Internal Server Error", "Failed to retrieve category")
		return
	}

	web.RespondJSON(w, http.StatusOK, category)
}

func (h *Handler) uploadProductImage(w http.ResponseWriter, r *http.Request) {
	if h.storage == nil {
		web.RespondProblem(w, r, http.StatusNotImplemented, "STORAGE_NOT_CONFIGURED", "Storage Not Configured", "Media storage is not configured on this instance")
		return
	}

	productIDStr := chi.URLParam(r, "id")
	productID, err := uuid.Parse(productIDStr)
	if err != nil {
		web.RespondProblem(w, r, http.StatusBadRequest, "INVALID_UUID", "Invalid UUID", "Product ID must be a valid UUID")
		return
	}

	// Fast 404: verify product exists before uploading blob to storage
	if _, err := h.service.GetProduct(r.Context(), productID); err != nil {
		if errors.Is(err, ErrProductNotFound) {
			web.RespondProblem(w, r, http.StatusNotFound, "PRODUCT_NOT_FOUND", "Product Not Found", fmt.Sprintf("Product %s not found", productIDStr))
			return
		}
		h.logger.Error("failed to verify product existence", "err", err, "product_id", productIDStr)
		web.RespondProblem(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "Internal Server Error", "Failed to verify product")
		return
	}

	// Limit upload size to 5MB + 512KB overhead
	r.Body = http.MaxBytesReader(w, r.Body, storage.MaxFileSizeBytes+512*1024)
	if err := r.ParseMultipartForm(storage.MaxFileSizeBytes); err != nil {
		web.RespondProblem(w, r, http.StatusBadRequest, "FILE_TOO_LARGE", "File Too Large", "Uploaded file exceeds maximum limit of 5MB")
		return
	}

	file, header, err := r.FormFile("image")
	if err != nil {
		web.RespondProblem(w, r, http.StatusBadRequest, "MISSING_FILE", "Missing File", "The 'image' multipart field is required")
		return
	}
	defer file.Close()

	if header.Size > storage.MaxFileSizeBytes || header.Size <= 0 {
		web.RespondProblem(w, r, http.StatusBadRequest, "FILE_TOO_LARGE", "File Too Large", "Uploaded file exceeds maximum limit of 5MB")
		return
	}

	// MIME sniffing & Magic bytes inspection
	sniffBuf := make([]byte, 512)
	n, err := io.ReadFull(file, sniffBuf)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		web.RespondProblem(w, r, http.StatusBadRequest, "READ_ERROR", "Read Error", "Failed to read image content")
		return
	}
	if n == 0 {
		web.RespondProblem(w, r, http.StatusBadRequest, "EMPTY_FILE", "Empty File", "Uploaded file is empty")
		return
	}

	var fileReader io.Reader = file
	if seeker, ok := file.(io.Seeker); ok {
		if _, err := seeker.Seek(0, io.SeekStart); err != nil {
			fileReader = io.MultiReader(bytes.NewReader(sniffBuf[:n]), file)
		}
	} else {
		fileReader = io.MultiReader(bytes.NewReader(sniffBuf[:n]), file)
	}

	sniffedMIME := storage.DetectImageContentType(sniffBuf[:n])
	ext, err := storage.ValidateImageUpload(header.Size, sniffedMIME)
	if err != nil {
		web.RespondProblem(w, r, http.StatusBadRequest, "INVALID_IMAGE", "Invalid Image", err.Error())
		return
	}

	imageID := uuid.New()
	storageKey := storage.BuildProductImageKey(productID, imageID, ext)

	// Put blob to storage outside database transaction
	storedObj, err := h.storage.Put(r.Context(), storageKey, fileReader, header.Size, sniffedMIME)
	if err != nil {
		h.logger.Error("failed to store image object", "err", err, "key", storageKey)
		web.RespondProblem(w, r, http.StatusInternalServerError, "STORAGE_ERROR", "Storage Error", "Failed to store image file")
		return
	}

	isPrimary := r.FormValue("is_primary") == "true"
	sortOrder, _ := strconv.Atoi(r.FormValue("sort_order"))

	img, err := h.service.AddProductImage(r.Context(), productID, storedObj.Key, storedObj.URL, sniffedMIME, header.Size, isPrimary, sortOrder)
	if err != nil {
		// Orphaned upload compensation: immediately delete the blob using detached timeout context
		compCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = h.storage.Delete(compCtx, storageKey)

		if errors.Is(err, ErrProductNotFound) {
			web.RespondProblem(w, r, http.StatusNotFound, "PRODUCT_NOT_FOUND", "Product Not Found", fmt.Sprintf("Product %s not found", productIDStr))
			return
		}
		h.logger.Error("failed to record product image metadata", "err", err, "product_id", productIDStr)
		web.RespondProblem(w, r, http.StatusInternalServerError, "METADATA_ERROR", "Metadata Error", "Failed to record image metadata")
		return
	}

	web.RespondJSON(w, http.StatusCreated, img)
}

func (h *Handler) listProductImages(w http.ResponseWriter, r *http.Request) {
	productIDStr := chi.URLParam(r, "id")
	productID, err := uuid.Parse(productIDStr)
	if err != nil {
		web.RespondProblem(w, r, http.StatusBadRequest, "INVALID_UUID", "Invalid UUID", "Product ID must be a valid UUID")
		return
	}

	images, err := h.service.GetProductImages(r.Context(), productID)
	if err != nil {
		if errors.Is(err, ErrProductNotFound) {
			web.RespondProblem(w, r, http.StatusNotFound, "PRODUCT_NOT_FOUND", "Product Not Found", fmt.Sprintf("Product %s not found", productIDStr))
			return
		}
		h.logger.Error("failed to list product images", "err", err, "product_id", productIDStr)
		web.RespondProblem(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "Internal Server Error", "Failed to retrieve images")
		return
	}

	if images == nil {
		images = []ProductImage{}
	}
	web.RespondJSON(w, http.StatusOK, images)
}

func (h *Handler) deleteProductImage(w http.ResponseWriter, r *http.Request) {
	productIDStr := chi.URLParam(r, "id")
	productID, err := uuid.Parse(productIDStr)
	if err != nil {
		web.RespondProblem(w, r, http.StatusBadRequest, "INVALID_UUID", "Invalid UUID", "Product ID must be a valid UUID")
		return
	}

	imageIDStr := chi.URLParam(r, "imageId")
	imageID, err := uuid.Parse(imageIDStr)
	if err != nil {
		web.RespondProblem(w, r, http.StatusBadRequest, "INVALID_UUID", "Invalid UUID", "Image ID must be a valid UUID")
		return
	}

	deletedImg, err := h.service.DeleteProductImage(r.Context(), productID, imageID)
	if err != nil {
		if errors.Is(err, ErrProductNotFound) {
			web.RespondProblem(w, r, http.StatusNotFound, "PRODUCT_NOT_FOUND", "Product Not Found", fmt.Sprintf("Product %s not found", productIDStr))
			return
		}
		if errors.Is(err, ErrProductImageNotFound) {
			web.RespondProblem(w, r, http.StatusNotFound, "IMAGE_NOT_FOUND", "Image Not Found", fmt.Sprintf("Image %s not found", imageIDStr))
			return
		}
		h.logger.Error("failed to delete product image", "err", err, "image_id", imageIDStr)
		web.RespondProblem(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "Internal Server Error", "Failed to delete image")
		return
	}

	// Purge storage on deletion: delete binary blob from storage outside DB transaction
	if h.storage != nil && deletedImg != nil && deletedImg.StorageKey != "" {
		if err := h.storage.Delete(r.Context(), deletedImg.StorageKey); err != nil {
			h.logger.Warn("failed to delete blob from storage", "key", deletedImg.StorageKey, "err", err)
		}
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) searchProducts(w http.ResponseWriter, r *http.Request) {
	qParams := r.URL.Query()
	sq := search.Query{
		Text:     strings.TrimSpace(qParams.Get("q")),
		Page:     1,
		PageSize: 20,
	}

	if catIDStr := qParams.Get("category_id"); catIDStr != "" {
		if _, err := uuid.Parse(catIDStr); err != nil {
			web.RespondProblem(w, r, http.StatusBadRequest, "INVALID_CATEGORY_ID", "Invalid Category ID", "category_id must be a valid UUID")
			return
		}
		sq.CategoryID = &catIDStr
	}

	if minStr := qParams.Get("min_price"); minStr != "" {
		minPrice, err := strconv.ParseInt(minStr, 10, 64)
		if err != nil || minPrice < 0 {
			web.RespondProblem(w, r, http.StatusBadRequest, "INVALID_PRICE_FILTER", "Invalid Price Filter", "min_price must be a non-negative integer in minor units")
			return
		}
		sq.MinPriceMinor = &minPrice
	}

	if maxStr := qParams.Get("max_price"); maxStr != "" {
		maxPrice, err := strconv.ParseInt(maxStr, 10, 64)
		if err != nil || maxPrice < 0 {
			web.RespondProblem(w, r, http.StatusBadRequest, "INVALID_PRICE_FILTER", "Invalid Price Filter", "max_price must be a non-negative integer in minor units")
			return
		}
		sq.MaxPriceMinor = &maxPrice
	}

	if sq.MinPriceMinor != nil && sq.MaxPriceMinor != nil && *sq.MinPriceMinor > *sq.MaxPriceMinor {
		web.RespondProblem(w, r, http.StatusBadRequest, "INVALID_PRICE_RANGE", "Invalid Price Range", "min_price cannot exceed max_price")
		return
	}

	if stockStr := qParams.Get("in_stock"); stockStr != "" {
		if inStock, err := strconv.ParseBool(stockStr); err == nil {
			sq.InStockOnly = &inStock
		}
	}

	if pageStr := qParams.Get("page"); pageStr != "" {
		if p, err := strconv.Atoi(pageStr); err == nil && p > 0 {
			sq.Page = p
		}
	}

	if sizeStr := qParams.Get("page_size"); sizeStr != "" {
		if s, err := strconv.Atoi(sizeStr); err == nil && s > 0 {
			sq.PageSize = s
		}
	}

	result, err := h.service.SearchProducts(r.Context(), sq)
	if err != nil {
		h.logger.Error("product search failed", "err", err)
		web.RespondProblem(w, r, http.StatusInternalServerError, "SEARCH_ERROR", "Search Error", "Failed to execute search query")
		return
	}

	web.RespondJSON(w, http.StatusOK, result)
}

