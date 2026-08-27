package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// S3Config holds connection and bucket configuration for MinIO/S3 storage.
type S3Config struct {
	Endpoint  string
	Bucket    string
	AccessKey string
	SecretKey string
	UseSSL    bool
	Region    string
	BaseURL   string
}

// S3Storage implements BlobStorage backed by an S3-compatible service (e.g. MinIO).
type S3Storage struct {
	client  *minio.Client
	bucket  string
	baseURL string
	logger  *slog.Logger
}

// NewS3Storage creates and initializes a new S3Storage instance.
func NewS3Storage(ctx context.Context, cfg S3Config, logger *slog.Logger) (*S3Storage, error) {
	if logger == nil {
		logger = slog.Default()
	}

	opts := &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
		Region: cfg.Region,
	}

	client, err := minio.New(cfg.Endpoint, opts)
	if err != nil {
		return nil, fmt.Errorf("initialize minio client: %w", err)
	}

	baseURL := cfg.BaseURL
	if baseURL == "" {
		proto := "http"
		if cfg.UseSSL {
			proto = "https"
		}
		baseURL = fmt.Sprintf("%s://%s/%s", proto, cfg.Endpoint, cfg.Bucket)
	}
	baseURL = strings.TrimSuffix(baseURL, "/")

	store := &S3Storage{
		client:  client,
		bucket:  cfg.Bucket,
		baseURL: baseURL,
		logger:  logger,
	}

	// Verify or create bucket if reachable
	exists, err := client.BucketExists(ctx, cfg.Bucket)
	if err != nil {
		logger.Warn("unable to verify s3 bucket existence", "bucket", cfg.Bucket, "err", err)
	} else if !exists {
		err = client.MakeBucket(ctx, cfg.Bucket, minio.MakeBucketOptions{Region: cfg.Region})
		if err != nil {
			logger.Warn("unable to create s3 bucket", "bucket", cfg.Bucket, "err", err)
		} else {
			logger.Info("created s3 bucket", "bucket", cfg.Bucket)
		}
	}

	return store, nil
}

func (s *S3Storage) Put(ctx context.Context, key string, data io.Reader, sizeBytes int64, contentType string) (*StoredObject, error) {
	ext, err := ValidateImageUpload(sizeBytes, contentType)
	_ = ext
	if err != nil {
		return nil, err
	}

	uploadOpts := minio.PutObjectOptions{
		ContentType: contentType,
	}

	cleanKey := strings.TrimPrefix(key, "/")
	uploadInfo, err := s.client.PutObject(ctx, s.bucket, cleanKey, data, sizeBytes, uploadOpts)
	if err != nil {
		return nil, fmt.Errorf("s3 put object %s: %w", cleanKey, err)
	}

	return &StoredObject{
		Key:         cleanKey,
		ContentType: contentType,
		SizeBytes:   uploadInfo.Size,
		URL:         s.GetURL(cleanKey),
	}, nil
}

func (s *S3Storage) Get(ctx context.Context, key string) (io.ReadCloser, *StoredObject, error) {
	cleanKey := strings.TrimPrefix(key, "/")
	obj, err := s.client.GetObject(ctx, s.bucket, cleanKey, minio.GetObjectOptions{})
	if err != nil {
		return nil, nil, fmt.Errorf("s3 get object %s: %w", cleanKey, err)
	}

	stat, err := obj.Stat()
	if err != nil {
		_ = obj.Close()
		errResponse := minio.ToErrorResponse(err)
		if errResponse.Code == "NoSuchKey" || errors.Is(err, minio.ErrorResponse{Code: "NoSuchKey"}) {
			return nil, nil, ErrObjectNotFound
		}
		return nil, nil, fmt.Errorf("s3 stat object %s: %w", cleanKey, err)
	}

	stored := &StoredObject{
		Key:         cleanKey,
		ContentType: stat.ContentType,
		SizeBytes:   stat.Size,
		URL:         s.GetURL(cleanKey),
	}

	return obj, stored, nil
}

func (s *S3Storage) Delete(ctx context.Context, key string) error {
	cleanKey := strings.TrimPrefix(key, "/")
	err := s.client.RemoveObject(ctx, s.bucket, cleanKey, minio.RemoveObjectOptions{})
	if err != nil {
		return fmt.Errorf("s3 delete object %s: %w", cleanKey, err)
	}
	return nil
}

func (s *S3Storage) GetURL(key string) string {
	cleanKey := strings.TrimPrefix(key, "/")
	return fmt.Sprintf("%s/%s", s.baseURL, cleanKey)
}
