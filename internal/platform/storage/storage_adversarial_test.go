package storage

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"reflect"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAdversarial_ValidateImageUpload_Boundaries(t *testing.T) {
	formats := []string{"image/jpeg", "image/png", "image/webp", "image/gif"}

	t.Run("Zero_And_Negative_Sizes", func(t *testing.T) {
		for _, f := range formats {
			_, err := ValidateImageUpload(0, f)
			assert.ErrorIs(t, err, ErrFileTooLarge, "0 bytes must return ErrFileTooLarge for %s", f)

			_, err = ValidateImageUpload(-1, f)
			assert.ErrorIs(t, err, ErrFileTooLarge, "-1 bytes must return ErrFileTooLarge for %s", f)

			_, err = ValidateImageUpload(-1048576, f)
			assert.ErrorIs(t, err, ErrFileTooLarge, "negative size must return ErrFileTooLarge for %s", f)
		}
	})

	t.Run("Min_Valid_Size_1_Byte", func(t *testing.T) {
		for _, f := range formats {
			ext, err := ValidateImageUpload(1, f)
			require.NoError(t, err, "1 byte must be valid for %s", f)
			assert.NotEmpty(t, ext)
		}
	})

	t.Run("Exact_5MB_Boundary", func(t *testing.T) {
		exact5MB := int64(MaxFileSizeBytes) // 5,242,880 bytes
		for _, f := range formats {
			ext, err := ValidateImageUpload(exact5MB, f)
			require.NoError(t, err, "Exactly 5MB must be valid for %s", f)
			assert.NotEmpty(t, ext)
		}
	})

	t.Run("Exact_5MB_Plus_1_Byte_Rejected", func(t *testing.T) {
		over5MB := int64(MaxFileSizeBytes) + 1 // 5,242,881 bytes
		for _, f := range formats {
			_, err := ValidateImageUpload(over5MB, f)
			assert.ErrorIs(t, err, ErrFileTooLarge, "5MB + 1 byte must return ErrFileTooLarge for %s", f)
		}
	})

	t.Run("Extreme_Sizes", func(t *testing.T) {
		_, err := ValidateImageUpload(math.MaxInt64, "image/png")
		assert.ErrorIs(t, err, ErrFileTooLarge)

		_, err = ValidateImageUpload(100*1024*1024, "image/jpeg")
		assert.ErrorIs(t, err, ErrFileTooLarge)
	})

	t.Run("Case_Insensitive_MIME", func(t *testing.T) {
		ext, err := ValidateImageUpload(1024, "IMAGE/JPEG")
		require.NoError(t, err)
		assert.Equal(t, ".jpg", ext)

		ext, err = ValidateImageUpload(1024, "Image/WebP")
		require.NoError(t, err)
		assert.Equal(t, ".webp", ext)
	})

	t.Run("Unsupported_MIMEs", func(t *testing.T) {
		unsupported := []string{
			"application/pdf",
			"image/svg+xml",
			"image/bmp",
			"image/tiff",
			"text/html",
			"application/octet-stream",
			"video/mp4",
			"",
		}
		for _, u := range unsupported {
			_, err := ValidateImageUpload(1024, u)
			assert.ErrorIs(t, err, ErrInvalidContentType, "MIME %q must be rejected", u)
		}
	})
}

func TestAdversarial_DetectImageContentType_EdgeCases(t *testing.T) {
	// 1. Empty byte slice
	assert.Equal(t, "text/plain; charset=utf-8", DetectImageContentType([]byte{}))

	// 2. Short slices (< 4 bytes)
	assert.Equal(t, "text/plain; charset=utf-8", DetectImageContentType([]byte("RI")))

	// 3. RIFF but not WEBP (e.g. WAV file)
	wavHeader := []byte("RIFF\x24\x00\x00\x00WAVEfmt ")
	assert.NotEqual(t, "image/webp", DetectImageContentType(wavHeader))

	// 4. Truncated WebP header (< 12 bytes)
	truncatedWebP := []byte("RIFF\x24\x00\x00\x00WEB")
	assert.NotEqual(t, "image/webp", DetectImageContentType(truncatedWebP))

	// 5. Exact 12-byte WebP header
	exactWebP := []byte("RIFF\x00\x00\x00\x00WEBP")
	assert.Equal(t, "image/webp", DetectImageContentType(exactWebP))

	// 6. Polyglot / Spoofed: Shell script with fake GIF header
	polyglot := []byte("GIF89a; <?php system($_GET['cmd']); ?>")
	// Sniffer detects image/gif from magic bytes
	assert.Equal(t, "image/gif", DetectImageContentType(polyglot))

	// 7. Binary payload that is not an image (e.g. ELF header)
	elfHeader := []byte("\x7fELF\x02\x01\x01\x00\x00\x00\x00\x00\x00\x00\x00\x00")
	assert.NotContains(t, DetectImageContentType(elfHeader), "image/")
}

