package harness

import (
	"context"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// TestEnvironment coordinates database, messaging, and HTTP client dependencies for E2E tests.
type TestEnvironment struct {
	Cfg          *Config
	DB           *DBAssert
	Kafka        *KafkaClient
	Client       *Client
	IsContainer  bool
	cleanupFuncs []func()
	mu           sync.Mutex
}

// Close executes all cleanup hooks for containers and pools.
func (env *TestEnvironment) Close() {
	env.mu.Lock()
	defer env.mu.Unlock()

	for i := len(env.cleanupFuncs) - 1; i >= 0; i-- {
		env.cleanupFuncs[i]()
	}
	env.cleanupFuncs = nil

	if env.Kafka != nil {
		env.Kafka.Close()
		env.Kafka = nil
	}
	if env.DB != nil {
		env.DB.Close()
		env.DB = nil
	}
}

// SetupEnvironment initializes the test environment based on configuration and available resources.
func SetupEnvironment(t testing.TB) *TestEnvironment {
	t.Helper()
	cfg := LoadConfig()

	env := &TestEnvironment{
		Cfg:    cfg,
		Client: NewClient(cfg.BaseURL, cfg.HTTPClientTimeoutSec),
	}
	t.Cleanup(func() {
		env.Close()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// 1. If containers mode requested or auto-mode with docker available
	if cfg.Mode == ModeContainers || (cfg.Mode == ModeAuto && isDockerAvailable()) {
		if err := env.setupTestcontainers(ctx, t); err != nil {
			if cfg.Mode == ModeContainers {
				t.Fatalf("failed to initialize Testcontainers: %v", err)
			}
			t.Logf("testcontainers setup failed (%v), falling back to live/local mode", err)
		}
	}

	// 2. Setup PostgreSQL connection
	if env.DB == nil {
		dbAssert, err := NewDBAssert(ctx, env.Cfg.DatabaseURL)
		if err != nil {
			t.Logf("local PostgreSQL not reachable at %s: %v", env.Cfg.DatabaseURL, err)
		} else {
			env.DB = dbAssert
			// Apply schema migrations
			if err := env.DB.ApplyMigrations(ctx, env.Cfg.MigrationsDir); err != nil {
				t.Logf("warning: applying migrations failed: %v", err)
			}
		}
	}

	// 3. Setup Kafka connection
	if env.Kafka == nil && len(env.Cfg.KafkaBrokers) > 0 {
		kafkaClient, err := NewKafkaClient(env.Cfg.KafkaBrokers, "")
		if err != nil {
			t.Logf("local Kafka not reachable at %v: %v", env.Cfg.KafkaBrokers, err)
		} else {
			env.Kafka = kafkaClient
		}
	}

	return env
}

// RequireDB ensures the database is connected, or skips the test.
func (env *TestEnvironment) RequireDB(t testing.TB) *DBAssert {
	t.Helper()
	if env.DB == nil {
		t.Skip("Test skipped: PostgreSQL database not available (start with 'make docker-up' or configure DATABASE_URL)")
	}
	return env.DB
}

// RequireKafka ensures Kafka is connected, or skips the test.
func (env *TestEnvironment) RequireKafka(t testing.TB) *KafkaClient {
	t.Helper()
	if env.Kafka == nil {
		t.Skip("Test skipped: Kafka broker not available (start with 'make docker-up' or configure KAFKA_BROKERS)")
	}
	return env.Kafka
}

// RequireHTTPServer ensures the API gateway is responding, or skips the test.
func (env *TestEnvironment) RequireHTTPServer(t testing.TB) *Client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	status, _, err := env.Client.GetHealthLive(ctx)
	if err != nil || status != 200 {
		t.Skipf("Test skipped: ShopFlow HTTP server not reachable at %s (start server with 'make build && ./bin/shopflow')", env.Client.BaseURL)
	}
	return env.Client
}

// isDockerAvailable performs a quick probe for Docker daemon socket.
func isDockerAvailable() bool {
	// Simple connection check to docker pipe or socket
	conn, err := net.DialTimeout("tcp", "localhost:2375", 500*time.Millisecond)
	if err == nil {
		_ = conn.Close()
		return true
	}
	return false
}

// setupTestcontainers provisions real PostgreSQL 16 and Kafka KRaft containers.
func (env *TestEnvironment) setupTestcontainers(ctx context.Context, t testing.TB) error {
	// PostgreSQL 16 Container Request
	pgReq := testcontainers.ContainerRequest{
		Image:        "postgres:16-alpine",
		ExposedPorts: []string{"5432/tcp"},
		Env: map[string]string{
			"POSTGRES_DB":       "shopflow",
			"POSTGRES_USER":     "shopflow",
			"POSTGRES_PASSWORD": "shopflow_secret",
		},
		WaitingFor: wait.ForListeningPort("5432/tcp").WithStartupTimeout(60 * time.Second),
	}

	pgContainer, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: pgReq,
		Started:          true,
	})
	if err != nil {
		return fmt.Errorf("failed to start postgres container: %w", err)
	}

	env.cleanupFuncs = append(env.cleanupFuncs, func() {
		_ = pgContainer.Terminate(context.Background())
	})

	host, err := pgContainer.Host(ctx)
	if err != nil {
		return err
	}
	port, err := pgContainer.MappedPort(ctx, "5432/tcp")
	if err != nil {
		return err
	}

	containerDSN := fmt.Sprintf("postgres://shopflow:shopflow_secret@%s:%s/shopflow?sslmode=disable", host, port.Port())
	env.Cfg.DatabaseURL = containerDSN

	dbAssert, err := NewDBAssert(ctx, containerDSN)
	if err != nil {
		return fmt.Errorf("failed to connect to container postgres: %w", err)
	}
	env.DB = dbAssert
	env.IsContainer = true

	if err := env.DB.ApplyMigrations(ctx, env.Cfg.MigrationsDir); err != nil {
		return fmt.Errorf("failed to apply migrations in container postgres: %w", err)
	}

	return nil
}
