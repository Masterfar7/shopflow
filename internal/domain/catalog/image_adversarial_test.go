package catalog_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"

	"shopflow/internal/domain/catalog"
	"shopflow/internal/platform/storage"
	"shopflow/internal/platform/web"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Thread-safe storage for concurrent adversarial verification
type threadSafeTrackingStorage struct {
	*storage.MemoryStorage
	mu          sync.Mutex
	putKeys     []string
	deletedKeys []string
}

func newThreadSafeTrackingStorage(baseURL string) *threadSafeTrackingStorage {
	return &threadSafeTrackingStorage{
		MemoryStorage: storage.NewMemoryStorage(baseURL),
	}
}

func (s *threadSafeTrackingStorage) Put(ctx context.Context, key string, data io.Reader, sizeBytes int64, contentType string) (*storage.StoredObject, error) {
	s.mu.Lock()
	s.putKeys = append(s.putKeys, key)
	s.mu.Unlock()
	return s.MemoryStorage.Put(ctx, key, data, sizeBytes, contentType)
}

func (s *threadSafeTrackingStorage) Delete(ctx context.Context, key string) error {
	s.mu.Lock()
	s.deletedKeys = append(s.deletedKeys, key)
	s.mu.Unlock()
	return s.MemoryStorage.Delete(ctx, key)
}

func (s *threadSafeTrackingStorage) GetPutKeys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.putKeys))
	copy(out, s.putKeys)
	return out
}

func (s *threadSafeTrackingStorage) GetDeletedKeys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.deletedKeys))
	copy(out, s.deletedKeys)
	return out
}

// 1. Boundary Test: Exactly 5MB (5,242,880 bytes) upload succeeds with HTTP 201 Created
func TestAdversarialCatalog_ImageUpload_Exact5MB(t *testing.T) {
	store := storage.NewMemoryStorage("http://localhost:8080/media")
	router, svc, _ := setupCatalogTestRouterWithStorage(t, store)
	product := createTestProduct(t, svc, "SKU-ADV-5MB-EXACT")

	jpegHeader := []byte("\xff\xd8\xff\xe0\x00\x10JFIF\x00\x01\x01\x01")
	exact5MBData := append(jpegHeader, make([]byte, storage.MaxFileSizeBytes-len(jpegHeader))...)
	require.Equal(t, storage.MaxFileSizeBytes, len(exact5MBData))

	req, err := createMultipartRequest(
		fmt.Sprintf("/api/v1/products/%s/images", product.ID.String()),
		"image", "exact5mb.jpg", exact5MBData,
		map[string]string{"is_primary": "true", "sort_order": "1"},
	)
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code, "Exactly 5MB image must be accepted with HTTP 201")

	var created catalog.ProductImage
	err = json.Unmarshal(rec.Body.Bytes(), &created)
	require.NoError(t, err)
	assert.Equal(t, int64(storage.MaxFileSizeBytes), created.SizeBytes)
	assert.Equal(t, "image/jpeg", created.ContentType)
	assert.True(t, created.IsPrimary)

	// Verify object exists in storage
	rc, storedObj, err := store.Get(context.Background(), created.StorageKey)
	require.NoError(t, err)
	rc.Close()
	assert.Equal(t, int64(storage.MaxFileSizeBytes), storedObj.SizeBytes)
}

// 2. Boundary Test: Exactly 5MB + 1 byte (5,242,881 bytes) is rejected with HTTP 400 Bad Request
func TestAdversarialCatalog_ImageUpload_5MBPlus1Byte(t *testing.T) {
	store := newThreadSafeTrackingStorage("http://localhost:8080/media")
	router, svc, _ := setupCatalogTestRouterWithStorage(t, store)
	product := createTestProduct(t, svc, "SKU-ADV-5MB-OVER")

	jpegHeader := []byte("\xff\xd8\xff\xe0\x00\x10JFIF\x00\x01\x01\x01")
	over5MBData := append(jpegHeader, make([]byte, storage.MaxFileSizeBytes-len(jpegHeader)+1)...)
	require.Equal(t, storage.MaxFileSizeBytes+1, len(over5MBData))

	req, err := createMultipartRequest(
		fmt.Sprintf("/api/v1/products/%s/images", product.ID.String()),
		"image", "over5mb.jpg", over5MBData,
		nil,
	)
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code, "5MB + 1 byte must be rejected with HTTP 400")
	var prob web.ProblemDetails
	err = json.Unmarshal(rec.Body.Bytes(), &prob)
	require.NoError(t, err)
	assert.Equal(t, "FILE_TOO_LARGE", prob.Code)

	// Storage must NOT have any objects uploaded
	assert.Empty(t, store.GetPutKeys(), "Storage Put must not be executed for oversized file")
}