func TestAdversarial_MemoryStorage_HighConcurrencyStress(t *testing.T) {
	store := NewMemoryStorage("http://localhost:8080/media")
	ctx := context.Background()

	concurrency := 100
	var wg sync.WaitGroup
	wg.Add(concurrency)

	for i := 0; i < concurrency; i++ {
		workerID := i
		go func() {
			defer wg.Done()
			key := fmt.Sprintf("products/worker-%d/image.png", workerID%10)
			payload := bytes.Repeat([]byte{byte(workerID)}, 256)

			// Concurrent Put
			obj, err := store.Put(ctx, key, bytes.NewReader(payload), int64(len(payload)), "image/png")
			if err != nil {
				t.Errorf("worker %d Put failed: %v", workerID, err)
				return
			}
			if obj.Key != key {
				t.Errorf("worker %d unexpected key %s", workerID, obj.Key)
			}

			// Concurrent Get
			rc, readObj, err := store.Get(ctx, key)
			if err == nil {
				_ = rc.Close()
				if readObj.Key != key {
					t.Errorf("worker %d read mismatched key", workerID)
				}
			}

			// Concurrent GetURL
			url := store.GetURL(key)
			if url == "" {
				t.Errorf("worker %d empty URL", workerID)
			}

			// Concurrent Delete on some keys
			if workerID%3 == 0 {
				_ = store.Delete(ctx, key)
			}
		}()
	}

	wg.Wait()
}

func TestAdversarial_MemoryStorage_OverwriteAndDeletion(t *testing.T) {
	store := NewMemoryStorage("http://localhost:8080/media")
	ctx := context.Background()
	key := "products/overwrite/test.jpg"

	// Put 1
	data1 := []byte("version-1-bytes")
	_, err := store.Put(ctx, key, bytes.NewReader(data1), int64(len(data1)), "image/jpeg")
	require.NoError(t, err)

	// Put 2 (overwrite)
	data2 := []byte("version-2-bytes-longer-content")
	obj2, err := store.Put(ctx, key, bytes.NewReader(data2), int64(len(data2)), "image/jpeg")
	require.NoError(t, err)
	assert.Equal(t, int64(len(data2)), obj2.SizeBytes)

	// Verify Get returns version 2
	rc, readObj, err := store.Get(ctx, key)
	require.NoError(t, err)
	defer rc.Close()
	assert.Equal(t, int64(len(data2)), readObj.SizeBytes)
	assert.Equal(t, data2, readObj.Data)

	// Deleting non-existent key does not fail
	err = store.Delete(ctx, "non-existent-key")
	require.NoError(t, err)

	// Delete existing
	err = store.Delete(ctx, key)
	require.NoError(t, err)

	// Get after Delete returns ErrObjectNotFound
	_, _, err = store.Get(ctx, key)
	assert.ErrorIs(t, err, ErrObjectNotFound)
}

func TestAdversarial_BuildProductImageKey_CollisionAndDeterminism(t *testing.T) {
	pID1 := uuid.MustParse("10000000-0000-0000-0000-000000000001")
	pID2 := uuid.MustParse("10000000-0000-0000-0000-000000000002")
	imgID := uuid.MustParse("20000000-0000-0000-0000-000000000001")

	key1 := BuildProductImageKey(pID1, imgID, ".jpg")
	key2 := BuildProductImageKey(pID2, imgID, ".jpg")
	key3 := BuildProductImageKey(pID1, imgID, "jpg") // without leading dot

	assert.NotEqual(t, key1, key2, "Different products must yield different keys")
	assert.Equal(t, key1, key3, "Leading dot in ext must be handled consistently")
	assert.Equal(t, "products/10000000-0000-0000-0000-000000000001/20000000-0000-0000-0000-000000000001.jpg", key1)
}

func TestAdversarial_ZeroFloats_StorageStructs(t *testing.T) {
	// Verify StoredObject contains zero float fields
	objType := reflect.TypeOf(StoredObject{})
	for i := 0; i < objType.NumField(); i++ {
		field := objType.Field(i)
		k := field.Type.Kind()
		assert.False(t, k == reflect.Float32 || k == reflect.Float64,
			"StoredObject field %s must not be float, got %v", field.Name, k)
	}

	// Verify S3Config contains zero float fields
	cfgType := reflect.TypeOf(S3Config{})
	for i := 0; i < cfgType.NumField(); i++ {
		field := cfgType.Field(i)
		k := field.Type.Kind()
		assert.False(t, k == reflect.Float32 || k == reflect.Float64,
			"S3Config field %s must not be float, got %v", field.Name, k)
	}
}
