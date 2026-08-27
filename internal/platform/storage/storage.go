package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"sync"

	"shopflow/internal/platform/config"

	"github.com/google/uuid"
)

var (
	ErrInvalidContentType = errors.New("unsupported image format: must be image/jpeg, image/png, image/webp or image/gif")
	ErrFileTooLarge        = errors.New("file exceeds maximum allowed size (5MB)")
	ErrObjectNotFound     = errors.New("object not found in storage")
)

const (
	MaxFileSizeBytes = 5 * 1024 * 1024 // 5 MB
)

// AllowedContentTypes defines supported image formats for products.
var AllowedContentTypes = map[string]string{
	"image/jpeg": ".jpg",
	"image/png":  ".png",
	"image/webp": ".webp",
	"image/gif":  ".gif",
}

// StoredObject represents an object stored in BlobStorage.
type StoredObject struct {
	Key         string
	ContentType string
	SizeBytes   int64
	URL         string
	Data        []byte
}

// BlobStorage defines the abstract interface for object storage operations.
type BlobStorage interface {
	Put(ctx context.Context, key string, data io.Reader, sizeBytes int64, contentType string) (*StoredObject, error)
	Get(ctx context.Context, key string) (io.ReadCloser, *StoredObject, error)
	Delete(ctx context.Context, key string) error
	GetURL(key string) string
}

// ValidateImageUpload validates file size and MIME content type.
func ValidateImageUpload(sizeBytes int64, contentType string) (string, error) {
	if sizeBytes <= 0 || sizeBytes > MaxFileSizeBytes {
		return "", ErrFileTooLarge
	}
	ext, ok := AllowedContentTypes[strings.ToLower(contentType)]
	if !ok {
		return "", ErrInvalidContentType
	}
	return ext, nil
}

// DetectImageContentType sniffs MIME type from magic bytes, handling WebP ("RIFF....WEBP") explicitly.
func DetectImageContentType(data []byte) string {
	if len(data) >= 12 && string(data[0:4]) == "RIFF" && string(data[8:12]) == "WEBP" {
		return "image/webp"
	}
	return http.DetectContentType(data)
}

// BuildProductImageKey generates a deterministic, collision-free storage path.
func BuildProductImageKey(productID uuid.UUID, imageID uuid.UUID, ext string) string {
	cleanExt := strings.TrimPrefix(ext, ".")
	return fmt.Sprintf("products/%s/%s.%s", productID.String(), imageID.String(), cleanExt)
}

// NewStorageFromConfig creates a BlobStorage instance based on application configuration.
func NewStorageFromConfig(ctx context.Context, cfg config.Config, logger *slog.Logger) (BlobStorage, error) {
	if strings.EqualFold(cfg.StorageProvider, "memory") || cfg.S3Endpoint == "" {
		if logger != nil {
			logger.Info("using in-memory blob storage")
		}
		return NewMemoryStorage(cfg.MediaBaseURL), nil
	}
	return NewS3Storage(ctx, S3Config{
		Endpoint:  cfg.S3Endpoint,
		Bucket:    cfg.S3Bucket,
		AccessKey: cfg.S3AccessKey,
		SecretKey: cfg.S3SecretKey,
		UseSSL:    cfg.S3UseSSL,
		Region:    cfg.S3Region,
		BaseURL:   cfg.MediaBaseURL,
	}, logger)
}

// MemoryStorage is an in-memory implementation of BlobStorage for tests & standalone mode.
type MemoryStorage struct {
	mu      sync.RWMutex
	objects map[string]*StoredObject
	baseURL string
}

// NewMemoryStorage creates a new in-memory object storage.
func NewMemoryStorage(baseURL string) *MemoryStorage {
	if baseURL == "" {
		baseURL = "http://localhost:8080/media"
	}
	return &MemoryStorage{
		objects: make(map[string]*StoredObject),
		baseURL: strings.TrimSuffix(baseURL, "/"),
	}
}

func (m *MemoryStorage) Put(ctx context.Context, key string, r io.Reader, sizeBytes int64, contentType string) (*StoredObject, error) {
	ext, err := ValidateImageUpload(sizeBytes, contentType)
	_ = ext
	if err != nil {
		return nil, err
	}

	buf, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("read storage payload: %w", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	obj := &StoredObject{
		Key:         key,
		ContentType: contentType,
		SizeBytes:   int64(len(buf)),
		URL:         fmt.Sprintf("%s/%s", m.baseURL, key),
		Data:        buf,
	}
	m.objects[key] = obj
	return obj, nil
}

func (m *MemoryStorage) Get(ctx context.Context, key string) (io.ReadCloser, *StoredObject, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	obj, ok := m.objects[key]
	if !ok {
		return nil, nil, ErrObjectNotFound
	}
	return io.NopCloser(strings.NewReader(string(obj.Data))), obj, nil
}

func (m *MemoryStorage) Delete(ctx context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.objects, key)
	return nil
}

func (m *MemoryStorage) GetURL(key string) string {
	return fmt.Sprintf("%s/%s", m.baseURL, filepath.ToSlash(key))
}