// 3. Negative Test: 0 bytes empty file upload is rejected with HTTP 400 Bad Request
func TestAdversarialCatalog_ImageUpload_EmptyFile_ZeroBytes(t *testing.T) {
	store := newThreadSafeTrackingStorage("http://localhost:8080/media")
	router, svc, _ := setupCatalogTestRouterWithStorage(t, store)
	product := createTestProduct(t, svc, "SKU-ADV-EMPTY-FILE")

	req, err := createMultipartRequest(
		fmt.Sprintf("/api/v1/products/%s/images", product.ID.String()),
		"image", "empty.jpg", []byte{},
		nil,
	)
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code, "0-byte file must return HTTP 400")
	var prob web.ProblemDetails
	err = json.Unmarshal(rec.Body.Bytes(), &prob)
	require.NoError(t, err)
	assert.Contains(t, []string{"EMPTY_FILE", "FILE_TOO_LARGE"}, prob.Code)
	assert.Empty(t, store.GetPutKeys(), "Storage Put must not be executed for empty file")
}

// 4. Negative Test: Spoofed MIME extensions rejected via magic byte sniffing
func TestAdversarialCatalog_ImageUpload_SpoofedMIMEs(t *testing.T) {
	store := newThreadSafeTrackingStorage("http://localhost:8080/media")
	router, svc, _ := setupCatalogTestRouterWithStorage(t, store)
	product := createTestProduct(t, svc, "SKU-ADV-SPOOF")

	testCases := []struct {
		name     string
		fileName string
		content  []byte
	}{
		{
			name:     "PHP_Shell_with_JPG_extension",
			fileName: "shell.jpg",
			content:  []byte("<?php system($_GET['cmd']); ?>"),
		},
		{
			name:     "HTML_XSS_with_PNG_extension",
			fileName: "xss.png",
			content:  []byte("<script>alert('xss');</script>"),
		},
		{
			name:     "ELF_Executable_with_WebP_extension",
			fileName: "malware.webp",
			content:  []byte("\x7fELF\x02\x01\x01\x00\x00\x00\x00\x00\x00\x00\x00\x00"),
		},
		{
			name:     "Plain_Text_with_GIF_extension",
			fileName: "notes.gif",
			content:  []byte("This is just regular text pretending to be a gif"),
		},
		{
			name:     "PDF_Document_with_JPG_extension",
			fileName: "invoice.jpg",
			content:  []byte("%PDF-1.4\n%...\n"),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := createMultipartRequest(
				fmt.Sprintf("/api/v1/products/%s/images", product.ID.String()),
				"image", tc.fileName, tc.content,
				nil,
			)
			require.NoError(t, err)

			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			assert.Equal(t, http.StatusBadRequest, rec.Code, "Spoofed MIME must be rejected with HTTP 400")
			var prob web.ProblemDetails
			err = json.Unmarshal(rec.Body.Bytes(), &prob)
			require.NoError(t, err)
			assert.Equal(t, "INVALID_IMAGE", prob.Code)
		})
	}
}

// 5. Negative Test: Invalid Product and Image UUIDs return HTTP 400 INVALID_UUID
func TestAdversarialCatalog_InvalidUUIDs(t *testing.T) {
	store := storage.NewMemoryStorage("http://localhost:8080/media")
	router, _, _ := setupCatalogTestRouterWithStorage(t, store)

	invalidUUIDs := []string{
		"not-a-uuid",
		"12345",
		"00000000-0000-0000-0000-00000000000",  // 35 chars (too short)
		"00000000-0000-0000-0000-0000000000000", // 37 chars (too long)
		"11111111-1111-1111-1111-11111111111G", // invalid hex character
	}

	for _, badID := range invalidUUIDs {
		// POST /api/v1/products/{id}/images
		t.Run("POST_InvalidProductID_"+badID, func(t *testing.T) {
			req, err := createMultipartRequest(
				fmt.Sprintf("/api/v1/products/%s/images", badID),
				"image", "pic.jpg", []byte("\xff\xd8\xff\xe0\x00\x10JFIF\x00\x01\x01\x01data"),
				nil,
			)
			require.NoError(t, err)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			assert.Equal(t, http.StatusBadRequest, rec.Code)
		})

		// GET /api/v1/products/{id}/images
		t.Run("GET_InvalidProductID_"+badID, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/products/%s/images", badID), nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			assert.Equal(t, http.StatusBadRequest, rec.Code)
		})

		// DELETE /api/v1/products/{id}/images/{imageId}
		t.Run("DELETE_InvalidImageID_"+badID, func(t *testing.T) {
			validProdID := uuid.New()
			req := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/api/v1/products/%s/images/%s", validProdID.String(), badID), nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			assert.Equal(t, http.StatusBadRequest, rec.Code)
		})
	}
}

