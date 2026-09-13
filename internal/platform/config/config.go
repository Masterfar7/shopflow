package config

import (
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config encapsulates environment variables with sensible defaults for ShopFlow.
type Config struct {
	HTTPPort           string
	DatabaseURL        string
	KafkaBrokers       []string
	RedisAddr          string
	RedisPassword      string
	RedisDB            int
	LogLevel           slog.Level
	StorageProvider    string // "memory", "minio", "s3"
	S3Endpoint         string
	S3Bucket           string
	S3AccessKey        string
	S3SecretKey        string
	S3Region           string
	S3UseSSL           bool
	MediaBaseURL       string
	SearchProvider     string // "memory", "opensearch"
	OpenSearchURL      string
	OpenSearchIndex    string
	OpenSearchUsername string
	OpenSearchPassword string
	OpenSearchTimeout  time.Duration
	EmailProvider      string // "memory", "smtp"
	SMTPHost           string
	SMTPPort           int
	SMTPUsername       string
	SMTPPassword       string
	EmailFrom          string
}

// Load reads configuration from environment variables or returns defaults.
func Load() Config {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		pgPort := os.Getenv("POSTGRES_PORT")
		if pgPort == "" {
			pgPort = "5434"
		}
		dbURL = "postgres://shopflow:shopflow_secret@localhost:" + pgPort + "/shopflow?sslmode=disable"
	}

	kafkaBrokersEnv := os.Getenv("KAFKA_BROKERS")
	var kafkaBrokers []string
	if kafkaBrokersEnv == "" {
		kafkaBrokers = []string{"localhost:9094"}
	} else {
		for _, b := range strings.Split(kafkaBrokersEnv, ",") {
			trimmed := strings.TrimSpace(b)
			if trimmed != "" {
				kafkaBrokers = append(kafkaBrokers, trimmed)
			}
		}
	}

	redisAddr := os.Getenv("REDIS_ADDR")
	if redisAddr == "" {
		redisAddr = "localhost:6379"
	}

	redisPassword := os.Getenv("REDIS_PASSWORD") // empty string = no auth

	redisDB := 0

	logLevel := slog.LevelInfo
	switch strings.ToUpper(os.Getenv("LOG_LEVEL")) {
	case "DEBUG":
		logLevel = slog.LevelDebug
	case "WARN", "WARNING":
		logLevel = slog.LevelWarn
	case "ERROR":
		logLevel = slog.LevelError
	}

	storageProvider := os.Getenv("STORAGE_PROVIDER")
	if storageProvider == "" {
		storageProvider = "memory"
	}

	s3Endpoint := os.Getenv("S3_ENDPOINT")
	s3Bucket := os.Getenv("S3_BUCKET")
	if s3Bucket == "" {
		s3Bucket = "shopflow-media"
	}

	s3AccessKey := os.Getenv("S3_ACCESS_KEY")
	if s3AccessKey == "" {
		s3AccessKey = "minioadmin"
	}

	s3SecretKey := os.Getenv("S3_SECRET_KEY")
	if s3SecretKey == "" {
		s3SecretKey = "minioadmin_secret"
	}

	s3Region := os.Getenv("S3_REGION")
	if s3Region == "" {
		s3Region = "us-east-1"
	}

	s3UseSSL := strings.EqualFold(os.Getenv("S3_USE_SSL"), "true")

	mediaBaseURL := os.Getenv("MEDIA_BASE_URL")
	if mediaBaseURL == "" {
		if strings.EqualFold(storageProvider, "minio") || strings.EqualFold(storageProvider, "s3") {
			proto := "http"
			if s3UseSSL {
				proto = "https"
			}
			endpoint := s3Endpoint
			if endpoint == "" {
				endpoint = "localhost:9000"
			}
			mediaBaseURL = proto + "://" + endpoint + "/" + s3Bucket
		} else {
			mediaBaseURL = "http://localhost:8080/media"
		}
	}

	searchProvider := os.Getenv("SEARCH_PROVIDER")
	if searchProvider == "" {
		searchProvider = "memory"
	}

	openSearchURL := os.Getenv("OPENSEARCH_URL")

	openSearchIndex := os.Getenv("OPENSEARCH_INDEX")
	if openSearchIndex == "" {
		openSearchIndex = "shopflow_products"
	}

	openSearchUsername := os.Getenv("OPENSEARCH_USERNAME")
	openSearchPassword := os.Getenv("OPENSEARCH_PASSWORD")

	openSearchTimeout := 5 * time.Second
	if timeoutStr := os.Getenv("OPENSEARCH_TIMEOUT"); timeoutStr != "" {
		if d, err := time.ParseDuration(timeoutStr); err == nil && d > 0 {
			openSearchTimeout = d
		}
	}

	emailProvider := os.Getenv("EMAIL_PROVIDER")
	if emailProvider == "" {
		emailProvider = "smtp"
	}

	smtpHost := os.Getenv("SMTP_HOST")
	if smtpHost == "" {
		smtpHost = "localhost"
	}

	smtpPort := 1025
	if portStr := os.Getenv("SMTP_PORT"); portStr != "" {
		if p, err := strconv.Atoi(portStr); err == nil && p > 0 {
			smtpPort = p
		}
	}

	smtpUsername := os.Getenv("SMTP_USERNAME")
	smtpPassword := os.Getenv("SMTP_PASSWORD")

	emailFrom := os.Getenv("EMAIL_FROM")
	if emailFrom == "" {
		emailFrom = os.Getenv("SMTP_FROM")
	}
	if emailFrom == "" {
		emailFrom = "no-reply@shopflow.io"
	}

	return Config{
		HTTPPort:           port,
		DatabaseURL:        dbURL,
		KafkaBrokers:       kafkaBrokers,
		RedisAddr:          redisAddr,
		RedisPassword:      redisPassword,
		RedisDB:            redisDB,
		LogLevel:           logLevel,
		StorageProvider:    storageProvider,
		S3Endpoint:         s3Endpoint,
		S3Bucket:           s3Bucket,
		S3AccessKey:        s3AccessKey,
		S3SecretKey:        s3SecretKey,
		S3Region:           s3Region,
		S3UseSSL:           s3UseSSL,
		MediaBaseURL:       mediaBaseURL,
		SearchProvider:     searchProvider,
		OpenSearchURL:      openSearchURL,
		OpenSearchIndex:    openSearchIndex,
		OpenSearchUsername: openSearchUsername,
		OpenSearchPassword: openSearchPassword,
		OpenSearchTimeout:  openSearchTimeout,
		EmailProvider:      emailProvider,
		SMTPHost:           smtpHost,
		SMTPPort:           smtpPort,
		SMTPUsername:       smtpUsername,
		SMTPPassword:       smtpPassword,
		EmailFrom:          emailFrom,
	}
}
