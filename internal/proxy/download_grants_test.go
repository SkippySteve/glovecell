package proxy

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/download"
)

// A synthetic token (64 identical characters), obviously not a real credential.
var grantToken = strings.Repeat("c", 64)

type seen struct {
	mu   sync.Mutex
	reqs []*http.Request
}

func (s *seen) add(r *http.Request) {
	s.mu.Lock()
	s.reqs = append(s.reqs, r.Clone(context.Background()))
	s.mu.Unlock()
}
func (s *seen) list() []*http.Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*http.Request(nil), s.reqs...)
}

// grantProxy starts a project proxy whose grant transport reaches the given
// upstream handler, and returns a client that talks through it like the
// worker helper does (HTTP/1.1, inspection CA, no redirects).
func grantProxy(t *testing.T, h http.HandlerFunc) (*Server, *http.Client, *seen) {
	t.Helper()
	log := &seen{}
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { log.add(r); h(w, r) }))
	t.Cleanup(up.Close)
	cert, key, err := GenerateCA()
	if err != nil {
		t.Fatal(err)
	}
	p, err := New(Config{Rules: []api.HTTPSRule{{Host: "git.example.com", Methods: []string{"GET"}, Paths: []string{"/repo.git"}}}}, cert, key)
	if err != nil {
		t.Fatal(err)
	}
	upURL, _ := url.Parse(up.URL)
	roots := x509.NewCertPool()
	roots.AddCert(up.Certificate())
	p.GrantTransport = newGrantTransport(func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, upURL.Host)
	})
	p.GrantTransport.TLSClientConfig = &tls.Config{RootCAs: roots, ServerName: "example.com"}
	srv := httptest.NewServer(p)
	t.Cleanup(srv.Close)
	proxyURL, _ := url.Parse(srv.URL)
	ca := x509.NewCertPool()
	ca.AppendCertsFromPEM(cert)
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL), DisableKeepAlives: true, TLSClientConfig: &tls.Config{RootCAs: ca, NextProtos: []string{"http/1.1"}}, TLSNextProto: map[string]func(string, *tls.Conn) http.RoundTripper{}}}
	return p, client, log
}

func grant(id, raw, mode string) DownloadGrant {
	return DownloadGrant{ID: id, URL: raw, Mode: mode, TokenSHA256: download.TokenHash(grantToken), Expires: time.Now().Add(5 * time.Minute).Unix(), MaxBytes: 1 << 20, TimeoutSeconds: 60}
}

func send(t *testing.T, c *http.Client, method, raw string, token string, header map[string]string) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest(method, raw, nil)
	if token != "" {
		req.Header.Set(download.TokenHeader, token)
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	res, err := c.Do(req)
	if err != nil {
		return nil, nil
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res, b
}

func TestDownloadGrantIsExactAndSingleUse(t *testing.T) {
	p, c, log := grantProxy(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("payload")) })
	const target = "https://files.example.com/dl/tool.tgz?sig=abc"
	if err := p.grants.add(grant("g1", target, "fetch")); err != nil {
		t.Fatal(err)
	}
	// Other URLs, methods and tokens are refused and do not consume the grant.
	for _, attempt := range []struct{ method, url, token string }{
		{"GET", "https://files.example.com/dl/tool.tgz?sig=abd", grantToken},
		{"GET", "https://files.example.com/dl/tool.tgz", grantToken},
		{"GET", "https://files.example.com/dl/other.tgz?sig=abc", grantToken},
		{"POST", target, grantToken},
		{"HEAD", target, grantToken},
		{"GET", target, strings.Repeat("f", 64)},
		{"GET", target, ""},
	} {
		res, _ := send(t, c, attempt.method, attempt.url, attempt.token, nil)
		if res == nil || res.StatusCode != 403 {
			t.Fatalf("%+v: %v", attempt, res)
		}
	}
	if len(log.list()) != 0 {
		t.Fatal("refused attempts reached the destination")
	}
	res, body := send(t, c, "GET", target, grantToken, map[string]string{"Authorization": "Bearer agent", "Cookie": "a=b", "X-Exfil": "data"})
	if res == nil || res.StatusCode != 200 || string(body) != "payload" {
		t.Fatalf("granted fetch failed: %v %q", res, body)
	}
	up := log.list()
	if len(up) != 1 || up[0].Method != "GET" || up[0].URL.RequestURI() != "/dl/tool.tgz?sig=abc" || up[0].Host != "files.example.com" {
		t.Fatalf("upstream saw %+v", up)
	}
	for k := range up[0].Header {
		switch k {
		case "User-Agent", "Accept", "Accept-Encoding", "Connection":
		default:
			t.Errorf("worker header %s reached the destination", k)
		}
	}
	// A used grant no longer opens its host: CONNECT itself is refused.
	res, _ = send(t, c, "GET", target, grantToken, nil)
	if res != nil && res.StatusCode != 403 {
		t.Fatal("grant was reusable")
	}
	if len(log.list()) != 1 {
		t.Fatal("second request reached the destination")
	}
	st, _ := p.grants.get("g1")
	if !st.Used || !st.Done || st.Status != 200 || st.Bytes != 7 {
		t.Fatalf("%+v", st)
	}
}

