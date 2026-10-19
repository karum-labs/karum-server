package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/karum-labs/karum-server/internal/config"
)

func TestHealthReturnsOK(t *testing.T) {
	cfg := &config.Config{
		Mode:    config.ModeFixture,
		Version: "test",
	}
	router := NewRouter(cfg)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /health status = %d, want %d", w.Code, http.StatusOK)
	}

	var got HealthResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}

	if got.Service != "gateway" {
		t.Errorf("Service = %q, want gateway", got.Service)
	}
	if got.Status != "ok" {
		t.Errorf("Status = %q, want ok", got.Status)
	}
	if got.Mode != config.ModeFixture {
		t.Errorf("Mode = %q, want %q", got.Mode, config.ModeFixture)
	}
	if got.Version != "test" {
		t.Errorf("Version = %q, want test", got.Version)
	}
}

func TestUnknownPathReturnsJSON404(t *testing.T) {
	router := NewRouter(&config.Config{Mode: config.ModeFixture})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/does-not-exist", nil)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusNotFound)
	}

	// The 404 must be JSON, not Gin's default HTML error page.
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("404 body is not JSON: %v", err)
	}

	if body["error"] != "not_found" {
		t.Errorf("error = %v, want not_found", body["error"])
	}
}

func TestCORSAllowsConfiguredOrigin(t *testing.T) {
	router := NewRouter(&config.Config{
		Mode:          config.ModeFixture,
		ClientOrigins: []string{"https://karum.example"},
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Header.Set("Origin", "https://karum.example")
	router.ServeHTTP(w, req)

	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "https://karum.example" {
		t.Errorf("Access-Control-Allow-Origin = %q, want the configured origin", got)
	}
}

func TestCORSRejectsUnknownOrigin(t *testing.T) {
	router := NewRouter(&config.Config{
		Mode:          config.ModeFixture,
		ClientOrigins: []string{"https://karum.example"},
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Header.Set("Origin", "https://evil.example")
	router.ServeHTTP(w, req)

	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Access-Control-Allow-Origin = %q, want empty for an unknown origin", got)
	}
}

func TestCORSAllowsLocalDevOriginWhenUnconfigured(t *testing.T) {
	router := NewRouter(&config.Config{Mode: config.ModeFixture})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Header.Set("Origin", "http://localhost:46253")
	router.ServeHTTP(w, req)

	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:46253" {
		t.Errorf("Access-Control-Allow-Origin = %q, want the local dev origin", got)
	}
}

func TestCORSPreflightReturnsNoContent(t *testing.T) {
	router := NewRouter(&config.Config{Mode: config.ModeFixture})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodOptions, "/api/v1/scout", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "Idempotency-Key, Content-Type")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("preflight status = %d, want %d", w.Code, http.StatusNoContent)
	}
	if got := w.Header().Get("Access-Control-Allow-Headers"); got != "Idempotency-Key, Content-Type" {
		t.Errorf("Access-Control-Allow-Headers = %q, want the requested headers", got)
	}
}

func TestAPIV1RootReturnsOK(t *testing.T) {
	router := NewRouter(&config.Config{Mode: config.ModeFixture})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/", nil)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/ status = %d, want %d", w.Code, http.StatusOK)
	}
}
