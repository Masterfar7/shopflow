package storage

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"shopflow/internal/platform/config"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateImageUpload(t *testing.T) {
	// Valid WebP
	ext, err := ValidateImageUpload(1024, "image/webp")
	require.NoError(t, err)
	assert.Equal(t, ".webp", ext)

	// Valid JPEG
	ext, err = ValidateImageUpload(2048, "image/jpeg")
	require.NoError(t, err)
	assert.Equal(t, ".jpg", ext)

	// Valid PNG
	ext, err = ValidateImageUpload(4096, "image/png")
	require.NoError(t, err)
	assert.Equal(t, ".png", ext)

	// Valid GIF
	ext, err = ValidateImageUpload(512, "image/gif")
	require.NoError(t, err)
	assert.Equal(t, ".gif", ext)

	// Invalid format (e.g. text/plain, application/pdf)
	_, err = ValidateImageUpload(1024, "text/plain")
	assert.ErrorIs(t, err, ErrInvalidContentType)

	// Oversized (>5MB)
	_, err = ValidateImageUpload(6*1024*1024, "image/png")
	assert.ErrorIs(t, err, ErrFileTooLarge)

	// Zero or negative size
	_, err = ValidateImageUpload(0, "image/png")
	assert.ErrorIs(t, err, ErrFileTooLarge)
}

func TestDetectImageContentType(t *testing.T) {
	// WebP magic header
	webpData := []byte("RIFF\x24\x00\x00\x00WEBPVP8 ")
	assert.Equal(t, "image/webp", DetectImageContentType(webpData))

	// PNG magic header
	pngData := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	assert.Equal(t, "image/png", DetectImageContentType(pngData))

	// JPEG magic header
	jpegData := []byte("\xff\xd8\xff\xe0\x00\x10JFIF\x00\x01\x01\x01")
	assert.Equal(t, "image/jpeg", DetectImageContentType(jpegData))

	// GIF magic header
	gifData := []byte("GIF89a\x01\x00\x01\x00\x80\x00\x00")
	assert.Equal(t, "image/gif", DetectImageContentType(gifData))

	// Plain text
	textData := []byte("Hello world, this is not an image file")
	assert.Contains(t, DetectImageContentType(textData), "text/plain")
}

func TestBuildProductImageKey(t *testing.T) {
	prodID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	imgID := uuid.MustParse("22222222-2222-2222-2222-222222222222")

	key := BuildProductImageKey(prodID, imgID, ".webp")
	assert.Equal(t, "products/11111111-1111-1111-1111-111111111111/22222222-2222-2222-2222-222222222222.webp", key)
}

func TestMemoryStorage_PutGetDelete(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStorage("http://localhost:8080/media")

	payload := []byte("dummy-image-bytes-data")
	key := "products/test/image.jpg"

	// Put
	obj, err := store.Put(ctx, key, bytes.NewReader(payload), int64(len(payload)), "image/jpeg")
	require.NoError(t, err)
	assert.Equal(t, key, obj.Key)
	assert.Equal(t, "image/jpeg", obj.ContentType)
	assert.Equal(t, int64(len(payload)), obj.SizeBytes)
	assert.True(t, strings.HasPrefix(obj.URL, "http://localhost:8080/media/"))

	// Get
	rc, readObj, err := store.Get(ctx, key)
	require.NoError(t, err)
	defer rc.Close()
	assert.Equal(t, obj.SizeBytes, readObj.SizeBytes)

	// Delete
	err = store.Delete(ctx, key)
	require.NoError(t, err)

	// Get after Delete
	_, _, err = store.Get(ctx, key)
	assert.ErrorIs(t, err, ErrObjectNotFound)
}

func TestNewStorageFromConfig(t *testing.T) {
	ctx := context.Background()
	cfg := config.Config{
		StorageProvider: "memory",
		MediaBaseURL:    "http://localhost:8080/media",
	}

	store, err := NewStorageFromConfig(ctx, cfg, nil)
	require.NoError(t, err)
	assert.NotNil(t, store)

	url := store.GetURL("test.png")
	assert.Equal(t, "http://localhost:8080/media/test.png", url)
}
