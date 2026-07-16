package harness

import (
	"os"
	"path/filepath"
	"strings"
)

// Mode represents test execution mode.
type Mode string

const (
	ModeAuto       Mode = "auto"
	ModeContainers Mode = "containers"
	ModeLive       Mode = "live"
)

// Config encapsulates test execution parameters and connection coordinates.
type Config struct {
	BaseURL              string
	DatabaseURL          string
	KafkaBrokers         []string
	Mode                 Mode
	MigrationsDir        string
	HTTPClientTimeoutSec int
}

// LoadConfig resolves test configuration from environment variables with safe defaults.
func LoadConfig() *Config {
	modeStr := strings.ToLower(os.Getenv("TEST_MODE"))
	mode := ModeAuto
	switch modeStr {
	case "containers":
		mode = ModeContainers
	case "live":
		mode = ModeLive
	default:
		mode = ModeAuto
	}

	baseURL := os.Getenv("TEST_BASE_URL")
	if baseURL == "" {
		port := os.Getenv("PORT")
		if port == "" {
			port = "8080"
		}
		baseURL = "http://localhost:" + port
	}

	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		dbURL = os.Getenv("DATABASE_URL")
		if dbURL == "" {
			dbURL = "postgres://shopflow:shopflow_secret@localhost:5432/shopflow?sslmode=disable"
		}
	}

	kafkaBrokersEnv := os.Getenv("TEST_KAFKA_BROKERS")
	if kafkaBrokersEnv == "" {
		kafkaBrokersEnv = os.Getenv("KAFKA_BROKERS")
	}
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

	migrationsDir := os.Getenv("TEST_MIGRATIONS_DIR")
	if migrationsDir == "" {
		// Attempt to discover migrations relative to known locations
		candidates := []string{
			"./migrations",
			"../migrations",
			"../../migrations",
			"../../../migrations",
		}
		for _, c := range candidates {
			abs, err := filepath.Abs(c)
			if err == nil {
				if fi, err := os.Stat(abs); err == nil && fi.IsDir() {
					migrationsDir = abs
					break
				}
			}
		}
		if migrationsDir == "" {
			migrationsDir = "./migrations"
		}
	}

	return &Config{
		BaseURL:              baseURL,
		DatabaseURL:          dbURL,
		KafkaBrokers:         kafkaBrokers,
		Mode:                 mode,
		MigrationsDir:        migrationsDir,
		HTTPClientTimeoutSec: 15,
	}
}
