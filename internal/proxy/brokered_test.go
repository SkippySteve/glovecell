package proxy

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
)

// brokeredProxy runs a project proxy whose fixed policy names only a Git host,
// with mutable brokered list and grant files.
type brokeredProxy struct {
	mu     sync.Mutex
	list   string
	grants map[string]any
	client *http.Client
	hits   int
}

func newBrokeredProxy(t *testing.T) *brokeredProxy {
	t.Helper()
	b := &brokeredProxy{}
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		b.hits++
		b.mu.Unlock()
		w.Write([]byte("provider"))
	}))
	t.Cleanup(upstream.Close)
	cert, key, err := GenerateCA()
	if err != nil {
		t.Fatal(err)
	}
	p, err := New(Config{Rules: []api.HTTPSRule{{Host: "git.example.com", Methods: []string{"GET"}, Paths: []string{"/repo.git"}}}}, cert, key)
	if err != nil {
		t.Fatal(err)
	}
	p.BrokeredList = func() ([]byte, error) { b.mu.Lock(); defer b.mu.Unlock(); return []byte(b.list), nil }
	p.ProviderGrants = func() ([]byte, error) { b.mu.Lock(); defer b.mu.Unlock(); return json.Marshal(b.grants) }
	upstreamURL, _ := url.Parse(upstream.URL)
	roots := x509.NewCertPool()
	roots.AddCert(upstream.Certificate())
	p.Transport.TLSClientConfig = &tls.Config{RootCAs: roots, ServerName: "example.com"}
	p.Transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, upstreamURL.Host)
	}
	server := httptest.NewServer(p)
	t.Cleanup(server.Close)
	serverURL, _ := url.Parse(server.URL)
	ca := x509.NewCertPool()
	ca.AppendCertsFromPEM(cert)
	b.client = &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(serverURL), TLSClientConfig: &tls.Config{RootCAs: ca}, DisableKeepAlives: true}}
	return b
}

func (b *brokeredProxy) set(list string, grants map[string]any) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.list, b.grants = list, grants
}

// status returns the HTTP status, or 0 when the proxy refused the tunnel.
func (b *brokeredProxy) status(t *testing.T, method, target, token string) int {
	t.Helper()
	req, _ := http.NewRequest(method, target, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := b.client.Do(req)
	if err != nil {
		return 0
	}
	_, _ = io.Copy(io.Discard, res.Body)
	res.Body.Close()
	return res.StatusCode
}

func grokGrant(enabled bool, token string, expires time.Time) map[string]any {
	if !enabled {
		return map[string]any{"grok": map[string]any{"enabled": false}}
	}
	return map[string]any{"grok": map[string]any{"enabled": true, "access": map[string]any{"accessToken": token, "expiresAt": expires.Unix(), "issuer": "https://auth.x.ai"}}}
}

func TestBrokeredRouteNeedsOptInLiveGrantAndMatchingToken(t *testing.T) {
	b := newBrokeredProxy(t)
	responses := "https://cli-chat-proxy.grok.com/v1/responses"
	hour := time.Now().Add(time.Hour)

	// Not opted in: the grant alone opens nothing.
	b.set("", grokGrant(true, "lease", hour))
	if got := b.status(t, "POST", responses, "lease"); got == 200 {
		t.Fatal("grant without opt-in reached the provider")
	}
	// Opted in and live: the reviewed route works with the current token only.
	b.set("grok", grokGrant(true, "lease", hour))
	if got := b.status(t, "POST", responses, "lease"); got != 200 {
		t.Fatal("brokered route denied", got)
	}
	for _, test := range []struct{ method, target, token string }{
		{"POST", responses, "stale"},
		{"POST", responses, ""},
		{"DELETE", responses, "lease"},
		{"GET", "https://cli-chat-proxy.grok.com/admin", "lease"},
		{"POST", "https://api2.cursor.sh/aiserver.v1.AiService/AvailableModels", "lease"},
	} {
		if got := b.status(t, test.method, test.target, test.token); got == 200 {
			t.Fatal("reached the provider", test)
		}
	}
	// Revoked, expired or malformed: denied on the next request.
	for _, grants := range []map[string]any{grokGrant(false, "", hour), grokGrant(true, "lease", time.Now().Add(30*time.Second)), {"grok": map[string]any{"enabled": true, "access": map[string]any{"accessToken": "lease", "expiresAt": hour.Unix(), "refreshToken": "r"}}}} {
		b.set("grok", grants)
		if got := b.status(t, "POST", responses, "lease"); got == 200 {
			t.Fatal("non-live grant reached the provider", grants)
		}
	}
	// An unreviewed name in the opt-in list fails closed for every provider.
	b.set("grok,muse", grokGrant(true, "lease", hour))
	if got := b.status(t, "POST", responses, "lease"); got == 200 {
		t.Fatal("unreviewed opt-in list accepted")
	}
	// The fixed policy is unaffected.
	if got := b.status(t, "GET", "https://git.example.com/repo.git/info/refs", ""); got != 200 {
		t.Fatal("fixed policy broken", got)
	}
}

func TestBrokeredCursorKeepsAccountRoutesClosed(t *testing.T) {
	b := newBrokeredProxy(t)
	hour := time.Now().Add(time.Hour)
	b.set("cursor", map[string]any{"cursor": map[string]any{"enabled": true, "access": map[string]any{"accessToken": "jwt", "expiresAt": hour.Unix()}}})
	if got := b.status(t, "POST", "https://api2.cursor.sh/aiserver.v1.AiService/AvailableModels", "jwt"); got != 200 {
		t.Fatal("Cursor route denied", got)
	}
	if got := b.status(t, "POST", "https://agentn.global.api5.cursor.sh/agent.v1.AgentService/Run", "jwt"); got != 200 {
		t.Fatal("Cursor agent route denied", got)
	}
	for _, path := range []string{"/oauth/token", "/auth/poll", "/auth"} {
		if got := b.status(t, "POST", "https://api2.cursor.sh"+path, "jwt"); got == 200 {
			t.Fatal("worker reached Cursor account route", path)
		}
	}
}

func TestParseBrokeredProviders(t *testing.T) {
	for raw, ok := range map[string]bool{"": true, "grok": true, "cursor,grok": true, "grok,grok": false, "codex,claude": true, "gemini": false, "grok,": false, " grok ": true} {
		if _, err := ParseBrokeredProviders([]byte(raw)); (err == nil) != ok {
			t.Error(raw, err)
		}
	}
}