func TestDownloadGrantDoesNotWidenProjectRules(t *testing.T) {
	p, c, _ := grantProxy(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("x")) })
	// No grant: the host is not in the project rules, so CONNECT is denied.
	if res, _ := send(t, c, "GET", "https://files.example.com/a", "", nil); res != nil && res.StatusCode != 403 {
		t.Fatal("ungranted host reachable")
	}
	if err := p.grants.add(grant("g2", "https://files.example.com/a", "fetch")); err != nil {
		t.Fatal(err)
	}
	// With a waiting grant, a request without the token still meets the project rules.
	if res, _ := send(t, c, "GET", "https://files.example.com/a", "", nil); res == nil || res.StatusCode != 403 {
		t.Fatal("grant host opened to ordinary requests")
	}
}

func TestDownloadGrantExpires(t *testing.T) {
	p, c, log := grantProxy(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("x")) })
	g := grant("g3", "https://files.example.com/a", "fetch")
	if err := p.grants.add(g); err != nil {
		t.Fatal(err)
	}
	p.grants.now = func() time.Time { return time.Now().Add(6 * time.Minute) }
	if p.grants.hostLive("files.example.com") {
		t.Fatal("expired grant keeps its host open")
	}
	if res, _ := send(t, c, "GET", g.URL, grantToken, nil); res != nil && res.StatusCode != 403 {
		t.Fatal("expired grant used")
	}
	if len(log.list()) != 0 {
		t.Fatal("expired grant reached the destination")
	}
	st, _ := p.grants.get("g3")
	if !st.Expired || st.Used {
		t.Fatalf("%+v", st)
	}
}

func TestProbeGrantNeverReturnsABodyAndFallsBackToGet(t *testing.T) {
	p, c, log := grantProxy(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/signed":
			if r.Method == "HEAD" {
				w.WriteHeader(403) // signature covers the method
				return
			}
			w.Header().Set("Content-Type", "application/gzip")
			w.Header().Set("Content-Length", "11")
			w.Write([]byte("secret body"))
		case "/moved":
			http.Redirect(w, r, "/elsewhere/file.bin?sig=2", http.StatusFound)
		}
	})
	if err := p.grants.add(grant("p1", "https://files.example.com/signed", "probe")); err != nil {
		t.Fatal(err)
	}
	res, body := send(t, c, "HEAD", "https://files.example.com/signed", grantToken, nil)
	if res == nil || res.StatusCode != 200 || len(body) != 0 {
		t.Fatalf("%v %q", res, body)
	}
	up := log.list()
	if len(up) != 2 || up[0].Method != "HEAD" || up[1].Method != "GET" {
		t.Fatalf("probe requests: %d", len(up))
	}
	st, _ := p.grants.get("p1")
	if st.Method != "GET" || st.Status != 200 || st.ContentLength != 11 || st.ContentType != "application/gzip" || st.Bytes != 0 {
		t.Fatalf("%+v", st)
	}
	tok2 := strings.Repeat("1", 64)
	g := grant("p2", "https://files.example.com/moved", "probe")
	g.TokenSHA256 = download.TokenHash(tok2)
	if err := p.grants.add(g); err != nil {
		t.Fatal(err)
	}
	if res, _ := send(t, c, "HEAD", g.URL, tok2, nil); res == nil || res.StatusCode != 302 || res.Header.Get("Location") != "" {
		t.Fatalf("probe redirect answer: %v", res)
	}
	st, _ = p.grants.get("p2")
	if st.Location != "https://files.example.com/elsewhere/file.bin?sig=2" {
		t.Fatalf("location %q", st.Location)
	}
}

