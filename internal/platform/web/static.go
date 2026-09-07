// internal/platform/web/static.go
package web

import (
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"
)

// AttachSPAFallback checks if distDir exists and contains index.html.
// If present, it attaches a handler for non-API, non-health, non-metrics GET/HEAD routes
// to serve static files from distDir or fall back to index.html for client-side routing.
// Returns true if the SPA handler was attached, false otherwise.
func AttachSPAFallback(r chi.Router, distDir string, logger *slog.Logger) bool {
	if logger == nil {
		logger = slog.Default()
	}

	cleanDist := filepath.Clean(distDir)
	indexFile := filepath.Join(cleanDist, "index.html")

	info, err := os.Stat(indexFile)
	if err != nil || info.IsDir() {
		logger.Debug("static web dist directory not found or missing index.html, skipping SPA fallback", "distDir", cleanDist)
		return false
	}

	handler := NewSPAHandler(cleanDist, logger)
	r.Get("/*", handler.ServeHTTP)
	r.Head("/*", handler.ServeHTTP)

	logger.Info("static web SPA fallback enabled", "distDir", cleanDist)
	return true
}

// SPAHandler serves files from distDir with fallback to index.html for SPA routes.
type SPAHandler struct {
	distDir   string
	indexFile string
	logger    *slog.Logger
}

// NewSPAHandler creates a new SPAHandler.
func NewSPAHandler(distDir string, logger *slog.Logger) *SPAHandler {
	if logger == nil {
		logger = slog.Default()
	}
	cleanDist := filepath.Clean(distDir)
	return &SPAHandler{
		distDir:   cleanDist,
		indexFile: filepath.Join(cleanDist, "index.html"),
		logger:    logger,
	}
}

// ServeHTTP serves the static asset from distDir, or falls back to index.html for SPA routes.
// It explicitly rejects API, health, and metrics endpoints to prevent masking 404s.
func (h *SPAHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	path := r.URL.Path

	// CRITICAL: Do NOT intercept /api/, /health/, or /metrics
	if isReservedPrefix(path) {
		http.NotFound(w, r)
		return
	}

	// Clean path and normalize
	cleanPath := strings.TrimPrefix(filepath.Clean(path), string(filepath.Separator))
	cleanPath = strings.TrimPrefix(cleanPath, "/")
	cleanPath = strings.TrimPrefix(cleanPath, "\\")
	targetPath := filepath.Join(h.distDir, cleanPath)

	// Directory traversal guard
	rel, err := filepath.Rel(h.distDir, targetPath)
	if err != nil || strings.HasPrefix(rel, "..") {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	// Check if physical file exists and is not a directory
	stat, err := os.Stat(targetPath)
	if err == nil && !stat.IsDir() {
		http.ServeFile(w, r, targetPath)
		return
	}

	// Fallback to index.html for client-side routing
	http.ServeFile(w, r, h.indexFile)
}

func isReservedPrefix(path string) bool {
	if strings.HasPrefix(path, "/api/") || path == "/api" {
		return true
	}
	if strings.HasPrefix(path, "/health/") || path == "/health" {
		return true
	}
	if strings.HasPrefix(path, "/metrics") || path == "/metrics" {
		return true
	}
	return false
}