// 6. Fast 404 Invariant: Non-existent product aborts immediately with zero storage Put calls
func TestAdversarialCatalog_Fast404_NoStoragePut(t *testing.T) {
	store := newThreadSafeTrackingStorage("http://localhost:8080/media")
	router, _, _ := setupCatalogTestRouterWithStorage(t, store)

	nonExistentID := uuid.New()
	jpegData := append([]byte("\xff\xd8\xff\xe0\x00\x10JFIF\x00\x01\x01\x01"), bytes.Repeat([]byte{0x01}, 1024)...)

	req, err := createMultipartRequest(
		fmt.Sprintf("/api/v1/products/%s/images", nonExistentID.String()),
		"image", "fast404.jpg", jpegData,
		nil,
	)
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code, "Non-existent product must return 404")
	assert.Empty(t, store.GetPutKeys(), "Fast 404 must prevent any upload to storage")
}

// 7. Invariant: Orphaned upload compensation executes even if HTTP request context is canceled
func TestAdversarialCatalog_OrphanedUploadCompensation_WithCanceledContext(t *testing.T) {
	store := newThreadSafeTrackingStorage("http://localhost:8080/media")
	baseRepo := newMemoryCatalogRepo()
	failRepo := &failingCatalogRepo{memoryCatalogRepo: baseRepo}
	svc := catalog.NewService(failRepo, nil)
	handler := catalog.NewHandlerWithStorage(svc, store, nil)

	r := chi.NewRouter()
	r.Mount("/api/v1/products", handler.ProductRoutes())

	product := createTestProduct(t, svc, "SKU-ADV-CANCEL-CTX")

	jpegData := append([]byte("\xff\xd8\xff\xe0\x00\x10JFIF\x00\x01\x01\x01"), bytes.Repeat([]byte{0x05}, 100)...)

	// Create a context that is already canceled
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // canceled!

	req, err := createMultipartRequest(
		fmt.Sprintf("/api/v1/products/%s/images", product.ID.String()),
		"image", "cancel.jpg", jpegData,
		nil,
	)
	require.NoError(t, err)
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	// DB failed (simulated failure), handler returns 500
	assert.Equal(t, http.StatusInternalServerError, rec.Code)

	// Compensation must have occurred: Put was called, and Delete was called for the same key
	putKeys := store.GetPutKeys()
	deletedKeys := store.GetDeletedKeys()

	require.NotEmpty(t, putKeys, "Put must have been called before DB insert")
	assert.Contains(t, deletedKeys, putKeys[0], "Orphaned blob must be deleted via compensation even when request context is canceled")
}

