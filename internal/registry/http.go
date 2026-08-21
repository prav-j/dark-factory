package registry

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// NewHTTPHandler returns the registry's REST surface. Authentication is not
// applied here — issue #8 wraps this with OIDC middleware; until then
// identity arrives via X-Org-ID / X-User-ID headers (dev only).
func NewHTTPHandler(store *Store) http.Handler {
	mux := http.NewServeMux()
	h := &handler{store: store}

	mux.HandleFunc("/v1/agents", h.requireIdentity(h.agents))
	mux.HandleFunc("/v1/agents/", h.requireIdentity(h.agentSubresource))
	return mux
}

type handler struct {
	store *Store
}

type identity struct {
	orgID  string
	userID string
}

func (h *handler) requireIdentity(next func(w http.ResponseWriter, r *http.Request, id identity)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := identity{
			orgID:  r.Header.Get("X-Org-ID"),
			userID: r.Header.Get("X-User-ID"),
		}
		if id.orgID == "" || id.userID == "" {
			writeErr(w, http.StatusUnauthorized, "missing identity headers")
			return
		}
		next(w, r, id)
	}
}

func (h *handler) agents(w http.ResponseWriter, r *http.Request, id identity) {
	if r.URL.Path != "/v1/agents" {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodPost:
		h.create(w, r, id)
	default:
		methodNotAllowed(w, http.MethodPost)
	}
}

// agentSubresource routes /v1/agents/{id} and /v1/agents/{id}/versions[...].
func (h *handler) agentSubresource(w http.ResponseWriter, r *http.Request, id identity) {
	parts := splitPath(strings.TrimPrefix(r.URL.Path, "/v1/agents/"))
	if len(parts) == 0 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	agentID := parts[0]

	switch {
	case len(parts) == 1:
		switch r.Method {
		case http.MethodGet:
			h.getAgent(w, r, id, agentID)
		default:
			methodNotAllowed(w, http.MethodGet)
		}
	case len(parts) == 2 && parts[1] == "versions":
		switch r.Method {
		case http.MethodPost:
			h.addVersion(w, r, id, agentID)
		case http.MethodGet:
			h.listVersions(w, r, id, agentID)
		default:
			methodNotAllowed(w, http.MethodPost, http.MethodGet)
		}
	case len(parts) == 3 && parts[1] == "versions":
		// Forms: GET .../versions/{n} | POST .../versions/{n}:publish | :deprecate
		numPart := parts[2]
		action := ""
		if i := strings.Index(numPart, ":"); i >= 0 {
			action = numPart[i+1:]
			numPart = numPart[:i]
		}
		v, err := strconv.Atoi(numPart)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "version must be a number")
			return
		}
		switch action {
		case "":
			switch r.Method {
			case http.MethodGet:
				h.getVersion(w, r, id, agentID, v)
			case http.MethodPut:
				h.updateDraft(w, r, id, agentID, v)
			default:
				methodNotAllowed(w, http.MethodGet, http.MethodPut)
			}
		case "publish":
			h.publish(w, r, id, agentID, v)
		case "deprecate":
			h.deprecate(w, r, id, agentID, v)
		default:
			methodNotAllowed(w, http.MethodPost, http.MethodGet)
		}
	default:
		http.NotFound(w, r)
	}
}

func (h *handler) create(w http.ResponseWriter, r *http.Request, id identity) {
	body := struct {
		Name     string `json:"name"`
		SpecYAML string `json:"specYaml"`
	}{}
	if !decodeBody(w, r, &body) {
		return
	}
	agent, version, err := h.store.CreateAgent(r.Context(), id.orgID, id.userID, body.Name, []byte(body.SpecYAML))
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"agent": agent, "version": version})
}

func (h *handler) addVersion(w http.ResponseWriter, r *http.Request, id identity, agentID string) {
	body := struct {
		SpecYAML string `json:"specYaml"`
	}{}
	if !decodeBody(w, r, &body) {
		return
	}
	version, err := h.store.AddVersion(r.Context(), id.orgID, agentID, []byte(body.SpecYAML))
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"version": version})
}

func (h *handler) updateDraft(w http.ResponseWriter, r *http.Request, id identity, agentID string, version int) {
	body := struct {
		SpecYAML string `json:"specYaml"`
	}{}
	if !decodeBody(w, r, &body) {
		return
	}
	v, err := h.store.UpdateDraft(r.Context(), id.orgID, agentID, version, []byte(body.SpecYAML))
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"version": v})
}

func (h *handler) getAgent(w http.ResponseWriter, r *http.Request, id identity, agentID string) {
	agent, err := h.store.GetAgent(r.Context(), id.orgID, agentID)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, agent)
}

func (h *handler) listVersions(w http.ResponseWriter, r *http.Request, id identity, agentID string) {
	versions, err := h.store.ListVersions(r.Context(), id.orgID, agentID)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"versions": versions})
}

func (h *handler) getVersion(w http.ResponseWriter, r *http.Request, id identity, agentID string, version int) {
	v, err := h.store.GetVersion(r.Context(), id.orgID, agentID, version)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (h *handler) publish(w http.ResponseWriter, r *http.Request, id identity, agentID string, version int) {
	v, err := h.store.PublishVersion(r.Context(), id.orgID, agentID, version)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"version": v})
}

func (h *handler) deprecate(w http.ResponseWriter, r *http.Request, id identity, agentID string, version int) {
	v, err := h.store.DeprecateVersion(r.Context(), id.orgID, agentID, version)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"version": v})
}

// --- helpers ---

func splitPath(p string) []string {
	var out []string
	for _, s := range strings.Split(p, "/") {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func decodeBody(w http.ResponseWriter, r *http.Request, into any) bool {
	defer func() { _ = r.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "read body")
		return false
	}
	if err := json.Unmarshal(raw, into); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	return true
}

func writeStoreErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		writeErr(w, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrImmutable):
		writeErr(w, http.StatusConflict, err.Error())
	case errors.Is(err, ErrInvalidState):
		writeErr(w, http.StatusUnprocessableEntity, err.Error())
	case errors.Is(err, ErrDuplicateName):
		writeErr(w, http.StatusConflict, err.Error())
	default:
		writeErr(w, http.StatusInternalServerError, "internal error")
	}
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func methodNotAllowed(w http.ResponseWriter, allowed ...string) {
	w.Header().Set("Allow", strings.Join(allowed, ", "))
	writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
}
