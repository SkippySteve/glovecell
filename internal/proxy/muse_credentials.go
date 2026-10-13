package proxy

import (
	"errors"
	"net/http"
	"os"
	"strings"
)

// Only the subscription credential produced by official Muse device-code sign-in
// belongs here. It stays on the trusted gateway, outside worker storage.
func (s *Server) museCredentials(r *http.Request) error {
	if !s.Config.MuseSubscriptionGateway || r.URL.Hostname() != "api.meta.ai" {
		return nil
	}
	for _, segment := range strings.Split(r.URL.Path, "/") {
		if segment == "." || segment == ".." {
			return errors.New("Muse subscription authorization unavailable")
		}
	}
	allowed := r.Method == "GET" && (r.URL.Path == "/v1/models" || strings.HasPrefix(r.URL.Path, "/v1/models/")) ||
		r.Method == "POST" && (r.URL.Path == "/v1/responses" || r.URL.Path == "/v1/chat/completions")
	if !allowed || r.URL.RawQuery != "" || s.MuseSubscriptionKey == nil {
		return errors.New("Muse subscription authorization unavailable")
	}
	key, err := s.MuseSubscriptionKey()
	if err != nil || key == "" || len(key) > 32768 {
		return errors.New("Muse subscription authorization unavailable")
	}
	for _, c := range key {
		if c < 33 || c > 126 {
			return errors.New("Muse subscription authorization unavailable")
		}
	}
	r.Header.Del("Cookie")
	r.Header.Set("Authorization", "Bearer "+key)
	return nil
}

func gatewayMuseSubscriptionKey() (string, error) {
	raw, err := os.ReadFile("/tls/muse-subscription-key")
	return strings.TrimSpace(string(raw)), err
}
