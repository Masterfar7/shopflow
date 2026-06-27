package config_test

import (
	"log/slog"
	"os"
	"testing"

	"shopflow/internal/platform/config"
)

func TestLoad_Defaults(t *testing.T) {
	// Ensure clean env
	_ = os.Unsetenv("PORT")
	_ = os.Unsetenv("DATABASE_URL")
	_ = os.Unsetenv("KAFKA_BROKERS")
	_ = os.Unsetenv("REDIS_ADDR")
	_ = os.Unsetenv("LOG_LEVEL")

	cfg := config.Load()

	if cfg.HTTPPort != "8080" {
		t.Errorf("expected default HTTPPort '8080', got '%s'", cfg.HTTPPort)
	}
	expectedDB := "postgres://shopflow:shopflow_secret@localhost:5432/shopflow?sslmode=disable"
	if cfg.DatabaseURL != expectedDB {
		t.Errorf("expected default DatabaseURL '%s', got '%s'", expectedDB, cfg.DatabaseURL)
	}
	if len(cfg.KafkaBrokers) != 1 || cfg.KafkaBrokers[0] != "localhost:9094" {
		t.Errorf("expected default KafkaBrokers ['localhost:9094'], got %v", cfg.KafkaBrokers)
	}
	if cfg.RedisAddr != "localhost:6379" {
		t.Errorf("expected default RedisAddr 'localhost:6379', got '%s'", cfg.RedisAddr)
	}
	if cfg.LogLevel != slog.LevelInfo {
		t.Errorf("expected default LogLevel LevelInfo, got %v", cfg.LogLevel)
	}
}

func TestLoad_CustomEnvironment(t *testing.T) {
	t.Setenv("PORT", "9000")
	t.Setenv("DATABASE_URL", "postgres://custom:pass@custom-host:5432/custom_db")
	t.Setenv("KAFKA_BROKERS", "broker1:9092, broker2:9092, ")
	t.Setenv("REDIS_ADDR", "custom-redis:6379")
	t.Setenv("LOG_LEVEL", "DEBUG")

	cfg := config.Load()

	if cfg.HTTPPort != "9000" {
		t.Errorf("expected HTTPPort '9000', got '%s'", cfg.HTTPPort)
	}
	if cfg.DatabaseURL != "postgres://custom:pass@custom-host:5432/custom_db" {
		t.Errorf("expected custom DatabaseURL, got '%s'", cfg.DatabaseURL)
	}
	if len(cfg.KafkaBrokers) != 2 || cfg.KafkaBrokers[0] != "broker1:9092" || cfg.KafkaBrokers[1] != "broker2:9092" {
		t.Errorf("expected parsed trimmed KafkaBrokers ['broker1:9092', 'broker2:9092'], got %v", cfg.KafkaBrokers)
	}
	if cfg.RedisAddr != "custom-redis:6379" {
		t.Errorf("expected RedisAddr 'custom-redis:6379', got '%s'", cfg.RedisAddr)
	}
	if cfg.LogLevel != slog.LevelDebug {
		t.Errorf("expected LogLevel LevelDebug, got %v", cfg.LogLevel)
	}
}

func TestLoad_LogLevelParsing(t *testing.T) {
	cases := []struct {
		envVal   string
		expected slog.Level
	}{
		{"warn", slog.LevelWarn},
		{"WARNING", slog.LevelWarn},
		{"error", slog.LevelError},
		{"ERROR", slog.LevelError},
		{"unknown", slog.LevelInfo},
	}

	for _, tc := range cases {
		t.Run(tc.envVal, func(t *testing.T) {
			t.Setenv("LOG_LEVEL", tc.envVal)
			cfg := config.Load()
			if cfg.LogLevel != tc.expected {
				t.Errorf("for env LOG_LEVEL=%s, expected %v, got %v", tc.envVal, tc.expected, cfg.LogLevel)
			}
		})
	}
}

func TestLoad_EmailDefaults(t *testing.T) {
	_ = os.Unsetenv("EMAIL_PROVIDER")
	_ = os.Unsetenv("SMTP_HOST")
	_ = os.Unsetenv("SMTP_PORT")
	_ = os.Unsetenv("SMTP_USERNAME")
	_ = os.Unsetenv("SMTP_PASSWORD")
	_ = os.Unsetenv("EMAIL_FROM")
	_ = os.Unsetenv("SMTP_FROM")

	cfg := config.Load()

	if cfg.EmailProvider != "smtp" {
		t.Errorf("expected default EmailProvider 'smtp', got '%s'", cfg.EmailProvider)
	}
	if cfg.SMTPHost != "localhost" {
		t.Errorf("expected default SMTPHost 'localhost', got '%s'", cfg.SMTPHost)
	}
	if cfg.SMTPPort != 1025 {
		t.Errorf("expected default SMTPPort 1025, got %d", cfg.SMTPPort)
	}
	if cfg.EmailFrom != "no-reply@shopflow.io" {
		t.Errorf("expected default EmailFrom 'no-reply@shopflow.io', got '%s'", cfg.EmailFrom)
	}
}

func TestLoad_EmailCustom(t *testing.T) {
	t.Setenv("EMAIL_PROVIDER", "memory")
	t.Setenv("SMTP_HOST", "mail.example.com")
	t.Setenv("SMTP_PORT", "2525")
	t.Setenv("SMTP_USERNAME", "mailer")
	t.Setenv("SMTP_PASSWORD", "secret")
	t.Setenv("EMAIL_FROM", "orders@shopflow.io")

	cfg := config.Load()

	if cfg.EmailProvider != "memory" {
		t.Errorf("expected EmailProvider 'memory', got '%s'", cfg.EmailProvider)
	}
	if cfg.SMTPHost != "mail.example.com" {
		t.Errorf("expected SMTPHost 'mail.example.com', got '%s'", cfg.SMTPHost)
	}
	if cfg.SMTPPort != 2525 {
		t.Errorf("expected SMTPPort 2525, got %d", cfg.SMTPPort)
	}
	if cfg.SMTPUsername != "mailer" {
		t.Errorf("expected SMTPUsername 'mailer', got '%s'", cfg.SMTPUsername)
	}
	if cfg.SMTPPassword != "secret" {
		t.Errorf("expected SMTPPassword 'secret', got '%s'", cfg.SMTPPassword)
	}
	if cfg.EmailFrom != "orders@shopflow.io" {
		t.Errorf("expected EmailFrom 'orders@shopflow.io', got '%s'", cfg.EmailFrom)
	}
}
