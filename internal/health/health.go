// Package health provides a shared liveness/readiness handler for all
// dark-factory services.
package health

import (
	"encoding/json"
	"net/http"
	"sync"
)

// Checker reports whether a named dependency is currently healthy.
type Checker func() error

// Handler serves /healthz (liveness) and /readyz (readiness, runs Checkers).
type Handler struct {
	mu       sync.RWMutex
	checkers map[string]Checker
}

// NewHandler returns a Handler with no dependency checkers registered.
func NewHandler() *Handler {
	return &Handler{checkers: make(map[string]Checker)}
}

// Register adds or replaces a named dependency checker.
func (h *Handler) Register(name string, c Checker) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.checkers[name] = c
}

// Deregister removes a named dependency checker.
func (h *Handler) Deregister(name string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.checkers, name)
}

// RegisterRoutes wires the health endpoints onto mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/healthz", h.liveness)
	mux.HandleFunc("/readyz", h.readiness)
}

func (h *Handler) liveness(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) readiness(w http.ResponseWriter, _ *http.Request) {
	h.mu.RLock()
	checkers := make(map[string]Checker, len(h.checkers))
	for k, v := range h.checkers {
		checkers[k] = v
	}
	h.mu.RUnlock()

	failed := make(map[string]string)
	for name, check := range checkers {
		if err := check(); err != nil {
			failed[name] = err.Error()
		}
	}
	if len(failed) > 0 {
		writeJSON(w, http.StatusServiceUnavailable, map[string]interface{}{"status": "unavailable", "failed": failed})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func writeJSON(w http.ResponseWriter, code int, body interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}
