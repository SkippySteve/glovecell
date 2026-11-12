package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The console relays sign-in to the broker with its projected token, only for
// a signed-in OIDC operator, and passes back only the broker's JSON.
func TestProviderLoginsRelay(t *testing.T) {
	var seen []string
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer projected-token" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
			return
		}
		seen = append(seen, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"provider":"grok","state":"starting"}`))
			return
		}
		_, _ = w.Write([]byte(`{"logins":[{"provider":"grok","state":"waiting","url":"https://accounts.x.ai/oauth2/device?user_code=ABCD-1234","code":"ABCD-1234"}]}`))
	}))
	defer broker.Close()
	token := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(token, []byte("projected-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	e := providerFixture(t, true)
	logins, err := NewProviderLogins(broker.URL, token)
	if err != nil {
		t.Fatal(err)
	}
	e.s.ProviderLogins = logins
	if e.h, err = e.s.Handler(); err != nil {
		t.Fatal(err)
	}

	if rec := e.do("POST", "/api/v1/provider-logins/grok", `{"replaceAccount":false}`); rec.Code != http.StatusAccepted {
		t.Fatalf("start: %d %s", rec.Code, rec.Body)
	}
	rec := e.do("GET", "/api/v1/provider-logins", "")
	var status struct {
		Logins []map[string]string `json:"logins"`
	}
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &status) != nil || status.Logins[0]["code"] != "ABCD-1234" || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status: %d %s", rec.Code, rec.Body)
	}
	if rec := e.do("DELETE", "/api/v1/provider-logins/grok", ""); rec.Code != 200 {
		t.Fatalf("cancel: %d", rec.Code)
	}
	if strings.Join(seen, ",") != "POST /v1/logins/grok,GET /v1/logins,DELETE /v1/logins/grok" {
		t.Fatalf("broker calls %v", seen)
	}
	if rec := e.do("POST", "/api/v1/provider-logins/codex", ""); rec.Code != http.StatusBadRequest {
		t.Fatal("unknown provider relayed", rec.Code)
	}

	bearer := func(r *http.Request) {
		r.Header.Del("Cookie")
		r.Header.Del("Origin")
		r.Header.Set("Authorization", "Bearer "+strings.Repeat("a", 32))
	}
	for _, path := range []string{"/api/v1/provider-logins/grok"} {
		if rec := e.do("POST", path, "", bearer); rec.Code != http.StatusForbidden {
			t.Errorf("automation bearer started a sign-in: %d", rec.Code)
		}
		if rec := e.do("POST", path, "", func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") }); rec.Code != http.StatusForbidden {
			t.Errorf("cross-site start: %d", rec.Code)
		}
	}
	if rec := e.do("GET", "/api/v1/provider-logins", "", bearer); rec.Code != http.StatusForbidden {
		t.Errorf("automation bearer read sign-in codes: %d", rec.Code)
	}

	// A broker that refuses the controller's token is reported, not relayed.
	_ = os.WriteFile(token, []byte("stale"), 0o600)
	if rec := e.do("GET", "/api/v1/provider-logins", ""); rec.Code != http.StatusBadGateway {
		t.Fatalf("refused token: %d", rec.Code)
	}
}

func TestProviderLoginsOff(t *testing.T) {
	e := providerFixture(t, true)
	if rec := e.do("POST", "/api/v1/provider-logins/grok", ""); rec.Code != http.StatusForbidden {
		t.Fatal(rec.Code)
	}
	if rec := e.do("GET", "/api/v1/provider-logins", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"configured":false`) {
		t.Fatal(rec.Code, rec.Body)
	}
	for _, bad := range []string{"ftp://broker", "http://user@broker:8443", "http://broker:8443/path"} {
		if _, err := NewProviderLogins(bad, "/token"); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
	if _, err := NewProviderLogins("http://broker:8443", ""); err == nil {
		t.Error("accepted without token file")
	}
}
