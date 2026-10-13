package t3

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fixedToken string

func (t fixedToken) Token(context.Context) (string, error) { return string(t), nil }
func signedProof(t *testing.T, mutate func(map[string]any), private ed25519.PrivateKey) string {
	t.Helper()
	pub, _ := x509.MarshalPKIXPublicKey(private.Public())
	key := strings.TrimSpace(string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pub})))
	p := map[string]any{"environmentId": "project", "sub": "project", "iss": "t3-env:project", "aud": "https://relay.example.com", "challenge": "challenge", "environmentPublicKey": key, "endpoint": Endpoint{HTTP: "http://127.0.0.1:1234", WS: "ws://127.0.0.1:1234", Kind: "cloudflare_tunnel"}, "origin": map[string]any{"localHttpHost": "127.0.0.1", "localHttpPort": 1234}, "descriptor": map[string]any{"environmentId": "project"}, "iat": time.Now().Unix(), "exp": time.Now().Add(time.Minute).Unix(), "scopes": []string{"agent_activity_notifications", "managed_tunnels"}}
	if mutate != nil {
		mutate(p)
	}
	b, _ := json.Marshal(p)
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"EdDSA","typ":"t3-env-link+jwt"}`))
	data := header + "." + base64.RawURLEncoding.EncodeToString(b)
	return data + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(private, []byte(data)))
}
func TestProofDeniesChangedProjectOriginAccountAndSigningKey(t *testing.T) {
	_, private, _ := ed25519.GenerateKey(rand.Reader)
	valid := signedProof(t, nil, private)
	key, e := verifyProof(valid, "project", "challenge", "https://relay.example.com", "http://127.0.0.1:1234", "")
	if e != nil {
		t.Fatal(e)
	}
	for name, change := range map[string]func(map[string]any){"project": func(p map[string]any) { p["environmentId"] = "another" }, "account": func(p map[string]any) { p["aud"] = "https://evil.example.com" }, "challenge": func(p map[string]any) { p["challenge"] = "stolen" }, "origin": func(p map[string]any) {
		p["origin"] = map[string]any{"localHttpHost": "10.0.0.1", "localHttpPort": 1234}
	}, "endpoint": func(p map[string]any) {
		p["endpoint"] = Endpoint{HTTP: "http://10.0.0.1:1234", WS: "ws://10.0.0.1:1234", Kind: "cloudflare_tunnel"}
	}, "expired": func(p map[string]any) { p["exp"] = time.Now().Add(-time.Minute).Unix() }, "scope": func(p map[string]any) { p["scopes"] = []string{"managed_tunnels"} }} {
		t.Run(name, func(t *testing.T) {
			if _, e := verifyProof(signedProof(t, change, private), "project", "challenge", "https://relay.example.com", "http://127.0.0.1:1234", key); e == nil {
				t.Fatal("invalid proof accepted")
			}
		})
	}
	_, another, _ := ed25519.GenerateKey(rand.Reader)
	if _, e = verifyProof(signedProof(t, nil, another), "project", "challenge", "https://relay.example.com", "http://127.0.0.1:1234", key); e == nil {
		t.Fatal("changed signing key accepted")
	}
	tampered := valid[:strings.LastIndex(valid, ".")+1] + base64.RawURLEncoding.EncodeToString(make([]byte, 64))
	if _, e = verifyProof(tampered, "project", "challenge", "https://relay.example.com", "http://127.0.0.1:1234", key); e == nil {
		t.Fatal("forged signature accepted")
	}
}
func TestCloudDeniesRedirectAndUnverifiedRemoval(t *testing.T) {
	calls := 0
	present := true
	redirect := false
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer controller-only" {
			t.Error("missing account authorization")
		}
		if redirect {
			w.Header().Set("Location", "https://evil.example.com")
			w.WriteHeader(307)
			return
		}
		if r.Method == "DELETE" {
			fmt.Fprint(w, `{"ok":true}`)
			return
		}
		if present {
			fmt.Fprint(w, `{"environments":[{"environmentId":"project"}]}`)
		} else {
			fmt.Fprint(w, `{"environments":[]}`)
		}
	}))
	defer server.Close()
	c := &Cloud{URL: server.URL, Tokens: fixedToken("controller-only"), HTTP: server.Client()}
	if e := c.Revoke(context.Background(), "project"); e == nil {
		t.Fatal("DELETE alone reported completed revocation")
	}
	present = false
	if e := c.Revoke(context.Background(), "project"); e != nil {
		t.Fatal(e)
	}
	redirect = true
	before := calls
	if _, e := c.List(context.Background()); e == nil {
		t.Fatal("redirect accepted")
	}
	if calls != before+1 {
		t.Fatal("redirect followed")
	}
	for _, u := range []string{"http://relay.example.com", "https://user:pass@relay.example.com", "https://relay.example.com/another", "https://relay.example.com?token=secret"} {
		c.URL = u
		if c.Validate() == nil {
			t.Fatal("unsafe origin accepted", u)
		}
	}
}
func TestManagedEndpointDeniesOtherHostsAndProtocols(t *testing.T) {
	good := Endpoint{HTTP: "https://project.t3coderelay.com/", WS: "wss://project.t3coderelay.com/ws", Kind: "cloudflare_tunnel"}
	if e := validateEndpoint(good); e != nil {
		t.Fatal(e)
	}
	for _, bad := range []Endpoint{{HTTP: "http://project.t3coderelay.com", WS: good.WS}, {HTTP: "https://127.0.0.1/", WS: "wss://127.0.0.1/"}, {HTTP: "https://evil.example.com/", WS: "wss://evil.example.com/"}, {HTTP: good.HTTP, WS: "wss://other.t3coderelay.com/ws"}, {HTTP: "https://user:pass@project.t3coderelay.com/", WS: good.WS}} {
		if validateEndpoint(bad) == nil {
			t.Fatal("unsafe endpoint accepted", bad.HTTP)
		}
	}
}
func TestAccountRequiresPrivateFileAndKeepsRefreshOutsideWorker(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "userdata", "secrets")
	if e := os.MkdirAll(path, 0700); e != nil {
		t.Fatal(e)
	}
	filename := filepath.Join(path, "cloud-cli-oauth-token.bin")
	account := &Account{Directory: directory, Binary: "/must-not-execute"}
	if _, e := account.Token(context.Background()); e == nil {
		t.Fatal("missing authorization accepted")
	}
	data := []byte(fmt.Sprintf(`{"accessToken":"owner-token","expiresAtEpochMs":%d}`, time.Now().Add(time.Hour).UnixMilli()))
	if e := os.WriteFile(filename, data, 0644); e != nil {
		t.Fatal(e)
	}
	if _, e := account.Token(context.Background()); e == nil {
		t.Fatal("public credential file accepted")
	}
	if e := os.Chmod(filename, 0600); e != nil {
		t.Fatal(e)
	}
	if token, e := account.Token(context.Background()); e != nil || token != "owner-token" {
		t.Fatal("private credential not read", e)
	}
	if e := os.Remove(filename); e != nil {
		t.Fatal(e)
	}
	target := filepath.Join(directory, "token")
	_ = os.WriteFile(target, data, 0600)
	_ = os.Symlink(target, filename)
	if _, e := account.Token(context.Background()); e == nil {
		t.Fatal("symlink accepted")
	}
}

func TestDeleteErrorRequiresAuthoritativeAbsence(t *testing.T) {
	absent := false
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "DELETE" {
			if r.Header.Get("Content-Type") != "" {
				t.Error("bodyless DELETE incorrectly labeled as JSON")
			}
			http.Error(w, "response lost after operation", 500)
			return
		}
		if absent {
			fmt.Fprint(w, `{"environments":[]}`)
		} else {
			fmt.Fprint(w, `{"environments":[{"environmentId":"project"}]}`)
		}
	}))
	defer server.Close()
	c := &Cloud{URL: server.URL, Tokens: fixedToken("controller-only"), HTTP: server.Client()}
	if e := c.Revoke(context.Background(), "project"); e == nil {
		t.Fatal("error and present environment reported successful revocation")
	}
	absent = true
	if e := c.Revoke(context.Background(), "project"); e != nil {
		t.Fatal("applied revocation with lost response did not recover", e)
	}
}
