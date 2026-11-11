package proxy

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"
)

// With a cell token the worker's stand-in is the only accepted credential, the
// real token goes upstream, and revocation, expiry and rotation still apply.
func TestCellTokenIsSwappedForTheRealToken(t *testing.T) {
	current := map[string]any{"grok": map[string]any{"enabled": true, "access": map[string]any{
		"accessToken": "real-access", "cellToken": "apc-cell-one", "expiresAt": time.Now().Add(time.Hour).Unix(), "issuer": "https://auth.x.ai"}}}
	s := &Server{Config: Config{BrokeredProviders: true}, ProviderGrants: func() ([]byte, error) { return json.Marshal(current) }}
	send := func(token string) (string, error) {
		r := httptest.NewRequest("POST", "https://cli-chat-proxy.grok.com/v1/responses", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		err := s.providerAuthorization(r)
		return r.Header.Get("Authorization"), err
	}
	upstream, err := send("apc-cell-one")
	if err != nil || upstream != "Bearer real-access" {
		t.Fatalf("cell token not swapped: %q %v", upstream, err)
	}
	if _, err := send("real-access"); err == nil {
		t.Fatal("real token accepted from a cell that uses cell tokens")
	}
	if _, err := send("apc-cell-two"); err == nil {
		t.Fatal("another cell's token accepted")
	}
	if _, err := send(""); err == nil {
		t.Fatal("missing credential accepted")
	}
	access := current["grok"].(map[string]any)["access"].(map[string]any)
	access["accessToken"], access["cellToken"] = "real-next", "apc-cell-next"
	if _, err := send("apc-cell-one"); err == nil {
		t.Fatal("old cell token survived rotation")
	}
	if upstream, err := send("apc-cell-next"); err != nil || upstream != "Bearer real-next" {
		t.Fatalf("rotated cell token: %q %v", upstream, err)
	}
	current["grok"].(map[string]any)["enabled"] = false
	if _, err := send("apc-cell-next"); err == nil {
		t.Fatal("revoked grant usable with its cell token")
	}
	current["grok"].(map[string]any)["enabled"] = true
	access["expiresAt"] = 1
	if _, err := send("apc-cell-next"); err == nil {
		t.Fatal("expired grant usable with its cell token")
	}
}

func TestCellTokenOnlyForReviewedProviders(t *testing.T) {
	grants := `{"codex":{"enabled":true,"access":{"accessToken":"real","cellToken":"cell","expiresAt":` + jsonNumber(time.Now().Add(time.Hour).Unix()) + `}}}`
	s := &Server{Config: Config{BrokeredProviders: true, IndependentCodex: true}, ProviderGrants: func() ([]byte, error) { return []byte(grants), nil }}
	r := httptest.NewRequest("POST", "https://chatgpt.com/backend-api/codex/responses", nil)
	r.Header.Set("Authorization", "Bearer cell")
	if s.providerAuthorization(r) == nil {
		t.Fatal("cell token accepted for an unreviewed provider")
	}
}

func jsonNumber(n int64) string { b, _ := json.Marshal(n); return string(b) }
