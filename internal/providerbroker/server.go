package providerbroker

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/forgeagent"
)

// Audience is the token audience the controller requests for the broker's
// sign-in API; the broker checks it with a TokenReview and accepts exactly
// one ServiceAccount (the controller's), like the forge agent.
const Audience = "agent-project-controller-provider-broker"

// Server is the broker's sign-in API for the console:
//
//	GET    /v1/logins            every provider's sign-in status
//	POST   /v1/logins/{provider} start a sign-in {"replaceAccount": bool}
//	DELETE /v1/logins/{provider} cancel it
//
// It carries no credentials in either direction: only the provider's own
// sign-in link and device code go back.
type Server struct {
	Logins   *Logins
	Reviewer forgeagent.Reviewer
	Caller   string
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/healthz" {
		w.WriteHeader(http.StatusOK)
		return
	}
	if !s.authorized(r) {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v1/logins":
		writeJSON(w, http.StatusOK, map[string]any{"logins": s.Logins.Status()})
	case strings.HasPrefix(r.URL.Path, "/v1/logins/"):
		provider := strings.TrimPrefix(r.URL.Path, "/v1/logins/")
		switch r.Method {
		case http.MethodPost:
			var req struct {
				ReplaceAccount bool `json:"replaceAccount"`
			}
			body, _ := io.ReadAll(io.LimitReader(r.Body, 4<<10))
			if len(body) > 0 && json.Unmarshal(body, &req) != nil {
				writeError(w, http.StatusBadRequest, "invalid request")
				return
			}
			status, err := s.Logins.Start(provider, req.ReplaceAccount)
			switch {
			case errors.Is(err, ErrLoginBusy):
				writeError(w, http.StatusConflict, err.Error())
			case err != nil:
				writeError(w, http.StatusBadRequest, err.Error())
			default:
				slog.InfoContext(r.Context(), "provider sign-in started", "provider", provider, "replace_account", req.ReplaceAccount)
				writeJSON(w, http.StatusAccepted, status)
			}
		case http.MethodDelete:
			s.Logins.Cancel(provider)
			writeJSON(w, http.StatusOK, map[string]any{"logins": s.Logins.Status()})
		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	default:
		writeError(w, http.StatusNotFound, "not found")
	}
}

func (s *Server) authorized(r *http.Request) bool {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || token == "" || s.Reviewer == nil || s.Caller == "" {
		return false
	}
	user, err := s.Reviewer.Review(r.Context(), token)
	return err == nil && user == s.Caller
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
