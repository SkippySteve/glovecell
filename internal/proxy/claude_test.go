package proxy

import (
	"encoding/json"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	"net/http/httptest"
	"testing"
	"time"
)

func claudeServer(t *testing.T, access map[string]any) *Server {
	t.Helper()
	grants := map[string]any{"claude": map[string]any{"enabled": true, "access": access}}
	return &Server{Config: Config{BrokeredProviders: true}, ProviderGrants: func() ([]byte, error) { return json.Marshal(grants) }}
}

// The worker's stand-in is swapped for the subscription token only on the
// reviewed routes, and the request is marked as an OAuth call.
func TestClaudeCellTokenSwapOnReviewedRoutes(t *testing.T) {
	s := claudeServer(t, map[string]any{"accessToken": "sk-ant-oat01-real", "cellToken": "sk-ant-oat01-apc-cell-one", "expiresAt": time.Now().Add(time.Hour).Unix()})
	r := httptest.NewRequest("POST", "https://api.anthropic.com/v1/messages?beta=true", nil)
	r.Header.Set("Authorization", "Bearer sk-ant-oat01-apc-cell-one")
	r.Header.Set("Anthropic-Beta", "claude-code-20250219")
	r.Header.Set("Cookie", "x=y")
	if err := s.providerAuthorization(r); err != nil {
		t.Fatal(err)
	}
	if r.Header.Get("Authorization") != "Bearer sk-ant-oat01-real" || r.Header.Get("Anthropic-Beta") != "claude-code-20250219,oauth-2025-04-20" || r.Header.Get("Cookie") != "" {
		t.Fatalf("headers = %v", r.Header)
	}
	for _, tc := range []struct{ method, path, token, apiKey string }{
		{"POST", "/v1/messages", "sk-ant-oat01-real", ""},                         // real token from inside
		{"POST", "/v1/messages", "sk-ant-oat01-apc-cell-two", ""},                 // another cell
		{"POST", "/v1/messages/batches", "sk-ant-oat01-apc-cell-one", ""},         // unreviewed route
		{"GET", "/v1/messages", "sk-ant-oat01-apc-cell-one", ""},                  // wrong method
		{"POST", "/v1/messages?beta=false", "sk-ant-oat01-apc-cell-one", ""},      // unreviewed query
		{"POST", "/v1/messages", "sk-ant-oat01-apc-cell-one", "sk-ant-api03-key"}, // API key
		{"POST", "/v1/files", "sk-ant-oat01-apc-cell-one", ""},
	} {
		r := httptest.NewRequest(tc.method, "https://api.anthropic.com"+tc.path, nil)
		r.Header.Set("Authorization", "Bearer "+tc.token)
		if tc.apiKey != "" {
			r.Header.Set("X-Api-Key", tc.apiKey)
		}
		if s.providerAuthorization(r) == nil {
			t.Errorf("accepted %+v", tc)
		}
	}
}

// A Claude grant without a cell token (the real token in the worker) is
// refused outright, and so is an expired one.
func TestClaudeNeedsCellToken(t *testing.T) {
	s := claudeServer(t, map[string]any{"accessToken": "sk-ant-oat01-real", "expiresAt": time.Now().Add(time.Hour).Unix()})
	r := httptest.NewRequest("POST", "https://api.anthropic.com/v1/messages", nil)
	r.Header.Set("Authorization", "Bearer sk-ant-oat01-real")
	if s.providerAuthorization(r) == nil {
		t.Fatal("Claude grant without a cell token accepted")
	}
	s = claudeServer(t, map[string]any{"accessToken": "sk-ant-oat01-real", "cellToken": "c", "expiresAt": time.Now().Add(30 * time.Second).Unix()})
	r = httptest.NewRequest("POST", "https://api.anthropic.com/v1/messages", nil)
	r.Header.Set("Authorization", "Bearer c")
	if s.providerAuthorization(r) == nil {
		t.Fatal("expiring Claude grant accepted")
	}
}

// Codex can use a cell token like Grok and Cursor.
func TestCodexCellTokenSwap(t *testing.T) {
	grants := `{"codex":{"enabled":true,"access":{"accessToken":"h.c.real","cellToken":"h.c.cell","chatgptAccountId":"acct","expiresAt":` + jsonNumber(time.Now().Add(time.Hour).Unix()) + `}}}`
	s := &Server{Config: Config{BrokeredProviders: true}, ProviderGrants: func() ([]byte, error) { return []byte(grants), nil }}
	r := httptest.NewRequest("POST", "https://chatgpt.com/backend-api/codex/responses", nil)
	r.Header.Set("Authorization", "Bearer h.c.cell")
	if err := s.providerAuthorization(r); err != nil || r.Header.Get("Authorization") != "Bearer h.c.real" {
		t.Fatal(err, r.Header.Get("Authorization"))
	}
}

func TestBrokeredRoutesForCodexAndClaude(t *testing.T) {
	for _, tc := range []struct {
		host, method, path string
		ok                 bool
	}{
		{"chatgpt.com", "POST", "/backend-api/codex/responses", true},
		{"chatgpt.com", "GET", "/backend-api/wham/accounts/check", true},
		{"chatgpt.com", "GET", "/backend-api/conversations", false},
		{"chatgpt.com", "GET", "/", false},
		{"api.anthropic.com", "POST", "/v1/messages", true},
		{"api.anthropic.com", "GET", "/api/oauth/profile", true},
		{"api.anthropic.com", "POST", "/v1/complete", false},
	} {
		rule := brokeredRoutes[tc.host]
		if got := Allowed(Config{Rules: []api.HTTPSRule{rule}}, tc.host, tc.method, tc.path); got != tc.ok {
			t.Errorf("%+v: %v", tc, got)
		}
	}
}