func TestFetchGrantSizeLimit(t *testing.T) {
	big := bytes.Repeat([]byte("z"), 4096)
	p, c, _ := grantProxy(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/declared" {
			w.Header().Set("Content-Length", strconv.Itoa(len(big)))
			w.Write(big)
			return
		}
		for i := 0; i < 16; i++ {
			w.Write(big[:512])
			w.(http.Flusher).Flush()
		}
	})
	for i, path := range []string{"/declared", "/chunked"} {
		tok := strings.Repeat(strconv.Itoa(i+2), 64)
		g := grant("s"+strconv.Itoa(i), "https://files.example.com"+path, "fetch")
		g.TokenSHA256 = download.TokenHash(tok)
		g.MaxBytes = 1000
		if err := p.grants.add(g); err != nil {
			t.Fatal(err)
		}
		req, _ := http.NewRequest("GET", g.URL, nil)
		req.Header.Set(download.TokenHeader, tok)
		res, err := c.Do(req)
		if err == nil {
			got, rerr := io.ReadAll(res.Body)
			res.Body.Close()
			if res.StatusCode == 200 && rerr == nil {
				t.Fatalf("%s: oversize body delivered as complete (%d bytes)", path, len(got))
			}
		}
		st, _ := p.grants.get(g.ID)
		if st.Failure != "size limit" || st.Bytes > 1000 {
			t.Fatalf("%s: %+v", path, st)
		}
	}
}

func TestGrantValidation(t *testing.T) {
	b := newGrantBook()
	ok := grant("ok", "https://files.example.com/a", "fetch")
	for name, g := range map[string]DownloadGrant{
		"http":      func() DownloadGrant { g := ok; g.URL = "http://files.example.com/a"; return g }(),
		"ip":        func() DownloadGrant { g := ok; g.URL = "https://10.0.0.1/a"; return g }(),
		"noncanon":  func() DownloadGrant { g := ok; g.URL = "https://files.example.com:443/a"; return g }(),
		"id":        func() DownloadGrant { g := ok; g.ID = "../x"; return g }(),
		"mode":      func() DownloadGrant { g := ok; g.Mode = "put"; return g }(),
		"token":     func() DownloadGrant { g := ok; g.TokenSHA256 = "x"; return g }(),
		"expired":   func() DownloadGrant { g := ok; g.Expires = time.Now().Unix() - 1; return g }(),
		"too long":  func() DownloadGrant { g := ok; g.Expires = time.Now().Add(time.Hour).Unix(); return g }(),
		"no limit":  func() DownloadGrant { g := ok; g.MaxBytes = 0; return g }(),
		"over cap":  func() DownloadGrant { g := ok; g.MaxBytes = download.MaxMaxBytes + 1; return g }(),
		"no expiry": func() DownloadGrant { g := ok; g.TimeoutSeconds = 0; return g }(),
	} {
		if err := b.add(g); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if err := b.add(ok); err != nil {
		t.Fatal(err)
	}
	if err := b.add(ok); err == nil {
		t.Fatal("duplicate id accepted")
	}
}

func TestGrantControlAndCLI(t *testing.T) {
	p, _, _ := grantProxy(t, func(w http.ResponseWriter, r *http.Request) {})
	ctl := httptest.NewServer(p.ControlHandler())
	defer ctl.Close()
	old := grantControlURL
	grantControlURL = ctl.URL
	defer func() { grantControlURL = old }()
	g := grant("cli1", "https://files.example.com/a", "fetch")
	line, _ := json.Marshal(GrantCommand{Op: "add", Grant: &g})
	var out bytes.Buffer
	if err := GrantCLI(context.Background(), bytes.NewReader(append(line, '\n')), &out); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := GrantCLI(context.Background(), strings.NewReader(`{"op":"status","id":"cli1"}`+"\n"), &out); err != nil {
		t.Fatal(err)
	}
	var st GrantStatus
	if json.Unmarshal(out.Bytes(), &st) != nil || st.ID != "cli1" || st.Used {
		t.Fatal(out.String())
	}
	if err := GrantCLI(context.Background(), bytes.NewReader(append(line, '\n')), io.Discard); err == nil || !strings.Contains(err.Error(), "already") {
		t.Fatal("duplicate add", err)
	}
	if err := GrantCLI(context.Background(), strings.NewReader(`{"op":"revoke","id":"cli1"}`+"\n"), io.Discard); err != nil {
		t.Fatal(err)
	}
	if _, ok := p.grants.get("cli1"); ok {
		t.Fatal("revoked grant still present")
	}
	for _, bad := range []string{`{"op":"exec"}`, `{"op":"status","id":"../x"}`, `{"op":"add"}`, `{"op":"add","extra":1}`} {
		if err := GrantCLI(context.Background(), strings.NewReader(bad+"\n"), io.Discard); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}
