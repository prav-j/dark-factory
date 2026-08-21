package credexchange

import (
	"encoding/json"
	"errors"
	"net/http"
)

// Handler serves POST /credentials/exchange. It is mounted WITHOUT the OIDC
// user-auth middleware: callers authenticate with a Run Token carried in the
// body, which is validated here.
func Handler(svc *Service) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var req struct {
			RunToken      string `json:"runToken"`
			CredentialRef string `json:"credentialRef"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.RunToken == "" || req.CredentialRef == "" {
			writeErr(w, http.StatusBadRequest, "runToken and credentialRef required")
			return
		}

		ex, err := svc.Exchange(r.Context(), req.RunToken, req.CredentialRef)
		if err != nil {
			switch {
			case errors.Is(err, ErrNotFound):
				writeErr(w, http.StatusNotFound, err.Error())
			case errors.Is(err, ErrUnauthorized):
				writeErr(w, http.StatusForbidden, err.Error())
			default:
				writeErr(w, http.StatusInternalServerError, "exchange failed")
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ex)
	})
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
