package proxy

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"
)

func TestProviderGatewayExpiryRevocationRotationAndRecovery(t *testing.T) {
	current := map[string]any{"grok": map[string]any{"enabled": true, "access": map[string]any{"accessToken": "old-access", "expiresAt": time.Now().Add(time.Hour).Unix(), "issuer": "https://auth.x.ai"}}}
	s := &Server{Config: Config{BrokeredProviders: true}, ProviderGrants: func() ([]byte, error) { return json.Marshal(current) }}
	request := func(token string) error {
		r := httptest.NewRequest("POST", "https://cli-chat-proxy.grok.com/v1/responses", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		return s.providerAuthorization(r)
	}
	if err := request("old-access"); err != nil {
		t.Fatal(err)
	}
	current["grok"].(map[string]any)["enabled"] = false
	if request("old-access") == nil {
		t.Fatal("revoked grant remained usable through raw HTTPS")
	}
	current["grok"].(map[string]any)["enabled"] = true
	access := current["grok"].(map[string]any)["access"].(map[string]any)
	access["expiresAt"] = 1
	if request("old-access") == nil {
		t.Fatal("expired grant remained usable through raw HTTPS")
	}
	access["expiresAt"] = time.Now().Add(time.Hour).Unix()
	access["accessToken"] = "new-access"
	if request("old-access") == nil || request("new-access") != nil {
		t.Fatal("rotation did not revoke old access and consume replacement")
	}
	access["refreshToken"] = "never-deliver"
	if request("new-access") == nil {
		t.Fatal("accepted unreviewed credential container")
	}
	delete(access, "refreshToken")
	restarted := &Server{Config: s.Config, ProviderGrants: s.ProviderGrants}
	if err := restarted.providerAuthorization(httptest.NewRequest("GET", "https://other.example/", nil)); err != nil {
		t.Fatal("affected another destination")
	}
	if err := request("new-access"); err != nil {
		t.Fatal("interrupted operation did not recover", err)
	}
	s.ProviderGrants = nil
	if request("new-access") == nil {
		t.Fatal("broker outage did not fail closed")
	}
}

func TestIndependentCodexMissingGrantCannotUseLegacyFallback(t *testing.T) {
	s := &Server{Config: Config{BrokeredProviders: true, IndependentCodex: true}, ProviderGrants: func() ([]byte, error) { return []byte(`{"cursor":{"enabled":false}}`), nil }}
	if s.providerAuthorization(httptest.NewRequest("POST", "https://chatgpt.com/backend-api/codex/responses", nil)) == nil {
		t.Fatal("missing Codex grant bypassed denial")
	}
}

func TestCursorWorkerCannotRefreshItsAccessJWTWithBroadHTTPSGrant(t *testing.T) {
	s := &Server{Config: Config{BrokeredProviders: true}, ProviderGrants: func() ([]byte, error) {
		return []byte(`{"cursor":{"enabled":true,"access":{"accessToken":"access","expiresAt":9999999999}}}`), nil
	}}
	for _, path := range []string{"/oauth/token", "/oauth/token/", "/auth/poll", "/auth/exchange_user_api_key"} {
		req := httptest.NewRequest("POST", "https://api2.cursor.sh"+path, nil)
		req.Header.Set("Authorization", "Bearer access")
		if s.providerAuthorization(req) == nil {
			t.Fatal("worker acquired owner renewal authority", path)
		}
	}
	req := httptest.NewRequest("POST", "https://api2.cursor.sh/aiserver.v1.DashboardService/GetMe", nil)
	req.Header.Set("Authorization", "Bearer access")
	if s.providerAuthorization(req) != nil {
		t.Fatal("denied inference-side access")
	}
}