// 8. Invariant: Primary image exclusivity — marking an image primary unsets primary on older images
func TestAdversarialCatalog_PrimaryImageExclusivity(t *testing.T) {
	store := storage.NewMemoryStorage("http://localhost:8080/media")
	router, svc, _ := setupCatalogTestRouterWithStorage(t, store)
	product := createTestProduct(t, svc, "SKU-ADV-PRIMARY")

	jpegData := append([]byte("\xff\xd8\xff\xe0\x00\x10JFIF\x00\x01\x01\x01"), bytes.Repeat([]byte{0x01}, 50)...)

	// Upload Image 1 as primary
	req1, _ := createMultipartRequest(
		fmt.Sprintf("/api/v1/products/%s/images", product.ID.String()),
		"image", "img1.jpg", jpegData,
		map[string]string{"is_primary": "true", "sort_order": "1"},
	)
	rec1 := httptest.NewRecorder()
	router.ServeHTTP(rec1, req1)
	require.Equal(t, http.StatusCreated, rec1.Code)

	// Upload Image 2 as primary
	req2, _ := createMultipartRequest(
		fmt.Sprintf("/api/v1/products/%s/images", product.ID.String()),
		"image", "img2.jpg", jpegData,
		map[string]string{"is_primary": "true", "sort_order": "2"},
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
	require.NoError(t, json.Unmarshal(listRec.Body.Bytes(), &list))
	require.Len(t, list, 2)

	var primaryCount int
	for _, img := range list {
		if img.IsPrimary {
			primaryCount++
			assert.Equal(t, 2, img.SortOrder, "Only the latest image should be primary")
		}
	}
	assert.Equal(t, 1, primaryCount, "Exactly one image must be marked primary")
}

// 9. Stress Harness: 50 concurrent goroutines performing uploads and listing
func TestAdversarialCatalog_ConcurrentImageOperations(t *testing.T) {
	store := newThreadSafeTrackingStorage("http://localhost:8080/media")
	router, svc, _ := setupCatalogTestRouterWithStorage(t, store)
	product := createTestProduct(t, svc, "SKU-ADV-CONCURRENT")

	concurrency := 50
	var wg sync.WaitGroup
	wg.Add(concurrency)

	jpegData := append([]byte("\xff\xd8\xff\xe0\x00\x10JFIF\x00\x01\x01\x01"), bytes.Repeat([]byte{0x01}, 100)...)

	var successUploads int
	var mu sync.Mutex

	for i := 0; i < concurrency; i++ {
		workerID := i
		go func() {
			defer wg.Done()

			if workerID%2 == 0 {
				// Upload operation
				req, err := createMultipartRequest(
					fmt.Sprintf("/api/v1/products/%s/images", product.ID.String()),
					"image", fmt.Sprintf("worker-%d.jpg", workerID), jpegData,
					map[string]string{"sort_order": fmt.Sprintf("%d", workerID)},
				)
				if err != nil {
					t.Errorf("worker %d failed to create request: %v", workerID, err)
					return
				}
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)

				if rec.Code == http.StatusCreated {
					mu.Lock()
					successUploads++
					mu.Unlock()
				} else {
					t.Errorf("worker %d upload unexpected code: %d", workerID, rec.Code)
				}
			} else {
				// List operation
				req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/products/%s/images", product.ID.String()), nil)
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)
				if rec.Code != http.StatusOK {
					t.Errorf("worker %d list unexpected code: %d", workerID, rec.Code)
				}
			}
		}()
	}

	wg.Wait()

	assert.Equal(t, concurrency/2, successUploads, "All concurrent uploads must succeed")

	// Final list must contain all uploaded images
	finalList, err := svc.GetProductImages(context.Background(), product.ID)
	require.NoError(t, err)
	assert.Len(t, finalList, concurrency/2)
}

// 10. Invariant Verification: Zero floats in ProductImage struct & SQL migration schema
func TestAdversarialCatalog_ZeroFloats_ProductImageInvariant(t *testing.T) {
	// Inspect ProductImage struct fields
	imgType := reflect.TypeOf(catalog.ProductImage{})
	for i := 0; i < imgType.NumField(); i++ {
		field := imgType.Field(i)
		k := field.Type.Kind()
		assert.False(t, k == reflect.Float32 || k == reflect.Float64,
			"ProductImage struct field %s must not be float, got %v", field.Name, k)
	}

	// Read migration 00008 and assert no FLOAT / REAL / DOUBLE / NUMERIC
	migrationPath := "../../../migrations/00008_create_product_images_tables.sql"
	content, err := os.ReadFile(migrationPath)
	require.NoError(t, err)

	upperSQL := strings.ToUpper(string(content))
	forbiddenSQLTypes := []string{"FLOAT", "DOUBLE", "REAL", "NUMERIC", "DECIMAL"}
	for _, forbidden := range forbiddenSQLTypes {
		assert.NotContains(t, upperSQL, " "+forbidden+" ", "Migration 00008 must not contain forbidden float SQL type: %s", forbidden)
		assert.NotContains(t, upperSQL, " "+forbidden+"(", "Migration 00008 must not contain forbidden float SQL type: %s", forbidden)
	}
	assert.Contains(t, upperSQL, "SIZE_BYTES BIGINT NOT NULL", "size_bytes must be BIGINT")
}
