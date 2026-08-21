package health

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLivenessAlwaysOK(t *testing.T) {
	h := NewHandler()
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("liveness: got status %d, want 200", rec.Code)
	}
}

func TestReadinessReflectsCheckers(t *testing.T) {
	h := NewHandler()
	h.Register("db", func() error { return nil })
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("ready with healthy deps: got %d, want 200", rec.Code)
	}

	h.Deregister("db")
	h.Register("db", func() error { return errors.New("connection refused") })

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("ready with unhealthy dep: got %d, want 503", rec.Code)
	}
}
