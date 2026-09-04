package email

import (
	"log/slog"
	"strings"

	"shopflow/internal/platform/config"
)

// NewSenderFromConfig creates an email Sender based on the provided configuration.
// If EmailProvider is "memory" or SMTPHost is empty, it returns a MemorySender.
func NewSenderFromConfig(cfg config.Config, logger *slog.Logger) (Sender, error) {
	if strings.EqualFold(cfg.EmailProvider, "memory") || strings.TrimSpace(cfg.SMTPHost) == "" {
		if logger != nil {
			logger.Info("using in-memory email sender")
		}
		return NewMemorySender(), nil
	}

	if logger != nil {
		logger.Info("smtp email sender initialized", "host", cfg.SMTPHost, "port", cfg.SMTPPort, "from", cfg.EmailFrom)
	}

	return NewSMTPSender(SMTPConfig{
		Host:     cfg.SMTPHost,
		Port:     cfg.SMTPPort,
		Username: cfg.SMTPUsername,
		Password: cfg.SMTPPassword,
		From:     cfg.EmailFrom,
	}, logger), nil
}
