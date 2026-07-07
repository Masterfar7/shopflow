package catalog_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"shopflow/internal/domain/catalog"
	"shopflow/internal/domain/money"
	"shopflow/internal/platform/search"
	"shopflow/internal/platform/storage"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupCatalogTestRouter(t *testing.T) (chi.Router, *catalog.Service) {
	repo := newMemoryCatalogRepo()
	svc := catalog.NewService(repo, nil)
	handler := catalog.NewHandler(svc, nil)

	r := chi.NewRouter()
	r.Mount("/api/v1/products", handler.ProductRoutes())
	r.Mount("/api/v1/categories", handler.CategoryRoutes())
	return r, svc
}

func setupCatalogTestRouterWithStorage(t *testing.T, store storage.BlobStorage) (chi.Router, *catalog.Service, *memoryCatalogRepo) {
	repo := newMemoryCatalogRepo()
	svc := catalog.NewService(repo, nil)
	handler := catalog.NewHandlerWithStorage(svc, store, nil)

	r := chi.NewRouter()
	r.Mount("/api/v1/products", handler.ProductRoutes())
	r.Mount("/api/v1/categories", handler.CategoryRoutes())
	return r, svc, repo
}

func TestCatalogHandler_CreateAndGetProduct(t *testing.T) {
	router, _ := setupCatalogTestRouter(t)

	body := []byte(`{
		"sku": "SKU-SHOES-RED",
		"title": "Red Running Shoes",
		"description": "Fast running shoes",
		"price": {
			"amount": 9900,
			"currency": "USD"
		}
	}`)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/products", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code)
	assert.Equal(t, `"1"`, rec.Header().Get("ETag"))
	assert.Contains(t, rec.Header().Get("Location"), "/api/v1/products/")

	var created catalog.Product
	err := json.Unmarshal(rec.Body.Bytes(), &created)
	require.NoError(t, err)
	assert.Equal(t, "SKU-SHOES-RED", created.SKU)
	assert.Equal(t, int64(9900), created.Price.Amount)
	assert.Equal(t, int64(1), created.Version)

	// GET Product
	getReq := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/products/%s", created.ID.String()), nil)
	getRec := httptest.NewRecorder()
	router.ServeHTTP(getRec, getReq)

	require.Equal(t, http.StatusOK, getRec.Code)
	assert.Equal(t, `"1"`, getRec.Header().Get("ETag"))

	var fetched catalog.Product
	err = json.Unmarshal(getRec.Body.Bytes(), &fetched)
	require.NoError(t, err)
	assert.Equal(t, created.ID, fetched.ID)
}

