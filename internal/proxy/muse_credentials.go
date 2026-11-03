package proxy

import (
	"net/http"
	"strings"

	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/credentials"
)

// Only the subscription credential produced by official Muse device-code sign-in
// belongs here. It stays on the trusted gateway, outside worker storage.
func (s *Server) museCredentials(r *http.Request) error {
	if !s.Config.MuseSubscriptionGateway || r.URL.Hostname() != "api.meta.ai" {
		return nil
	}
	for _, segment := range strings.Split(r.URL.Path, "/") {
		if segment == "." || segment == ".." {
			return credentials.ErrPermissionDenied
		}
	}
	allowed := r.Method == "GET" && (r.URL.Path == "/muse-code/models" || r.URL.Path == "/muse-code/config/v1" || r.URL.Path == "/v1/models" || strings.HasPrefix(r.URL.Path, "/v1/models/")) ||
		r.Method == "POST" && (r.URL.Path == "/v1/responses" || r.URL.Path == "/v1/chat/completions")
	if !allowed || r.URL.RawQuery != "" {
		return credentials.ErrPermissionDenied
	}
	key, err := s.credential(r.Context(), MuseSubscriptionKeyURI)
	if err != nil {
		return err
	}
	r.Header.Del("Cookie")
	r.Header.Set("Authorization", "Bearer "+string(key))
	return nil
}
