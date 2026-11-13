package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"
)

// ProviderLogins relays the console's provider sign-in to the provider
// broker's API (internal/providerbroker/server.go). The controller holds no
// provider credential: it proves itself to the broker with a projected
// ServiceAccount token and passes back only the provider's sign-in link,
// device code and status.
type ProviderLogins struct {
	URL       string
	TokenFile string
	HTTP      *http.Client
}

// NewProviderLogins checks PROVIDER_BROKER_URL / PROVIDER_BROKER_TOKEN_FILE.
func NewProviderLogins(brokerURL, tokenFile string) (*ProviderLogins, error) {
	u, err := url.Parse(brokerURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("PROVIDER_BROKER_URL must be an http(s) origin")
	}
	if strings.TrimSpace(tokenFile) == "" {
		return nil, errors.New("PROVIDER_BROKER_TOKEN_FILE is required with PROVIDER_BROKER_URL")
	}
	return &ProviderLogins{URL: strings.TrimSuffix(brokerURL, "/"), TokenFile: tokenFile,
		HTTP: &http.Client{Timeout: 60 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (p *ProviderLogins) call(ctx context.Context, method, path string, body any) (int, []byte, error) {
	token, err := os.ReadFile(p.TokenFile)
	if err != nil || len(bytes.TrimSpace(token)) == 0 {
		return 0, nil, errors.New("provider broker token unavailable")
	}
	var reader io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, p.URL+path, reader)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+string(bytes.TrimSpace(token)))
	req.Header.Set("Content-Type", "application/json")
	res, err := p.HTTP.Do(req)
	if err != nil {
		return 0, nil, errors.New("provider broker unreachable")
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	if err != nil {
		return 0, nil, errors.New("provider broker response unreadable")
	}
	return res.StatusCode, data, nil
}

// relay passes the broker's JSON answer through, or a fixed error.
func (s *Server) relayProviderLogin(w http.ResponseWriter, r *http.Request, method, path string, body any) {
	status, data, err := s.ProviderLogins.call(r.Context(), method, path, body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if !json.Valid(data) || status == http.StatusUnauthorized {
		http.Error(w, "provider broker refused the controller", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(data)
}

// providerLoginsStatus shows sign-in progress, including the provider's link
// and code, so it needs the same signed-in OIDC operator as starting one.
func (s *Server) providerLoginsStatus(w http.ResponseWriter, r *http.Request) {
	if _, ok := providerLoginOperator(w, r); !ok {
		return
	}
	if s.ProviderLogins == nil {
		writeJSON(w, map[string]any{"configured": false})
		return
	}
	s.relayProviderLogin(w, r, http.MethodGet, "/v1/logins", nil)
}

func (s *Server) startProviderLogin(w http.ResponseWriter, r *http.Request) {
	actor, ok := providerLoginOperator(w, r)
	if !ok {
		return
	}
	if s.ProviderLogins == nil {
		http.Error(w, "console sign-in is off on this controller", http.StatusForbidden)
		return
	}
	provider := r.PathValue("provider")
	if !slices.Contains([]string{"grok", "cursor"}, provider) {
		http.Error(w, "unsupported provider", http.StatusBadRequest)
		return
	}
	var req struct {
		ReplaceAccount bool `json:"replaceAccount"`
	}
	if r.ContentLength != 0 && decode(w, r, &req) != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	slog.InfoContext(r.Context(), "provider sign-in requested", "actor", actor, "provider", provider, "replace_account", req.ReplaceAccount)
	s.relayProviderLogin(w, r, http.MethodPost, "/v1/logins/"+provider, req)
}

func (s *Server) cancelProviderLogin(w http.ResponseWriter, r *http.Request) {
	actor, ok := providerLoginOperator(w, r)
	if !ok {
		return
	}
	if s.ProviderLogins == nil {
		http.Error(w, "console sign-in is off on this controller", http.StatusForbidden)
		return
	}
	provider := r.PathValue("provider")
	if !slices.Contains([]string{"grok", "cursor"}, provider) {
		http.Error(w, "unsupported provider", http.StatusBadRequest)
		return
	}
	slog.InfoContext(r.Context(), "provider sign-in canceled", "actor", actor, "provider", provider)
	s.relayProviderLogin(w, r, http.MethodDelete, "/v1/logins/"+provider, nil)
}

func providerLoginOperator(w http.ResponseWriter, r *http.Request) (string, bool) {
	method, _ := r.Context().Value(authKey{}).(string)
	actor, _ := r.Context().Value(actorKey{}).(string)
	if method != "oidc" || actor == "" {
		http.Error(w, "provider sign-in needs a signed-in OIDC operator; automation and static credentials cannot use it", http.StatusForbidden)
		return "", false
	}
	return actor, true
}