func TestCatalogHandler_UpdateProduct_ETagAndIfMatch(t *testing.T) {
	router, svc := setupCatalogTestRouter(t)

	p, err := svc.CreateProduct(t.Context(), catalog.CreateProductRequest{
		SKU:   "SKU-OCC-HTTP",
		Title: "Initial Title",
		Price: money.Money{Amount: 2000, Currency: "USD"},
	})
	require.NoError(t, err)

	url := fmt.Sprintf("/api/v1/products/%s", p.ID.String())

	// 1. Missing If-Match header -> 400 Bad Request
	updateBody := []byte(`{"title":"New Title","is_active":true}`)
	req := httptest.NewRequest(http.MethodPut, url, bytes.NewReader(updateBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	// 2. Matching If-Match header -> 200 OK with ETag "2"
	req = httptest.NewRequest(http.MethodPut, url, bytes.NewReader(updateBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("If-Match", `"1"`)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, `"2"`, rec.Header().Get("ETag"))

	var updated catalog.Product
	err = json.Unmarshal(rec.Body.Bytes(), &updated)
	require.NoError(t, err)
	assert.Equal(t, "New Title", updated.Title)
	assert.Equal(t, int64(2), updated.Version)

	// 3. Stale If-Match header -> 412 Precondition Failed (RFC 7807)
	req = httptest.NewRequest(http.MethodPut, url, bytes.NewReader(updateBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("If-Match", `"1"`) // stale! current is 2
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusPreconditionFailed, rec.Code)
	assert.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))
}

func TestCatalogHandler_UpdatePrice_OCC(t *testing.T) {
	router, svc := setupCatalogTestRouter(t)

	p, err := svc.CreateProduct(t.Context(), catalog.CreateProductRequest{
		SKU:   "SKU-PRICE-HTTP",
		Title: "Price Test",
		Price: money.Money{Amount: 1000, Currency: "USD"},
	})
	require.NoError(t, err)

	url := fmt.Sprintf("/api/v1/products/%s/price", p.ID.String())

	// Stale If-Match -> 412
	body := []byte(`{"price":{"amount":1200,"currency":"USD"}}`)
	req := httptest.NewRequest(http.MethodPatch, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("If-Match", `"999"`)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusPreconditionFailed, rec.Code)

	// Valid If-Match -> 200 OK with ETag "2"
	req = httptest.NewRequest(http.MethodPatch, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("If-Match", `"1"`)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, `"2"`, rec.Header().Get("ETag"))

	var priceUpdated catalog.Product
	err = json.Unmarshal(rec.Body.Bytes(), &priceUpdated)
	require.NoError(t, err)
	assert.Equal(t, int64(1200), priceUpdated.Price.Amount)
	assert.Equal(t, int64(2), priceUpdated.Version)
}

func TestCatalogHandler_Categories(t *testing.T) {
	router, _ := setupCatalogTestRouter(t)

	// Create Category
	catBody := []byte(`{
		"slug": "apparel",
		"name": "Apparel & Clothing",
		"description": "All kinds of clothes"
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/categories", bytes.NewReader(catBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code)
	var cat catalog.Category
	err := json.Unmarshal(rec.Body.Bytes(), &cat)
	require.NoError(t, err)
	assert.Equal(t, "apparel", cat.Slug)

	// Get Category by ID
	getReq := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/categories/%s", cat.ID.String()), nil)
	getRec := httptest.NewRecorder()
	router.ServeHTTP(getRec, getReq)
	require.Equal(t, http.StatusOK, getRec.Code)

	// Unknown Category ID -> 404
	badReq := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/categories/%s", uuid.New().String()), nil)
	badRec := httptest.NewRecorder()
	router.ServeHTTP(badRec, badReq)
	assert.Equal(t, http.StatusNotFound, badRec.Code)
}

func createMultipartRequest(url, fieldName, fileName string, fileContent []byte, formFields map[string]string) (*http.Request, error) {
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	part, err := writer.CreateFormFile(fieldName, fileName)
	if err != nil {
		return nil, err
	}
	if _, err := part.Write(fileContent); err != nil {
		return nil, err
	}

	for k, v := range formFields {
		if err := writer.WriteField(k, v); err != nil {
			return nil, err
		}
	}

	if err := writer.Close(); err != nil {
		return nil, err
	}

	req := httptest.NewRequest(http.MethodPost, url, body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req, nil
}

func createTestProduct(t *testing.T, svc *catalog.Service, sku string) *catalog.Product {
	price, err := money.New(4999, "USD")
	require.NoError(t, err)
	p, err := svc.CreateProduct(context.Background(), catalog.CreateProductRequest{
		SKU:         sku,
		Title:       "Test Product " + sku,
		Description: "A product for testing",
		Price:       price,
	})
	require.NoError(t, err)
	return p
}

func TestCatalogHandler_UploadProductImage_SuccessFormats(t *testing.T) {
	store := storage.NewMemoryStorage("http://localhost:8080/media")
	router, svc, _ := setupCatalogTestRouterWithStorage(t, store)
	product := createTestProduct(t, svc, "SKU-IMG-FORMATS")

	// 1. JPEG
	jpegBytes := append([]byte("\xff\xd8\xff\xe0\x00\x10JFIF\x00\x01\x01\x01"), bytes.Repeat([]byte{0x01}, 100)...)
	reqJPEG, err := createMultipartRequest(
		fmt.Sprintf("/api/v1/products/%s/images", product.ID.String()),
		"image", "photo.jpg", jpegBytes,
		map[string]string{"is_primary": "true", "sort_order": "1"},
	)
	require.NoError(t, err)
	recJPEG := httptest.NewRecorder()
	router.ServeHTTP(recJPEG, reqJPEG)
	require.Equal(t, http.StatusCreated, recJPEG.Code)

	var imgJPEG catalog.ProductImage
	err = json.Unmarshal(recJPEG.Body.Bytes(), &imgJPEG)
	require.NoError(t, err)
	assert.Equal(t, product.ID, imgJPEG.ProductID)
	assert.Equal(t, "image/jpeg", imgJPEG.ContentType)
	assert.True(t, imgJPEG.IsPrimary)
	assert.Equal(t, 1, imgJPEG.SortOrder)
	assert.Equal(t, int64(len(jpegBytes)), imgJPEG.SizeBytes)

	// Verify object exists in storage
	rc, storedObj, err := store.Get(context.Background(), imgJPEG.StorageKey)
	require.NoError(t, err)
	rc.Close()
	assert.Equal(t, int64(len(jpegBytes)), storedObj.SizeBytes)

	// 2. PNG
	pngBytes := append([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"), bytes.Repeat([]byte{0x02}, 100)...)
	reqPNG, err := createMultipartRequest(
		fmt.Sprintf("/api/v1/products/%s/images", product.ID.String()),
		"image", "photo.png", pngBytes,
		map[string]string{"is_primary": "false", "sort_order": "2"},
	)
	require.NoError(t, err)
	recPNG := httptest.NewRecorder()
	router.ServeHTTP(recPNG, reqPNG)
	require.Equal(t, http.StatusCreated, recPNG.Code)

	var imgPNG catalog.ProductImage
	err = json.Unmarshal(recPNG.Body.Bytes(), &imgPNG)
	require.NoError(t, err)
	assert.Equal(t, "image/png", imgPNG.ContentType)
	assert.False(t, imgPNG.IsPrimary)

	// 3. WebP
	webpBytes := append([]byte("RIFF\x24\x00\x00\x00WEBPVP8 "), bytes.Repeat([]byte{0x03}, 100)...)
	reqWebP, err := createMultipartRequest(
		fmt.Sprintf("/api/v1/products/%s/images", product.ID.String()),
		"image", "photo.webp", webpBytes,
		map[string]string{"is_primary": "false", "sort_order": "3"},
	)
	require.NoError(t, err)
	recWebP := httptest.NewRecorder()
	router.ServeHTTP(recWebP, reqWebP)
	require.Equal(t, http.StatusCreated, recWebP.Code)

	var imgWebP catalog.ProductImage
	err = json.Unmarshal(recWebP.Body.Bytes(), &imgWebP)
	require.NoError(t, err)
	assert.Equal(t, "image/webp", imgWebP.ContentType)
}

func TestCatalogHandler_UploadProductImage_Fast404(t *testing.T) {
	store := storage.NewMemoryStorage("http://localhost:8080/media")
	router, _, _ := setupCatalogTestRouterWithStorage(t, store)

	nonExistentID := uuid.New()
	jpegBytes := append([]byte("\xff\xd8\xff\xe0\x00\x10JFIF\x00\x01\x01\x01"), bytes.Repeat([]byte{0x01}, 100)...)
	req, err := createMultipartRequest(
		fmt.Sprintf("/api/v1/products/%s/images", nonExistentID.String()),
		"image", "photo.jpg", jpegBytes,
		nil,
	)
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestCatalogHandler_UploadProductImage_OversizedRejected(t *testing.T) {
	store := storage.NewMemoryStorage("http://localhost:8080/media")
	router, svc, _ := setupCatalogTestRouterWithStorage(t, store)
	product := createTestProduct(t, svc, "SKU-IMG-OVERSIZED")

	// Over 5MB
	oversizedBytes := append([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"), make([]byte, 5*1024*1024+100)...)
	req, err := createMultipartRequest(
		fmt.Sprintf("/api/v1/products/%s/images", product.ID.String()),
		"image", "large.png", oversizedBytes,
		nil,
	)
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestCatalogHandler_UploadProductImage_SpoofedMIMERejected(t *testing.T) {
	store := storage.NewMemoryStorage("http://localhost:8080/media")
	router, svc, _ := setupCatalogTestRouterWithStorage(t, store)
	product := createTestProduct(t, svc, "SKU-IMG-SPOOF")

	// Text content disguised as image/jpeg
	fakeBytes := []byte("<html><body>This is an HTML file, not a real image!</body></html>")
	req, err := createMultipartRequest(
		fmt.Sprintf("/api/v1/products/%s/images", product.ID.String()),
		"image", "fake.jpg", fakeBytes,
		nil,
	)
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestCatalogHandler_UploadProductImage_StorageNotConfigured(t *testing.T) {
	// Router with nil storage
	router, svc := setupCatalogTestRouter(t)
	product := createTestProduct(t, svc, "SKU-IMG-NOSTORE")

	jpegBytes := append([]byte("\xff\xd8\xff\xe0\x00\x10JFIF\x00\x01\x01\x01"), bytes.Repeat([]byte{0x01}, 50)...)
	req, err := createMultipartRequest(
		fmt.Sprintf("/api/v1/products/%s/images", product.ID.String()),
		"image", "photo.jpg", jpegBytes,
		nil,
	)
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusNotImplemented, rec.Code)
}

func TestCatalogHandler_ListProductImages(t *testing.T) {
	store := storage.NewMemoryStorage("http://localhost:8080/media")
	router, svc, _ := setupCatalogTestRouterWithStorage(t, store)
	product := createTestProduct(t, svc, "SKU-IMG-LIST")

	// Upload two images
	jpegBytes := append([]byte("\xff\xd8\xff\xe0\x00\x10JFIF\x00\x01\x01\x01"), bytes.Repeat([]byte{0x01}, 50)...)
	pngBytes := append([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"), bytes.Repeat([]byte{0x02}, 50)...)

	req1, _ := createMultipartRequest(
		fmt.Sprintf("/api/v1/products/%s/images", product.ID.String()),
		"image", "img1.jpg", jpegBytes,
		map[string]string{"sort_order": "10"},
	)
	rec1 := httptest.NewRecorder()
	router.ServeHTTP(rec1, req1)
	require.Equal(t, http.StatusCreated, rec1.Code)

	req2, _ := createMultipartRequest(
		fmt.Sprintf("/api/v1/products/%s/images", product.ID.String()),
		"image", "img2.png", pngBytes,
		map[string]string{"sort_order": "5"},
	)
	rec2 := httptest.NewRecorder()
	router.ServeHTTP(rec2, req2)
	require.Equal(t, http.StatusCreated, rec2.Code)

	// List images
	listReq := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/products/%s/images", product.ID.String()), nil)
	listRec := httptest.NewRecorder()
	router.ServeHTTP(listRec, listReq)

	require.Equal(t, http.StatusOK, listRec.Code)
	var list []catalog.ProductImage
	err := json.Unmarshal(listRec.Body.Bytes(), &list)
	require.NoError(t, err)
	require.Len(t, list, 2)
	// Sorted by sort_order ASC
	assert.Equal(t, 5, list[0].SortOrder)
	assert.Equal(t, 10, list[1].SortOrder)
}

func TestCatalogHandler_DeleteProductImage_PurgesStorageAndDB(t *testing.T) {
	store := storage.NewMemoryStorage("http://localhost:8080/media")
	router, svc, _ := setupCatalogTestRouterWithStorage(t, store)
	product := createTestProduct(t, svc, "SKU-IMG-DEL")

	// Upload image
	jpegBytes := append([]byte("\xff\xd8\xff\xe0\x00\x10JFIF\x00\x01\x01\x01"), bytes.Repeat([]byte{0x01}, 50)...)
	req, _ := createMultipartRequest(
		fmt.Sprintf("/api/v1/products/%s/images", product.ID.String()),
		"image", "del.jpg", jpegBytes,
		nil,
	)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusCreated, rec.Code)

	var created catalog.ProductImage
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))

	// Verify object is in storage
	_, _, err := store.Get(context.Background(), created.StorageKey)
	require.NoError(t, err)

	// Delete image
	delReq := httptest.NewRequest(
		http.MethodDelete,
		fmt.Sprintf("/api/v1/products/%s/images/%s", product.ID.String(), created.ID.String()),
		nil,
	)
	delRec := httptest.NewRecorder()
	router.ServeHTTP(delRec, delReq)
	require.Equal(t, http.StatusNoContent, delRec.Code)

	// Verify object is purged from storage
	_, _, err = store.Get(context.Background(), created.StorageKey)
	assert.ErrorIs(t, err, storage.ErrObjectNotFound)

	// Verify metadata is deleted from catalog
	imgs, err := svc.GetProductImages(context.Background(), product.ID)
	require.NoError(t, err)
	assert.Empty(t, imgs)

	// Deleting again returns 404
	delRec2 := httptest.NewRecorder()
	router.ServeHTTP(delRec2, delReq)
	assert.Equal(t, http.StatusNotFound, delRec2.Code)
}

type trackingStorage struct {
	*storage.MemoryStorage
	lastPutKey  string
	deletedKeys []string
}

func (t *trackingStorage) Put(ctx context.Context, key string, data io.Reader, sizeBytes int64, contentType string) (*storage.StoredObject, error) {
	t.lastPutKey = key
	return t.MemoryStorage.Put(ctx, key, data, sizeBytes, contentType)
}

func (t *trackingStorage) Delete(ctx context.Context, key string) error {
	t.deletedKeys = append(t.deletedKeys, key)
	return t.MemoryStorage.Delete(ctx, key)
}

type failingCatalogRepo struct {
	*memoryCatalogRepo
}

func (f *failingCatalogRepo) AddProductImage(ctx context.Context, img *catalog.ProductImage) error {
	return errors.New("simulated database insert failure")
}

func TestCatalogHandler_OrphanedUploadCompensation(t *testing.T) {
	memStore := storage.NewMemoryStorage("http://localhost:8080/media")
	tracker := &trackingStorage{MemoryStorage: memStore}

	baseRepo := newMemoryCatalogRepo()
	failRepo := &failingCatalogRepo{memoryCatalogRepo: baseRepo}
	svc := catalog.NewService(failRepo, nil)
	handler := catalog.NewHandlerWithStorage(svc, tracker, nil)

	r := chi.NewRouter()
	r.Mount("/api/v1/products", handler.ProductRoutes())

	product := createTestProduct(t, svc, "SKU-IMG-COMP")

	jpegBytes := append([]byte("\xff\xd8\xff\xe0\x00\x10JFIF\x00\x01\x01\x01"), bytes.Repeat([]byte{0x01}, 50)...)
	req, _ := createMultipartRequest(
		fmt.Sprintf("/api/v1/products/%s/images", product.ID.String()),
		"image", "comp.jpg", jpegBytes,
		nil,
	)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	// Handler should return 500 Metadata Error
	require.Equal(t, http.StatusInternalServerError, rec.Code)

	// Verify that storage Delete was called for compensation
	require.NotEmpty(t, tracker.lastPutKey)
	assert.Contains(t, tracker.deletedKeys, tracker.lastPutKey)

	// Verify the object is not left in storage
	_, _, err := tracker.Get(context.Background(), tracker.lastPutKey)
	assert.ErrorIs(t, err, storage.ErrObjectNotFound)
}

func setupCatalogSearchTestRouter(t *testing.T) (chi.Router, *catalog.Service, *search.MemoryClient) {
	repo := newMemoryCatalogRepo()
	searchClient := search.NewMemoryClient()
	svc := catalog.NewServiceWithSearch(repo, searchClient, nil)
	handler := catalog.NewHandlerWithStorageAndSearch(svc, nil, searchClient, nil)

	r := chi.NewRouter()
	r.Mount("/api/v1/catalog", handler.Routes())
	r.Mount("/api/v1/products", handler.ProductRoutes())
	return r, svc, searchClient
}

func TestCatalogHandler_Search_Success(t *testing.T) {
	router, svc, _ := setupCatalogSearchTestRouter(t)
	ctx := context.Background()

	cat, err := svc.CreateCategory(ctx, catalog.CreateCategoryRequest{
		Slug: "keyboards",
		Name: "Keyboards",
	})
	require.NoError(t, err)

	_, err = svc.CreateProduct(ctx, catalog.CreateProductRequest{
		SKU:         "SKU-KB-RGB",
		Title:       "Mechanical Keyboard RGB",
		Description: "Custom clicky switches",
		CategoryID:  &cat.ID,
		Price:       money.Money{Amount: 7999, Currency: "USD"},
	})
	require.NoError(t, err)

	_, err = svc.CreateProduct(ctx, catalog.CreateProductRequest{
		SKU:         "SKU-MOUSE",
		Title:       "Gaming Optical Mouse",
		Description: "Ergonomic gaming mouse with fast sensor",
		Price:       money.Money{Amount: 4999, Currency: "USD"},
	})
	require.NoError(t, err)

	// Test GET /api/v1/products/search?q=keyboard
	req := httptest.NewRequest(http.MethodGet, "/api/v1/products/search?q=keyboard", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var result search.SearchResult
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))
	assert.Equal(t, int64(1), result.TotalHits)
	require.Len(t, result.Products, 1)
	assert.Equal(t, "Mechanical Keyboard RGB", result.Products[0].Title)
	assert.Equal(t, int64(7999), result.Products[0].PriceMinor)
	require.Len(t, result.Facets.Categories, 1)
	assert.Equal(t, "Keyboards", result.Facets.Categories[0].Key)
	assert.Equal(t, int64(1), result.Facets.Categories[0].Count)

	// Test alias GET /api/v1/catalog/search?q=keyboard
	reqAlias := httptest.NewRequest(http.MethodGet, "/api/v1/catalog/search?q=keyboard", nil)
	recAlias := httptest.NewRecorder()
	router.ServeHTTP(recAlias, reqAlias)

	require.Equal(t, http.StatusOK, recAlias.Code)
	var resultAlias search.SearchResult
	require.NoError(t, json.Unmarshal(recAlias.Body.Bytes(), &resultAlias))
	assert.Equal(t, int64(1), resultAlias.TotalHits)
}

func TestCatalogHandler_Search_PriceFiltering(t *testing.T) {
	router, svc, _ := setupCatalogSearchTestRouter(t)
	ctx := context.Background()

	_, err := svc.CreateProduct(ctx, catalog.CreateProductRequest{
		SKU:   "SKU-10",
		Title: "Item 10",
		Price: money.Money{Amount: 1000, Currency: "USD"},
	})
	require.NoError(t, err)

	_, err = svc.CreateProduct(ctx, catalog.CreateProductRequest{
		SKU:   "SKU-50",
		Title: "Item 50",
		Price: money.Money{Amount: 5000, Currency: "USD"},
	})
	require.NoError(t, err)

	_, err = svc.CreateProduct(ctx, catalog.CreateProductRequest{
		SKU:   "SKU-100",
		Title: "Item 100",
		Price: money.Money{Amount: 10000, Currency: "USD"},
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/products/search?min_price=2000&max_price=8000", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var result search.SearchResult
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))
	assert.Equal(t, int64(1), result.TotalHits)
	require.Len(t, result.Products, 1)
	assert.Equal(t, "Item 50", result.Products[0].Title)
}

func TestCatalogHandler_Search_ValidationErrors(t *testing.T) {
	router, _, _ := setupCatalogSearchTestRouter(t)

	testCases := []struct {
		name         string
		url          string
		expectedCode string
	}{
		{
			name:         "invalid category UUID",
			url:          "/api/v1/products/search?category_id=not-a-uuid",
			expectedCode: "INVALID_CATEGORY_ID",
		},
		{
			name:         "invalid min_price non-numeric",
			url:          "/api/v1/products/search?min_price=abc",
			expectedCode: "INVALID_PRICE_FILTER",
		},
		{
			name:         "invalid min_price negative",
			url:          "/api/v1/products/search?min_price=-500",
			expectedCode: "INVALID_PRICE_FILTER",
		},
		{
			name:         "invalid max_price non-numeric",
			url:          "/api/v1/products/search?max_price=xyz",
			expectedCode: "INVALID_PRICE_FILTER",
		},
		{
			name:         "invalid max_price negative",
			url:          "/api/v1/products/search?max_price=-100",
			expectedCode: "INVALID_PRICE_FILTER",
		},
		{
			name:         "min_price greater than max_price",
			url:          "/api/v1/products/search?min_price=10000&max_price=5000",
			expectedCode: "INVALID_PRICE_RANGE",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.url, nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			assert.Equal(t, http.StatusBadRequest, rec.Code)
			var prob map[string]any
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &prob))
			assert.Equal(t, tc.expectedCode, prob["code"])
			assert.Equal(t, float64(http.StatusBadRequest), prob["status"])
		})
	}
}

