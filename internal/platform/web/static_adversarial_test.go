// internal/platform/web/static_adversarial_test.go
package web_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"shopflow/internal/platform/web"

	"github.com/go-chi/chi/v5"
)

func TestAdversarial_RouteIsolation(t *testing.T) {
	// Create dist directory
	tmpDir := t.TempDir()
	indexContent := "<!DOCTYPE html><html><body>SPA Root</body></html>"
	if err := os.WriteFile(filepath.Join(tmpDir, "index.html"), []byte(indexContent), 0644); err != nil {
		t.Fatalf("failed to write index.html: %v", err)
	}

	r := chi.NewRouter()

	// Normal handlers for legitimate routes
	r.Get("/health/live", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("HEALTH_OK"))
	})
	r.Get("/metrics", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("METRICS_OK"))
	})
	r.Get("/api/v1/orders", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"orders":[]}`))
	})

	attached := web.AttachSPAFallback(r, tmpDir, nil)
	if !attached {
		t.Fatalf("failed to attach SPA fallback")
	}

	server := httptest.NewServer(r)
	defer server.Close()

	// Reserved routes that MUST NEVER return SPA index.html
	reservedCases := []struct {
		path           string
		expectedStatus int
		expectIndex    bool
	}{
		// Existing endpoints
		{"/health/live", http.StatusOK, false},
		{"/metrics", http.StatusOK, false},
		{"/api/v1/orders", http.StatusOK, false},

		// Non-existent API endpoints (MUST 404, never 200 with index.html)
		{"/api", http.StatusNotFound, false},
		{"/api/", http.StatusNotFound, false},
		{"/api/v1/unknown", http.StatusNotFound, false},
		{"/api/v1/orders/123/items/xyz", http.StatusNotFound, false},
		{"/api/nonexistent", http.StatusNotFound, false},

		// Non-existent health endpoints (MUST 404, never 200 with index.html)
		{"/health", http.StatusNotFound, false},
		{"/health/", http.StatusNotFound, false},
		{"/health/ready2", http.StatusNotFound, false},

		// Non-existent metrics endpoints (MUST 404, never 200 with index.html)
		{"/metrics/extra", http.StatusNotFound, false},

		// Legitimate client-side routes (MUST return 200 with index.html)
		{"/", http.StatusOK, true},
		{"/catalog", http.StatusOK, true},
		{"/catalog/products/prod-123", http.StatusOK, true},
		{"/cart", http.StatusOK, true},
		{"/orders/ord-456", http.StatusOK, true},
		{"/any/nested/client/route", http.StatusOK, true},
	}

	for _, tc := range reservedCases {
		t.Run("Path_"+tc.path, func(t *testing.T) {
			resp, err := http.Get(server.URL + tc.path)
			if err != nil {
				t.Fatalf("request failed for %s: %v", tc.path, err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != tc.expectedStatus {
				t.Errorf("status mismatch for %s: expected %d, got %d", tc.path, tc.expectedStatus, resp.StatusCode)
			}

			body, _ := io.ReadAll(resp.Body)
			isIndex := strings.Contains(string(body), "SPA Root")
			if isIndex != tc.expectIndex {
				t.Errorf("SPA index leakage on %s: expectIndex=%v, got isIndex=%v, body=%q",
					tc.path, tc.expectIndex, isIndex, string(body))
			}
		})
	}
}

func TestAdversarial_PathTraversalGuards(t *testing.T) {
	tmpDir := t.TempDir()
	indexContent := "<!DOCTYPE html><html><body>SPA Root</body></html>"
	if err := os.WriteFile(filepath.Join(tmpDir, "index.html"), []byte(indexContent), 0644); err != nil {
		t.Fatalf("failed to write index.html: %v", err)
	}

	r := chi.NewRouter()
	web.AttachSPAFallback(r, tmpDir, nil)

	server := httptest.NewServer(r)
	defer server.Close()

	// Path traversal attempts
	traversals := []string{
		"/../main.go",
		"/../../go.mod",
		"/....//....//etc/passwd",
		"/%2e%2e/%2e%2e/go.mod",
	}

	for _, p := range traversals {
		t.Run("Traversal_"+p, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, server.URL+p, nil)
			if err != nil {
				t.Fatalf("failed to create request: %v", err)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			defer resp.Body.Close()

			body, _ := io.ReadAll(resp.Body)
			// Must never expose file contents outside distDir
			if strings.Contains(string(body), "module shopflow") || strings.Contains(string(body), "package main") {
				t.Fatalf("CRITICAL SECURITY VULNERABILITY: directory traversal leaked file content for %s", p)
			}
		})
	}
}

func TestAdversarial_HTTPMethods(t *testing.T) {
	tmpDir := t.TempDir()
	indexContent := "<!DOCTYPE html><html><body>SPA Root</body></html>"
	if err := os.WriteFile(filepath.Join(tmpDir, "index.html"), []byte(indexContent), 0644); err != nil {
		t.Fatalf("failed to write index.html: %v", err)
	}

	r := chi.NewRouter()
	web.AttachSPAFallback(r, tmpDir, nil)

	server := httptest.NewServer(r)
	defer server.Close()

	methods := []struct {
		method         string
		expectedStatus int
	}{
		{http.MethodGet, http.StatusOK},
		{http.MethodHead, http.StatusOK},
		{http.MethodPost, http.StatusMethodNotAllowed},
		{http.MethodPut, http.StatusMethodNotAllowed},
		{http.MethodDelete, http.StatusMethodNotAllowed},
		{http.MethodPatch, http.StatusMethodNotAllowed},
	}

	for _, m := range methods {
		t.Run("Method_"+m.method, func(t *testing.T) {
			req, err := http.NewRequest(m.method, server.URL+"/catalog", nil)
			if err != nil {
				t.Fatalf("failed to build request: %v", err)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != m.expectedStatus {
				t.Errorf("method %s on SPA route returned status %d, expected %d", m.method, resp.StatusCode, m.expectedStatus)
			}
		})
	}
}

func TestAdversarial_RealWebDistServing(t *testing.T) {
	distPath := filepath.Join("..", "..", "..", "web", "dist")
	if _, err := os.Stat(filepath.Join(distPath, "index.html")); os.IsNotExist(err) {
		t.Skip("web/dist not compiled, skipping")
	}

	r := chi.NewRouter()
	attached := web.AttachSPAFallback(r, distPath, nil)
	if !attached {
		t.Fatalf("failed to attach real web/dist")
	}

	server := httptest.NewServer(r)
	defer server.Close()

	// 1. Root serving
	resp, err := http.Get(server.URL + "/")
	if err != nil {
		t.Fatalf("failed to get root: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for root, got %d", resp.StatusCode)
	}
	contentType := resp.Header.Get("Content-Type")
	if !strings.Contains(contentType, "text/html") {
		t.Errorf("expected text/html for root, got %s", contentType)
	}

	// 2. Find asset file in dist/assets
	assetsDir := filepath.Join(distPath, "assets")
	entries, err := os.ReadDir(assetsDir)
	if err != nil {
		t.Fatalf("failed to read assets dir: %v", err)
	}

	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".js") {
			assetURL := server.URL + "/assets/" + entry.Name()
			assetResp, err := http.Get(assetURL)
			if err != nil {
				t.Fatalf("failed to get js asset %s: %v", entry.Name(), err)
			}
			defer assetResp.Body.Close()
			if assetResp.StatusCode != http.StatusOK {
				t.Errorf("expected 200 for asset %s, got %d", entry.Name(), assetResp.StatusCode)
			}
			ct := assetResp.Header.Get("Content-Type")
			if !strings.Contains(ct, "javascript") {
				t.Errorf("expected javascript content-type for %s, got %s", entry.Name(), ct)
			}
		} else if strings.HasSuffix(entry.Name(), ".css") {
			assetURL := server.URL + "/assets/" + entry.Name()
			assetResp, err := http.Get(assetURL)
			if err != nil {
				t.Fatalf("failed to get css asset %s: %v", entry.Name(), err)
			}
			defer assetResp.Body.Close()
			if assetResp.StatusCode != http.StatusOK {
				t.Errorf("expected 200 for asset %s, got %d", entry.Name(), assetResp.StatusCode)
			}
			ct := assetResp.Header.Get("Content-Type")
			if !strings.Contains(ct, "text/css") {
				t.Errorf("expected text/css content-type for %s, got %s", entry.Name(), ct)
			}
		}
	}
}
