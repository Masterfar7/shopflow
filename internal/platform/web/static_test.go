// internal/platform/web/static_test.go
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

func TestAttachSPAFallback_MissingDir(t *testing.T) {
	r := chi.NewRouter()
	attached := web.AttachSPAFallback(r, "nonexistent-dir-12345", nil)
	if attached {
		t.Errorf("expected AttachSPAFallback to return false for nonexistent dir, got true")
	}
}

func TestSPAHandler_ServingAndFallbacks(t *testing.T) {
	// Create temporary dist directory with index.html and assets
	tmpDir := t.TempDir()
	indexContent := "<!DOCTYPE html><html><head><title>ShopFlow</title></head><body><div id='root'></div></body></html>"
	if err := os.WriteFile(filepath.Join(tmpDir, "index.html"), []byte(indexContent), 0644); err != nil {
		t.Fatalf("failed to write index.html: %v", err)
	}

	assetsDir := filepath.Join(tmpDir, "assets")
	if err := os.MkdirAll(assetsDir, 0755); err != nil {
		t.Fatalf("failed to create assets dir: %v", err)
	}
	jsContent := "console.log('shopflow app');"
	if err := os.WriteFile(filepath.Join(assetsDir, "app.js"), []byte(jsContent), 0644); err != nil {
		t.Fatalf("failed to write app.js: %v", err)
	}

	r := chi.NewRouter()

	// Register some mock API and health routes
	r.Get("/health/live", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("live"))
	})
	r.Get("/metrics", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("metrics"))
	})
	r.Route("/api/v1", func(api chi.Router) {
		api.Get("/items", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[{"id":"1"}]`))
		})
	})

	attached := web.AttachSPAFallback(r, tmpDir, nil)
	if !attached {
		t.Fatalf("expected AttachSPAFallback to return true for valid dist dir, got false")
	}

	server := httptest.NewServer(r)
	defer server.Close()

	tests := []struct {
		name           string
		method         string
		path           string
		expectedStatus int
		expectedBody   string
		checkExactBody bool
	}{
		{
			name:           "Root serves index.html",
			method:         http.MethodGet,
			path:           "/",
			expectedStatus: http.StatusOK,
			expectedBody:   indexContent,
			checkExactBody: true,
		},
		{
			name:           "Direct asset file serves asset",
			method:         http.MethodGet,
			path:           "/assets/app.js",
			expectedStatus: http.StatusOK,
			expectedBody:   jsContent,
			checkExactBody: true,
		},
		{
			name:           "Client-side SPA route falls back to index.html",
			method:         http.MethodGet,
			path:           "/catalog/products/123",
			expectedStatus: http.StatusOK,
			expectedBody:   indexContent,
			checkExactBody: true,
		},
		{
			name:           "Cart client route falls back to index.html",
			method:         http.MethodGet,
			path:           "/cart",
			expectedStatus: http.StatusOK,
			expectedBody:   indexContent,
			checkExactBody: true,
		},
		{
			name:           "Existing health route is handled normally",
			method:         http.MethodGet,
			path:           "/health/live",
			expectedStatus: http.StatusOK,
			expectedBody:   "live",
			checkExactBody: true,
		},
		{
			name:           "Existing metrics route is handled normally",
			method:         http.MethodGet,
			path:           "/metrics",
			expectedStatus: http.StatusOK,
			expectedBody:   "metrics",
			checkExactBody: true,
		},
		{
			name:           "Existing API route is handled normally",
			method:         http.MethodGet,
			path:           "/api/v1/items",
			expectedStatus: http.StatusOK,
			expectedBody:   `[{"id":"1"}]`,
			checkExactBody: true,
		},
		{
			name:           "Unknown API route is NOT intercepted by SPA (returns 404)",
			method:         http.MethodGet,
			path:           "/api/v1/nonexistent",
			expectedStatus: http.StatusNotFound,
		},
		{
			name:           "Unknown health subroute is NOT intercepted by SPA (returns 404)",
			method:         http.MethodGet,
			path:           "/health/unknown",
			expectedStatus: http.StatusNotFound,
		},
		{
			name:           "Non-GET method on SPA route is rejected with MethodNotAllowed",
			method:         http.MethodPost,
			path:           "/catalog",
			expectedStatus: http.StatusMethodNotAllowed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequest(tt.method, server.URL+tt.path, nil)
			if err != nil {
				t.Fatalf("failed to create request: %v", err)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != tt.expectedStatus {
				t.Errorf("status mismatch for %s %s: expected %d, got %d", tt.method, tt.path, tt.expectedStatus, resp.StatusCode)
			}

			if tt.checkExactBody {
				bodyBytes, _ := io.ReadAll(resp.Body)
				if strings.TrimSpace(string(bodyBytes)) != strings.TrimSpace(tt.expectedBody) {
					t.Errorf("body mismatch: expected %q, got %q", tt.expectedBody, string(bodyBytes))
				}
			}
		})
	}
}

func TestAttachSPAFallback_WebDistIntegration(t *testing.T) {
	distPath := filepath.Join("..", "..", "..", "web", "dist")
	if _, err := os.Stat(filepath.Join(distPath, "index.html")); os.IsNotExist(err) {
		t.Skip("web/dist/index.html not found, skipping integration check")
	}

	r := chi.NewRouter()
	r.Route("/api/v1", func(api chi.Router) {
		api.Get("/test", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
	})

	attached := web.AttachSPAFallback(r, distPath, nil)
	if !attached {
		t.Fatalf("expected AttachSPAFallback to succeed for actual web/dist")
	}

	server := httptest.NewServer(r)
	defer server.Close()

	resp, err := http.Get(server.URL + "/")
	if err != nil {
		t.Fatalf("failed to GET /: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "ShopFlow") {
		t.Errorf("expected body to contain 'ShopFlow', got %s", string(body))
	}
}

