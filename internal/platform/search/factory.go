package search

import (
	"context"
	"log/slog"
	"strings"

	"shopflow/internal/platform/config"
)

// NewClientFromConfig creates a search Client based on the provided configuration.
// When SearchProvider is "memory" or OpenSearchURL is empty, it returns a MemoryClient.
func NewClientFromConfig(ctx context.Context, cfg config.Config, logger *slog.Logger) (Client, error) {
	if strings.EqualFold(cfg.SearchProvider, "memory") || strings.TrimSpace(cfg.OpenSearchURL) == "" {
		if logger != nil {
			logger.Info("using in-memory search client")
		}
		return NewMemoryClient(), nil
	}

	client, err := NewOpenSearchClient(OpenSearchConfig{
		URL:      cfg.OpenSearchURL,
		Index:    cfg.OpenSearchIndex,
		Username: cfg.OpenSearchUsername,
		Password: cfg.OpenSearchPassword,
		Timeout:  cfg.OpenSearchTimeout,
	}, logger)
	if err != nil {
		return nil, err
	}

	if logger != nil {
		logger.Info("opensearch client initialized", "url", cfg.OpenSearchURL, "index", cfg.OpenSearchIndex)
	}

	return client, nil
}
