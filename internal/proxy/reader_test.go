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
	"strings"
	"testing"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/reader"
)

func TestReaderUsesInspectedTLSAndCannotReachAnotherAuthority(t *testing.T) {
	for _, h2 := range []bool{false, true} {
		t.Run(map[bool]string{false: "http1", true: "http2"}[h2], func(t *testing.T) {
			calls := 0
			fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
					t.Error("worker credentials reached trusted reader")
				}
				_ = json.NewEncoder(w).Encode(reader.ProjectK8s(map[string]any{"kind": "Pod", "metadata": map[string]any{"name": "fixture", "annotations": map[string]any{"password": "INLINE_PASSWORD"}}}))
			}))
			defer fixture.Close()
			ca, key, _ := GenerateCA()
			p, err := New(Config{Rules: []api.HTTPSRule{{Host: "reader.invalid", Methods: []string{"GET"}, Paths: []string{"/k8s"}}}, Reader: &ReaderRoute{Host: "reader.invalid", Upstream: "http://reader.fixture.svc:8080"}}, ca, key)
			if err != nil {
				t.Fatal(err)
			}
			address, _ := url.Parse(fixture.URL)
			p.ReaderTransport.DialContext = func(ctx context.Context, network, host string) (net.Conn, error) {
				if host != "reader.fixture.svc:8080" {
					t.Fatal("unapproved internal host")
				}
				return (&net.Dialer{}).DialContext(ctx, network, address.Host)
			}
			p.Transport.DialContext = func(context.Context, string, string) (net.Conn, error) {
				t.Fatal("reader used ordinary external transport")
				return nil, nil
			}
			server := httptest.NewServer(p)
			defer server.Close()
			proxyURL, _ := url.Parse(server.URL)
			roots := x509.NewCertPool()
			roots.AppendCertsFromPEM(ca)
			transport := &http.Transport{Proxy: http.ProxyURL(proxyURL), TLSClientConfig: &tls.Config{RootCAs: roots}, ForceAttemptHTTP2: h2}
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport}
			request, _ := http.NewRequest("GET", "https://reader.invalid/k8s/api/v1/pods", nil)
			request.Header.Set("Cookie", "private")
			request.Header.Set("Authorization", "Bearer private")
			response, err := client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := io.ReadAll(response.Body)
			response.Body.Close()
			if response.StatusCode != 200 || strings.Contains(string(raw), "INLINE_PASSWORD") {
				t.Fatal("inspected read failed")
			}
			if h2 && response.ProtoMajor != 2 {
				t.Fatal("HTTP/2 not exercised")
			}
			response, err = client.Post("https://reader.invalid/k8s/api/v1/pods", "application/json", strings.NewReader("{}"))
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if response.StatusCode != 403 || calls != 1 {
				t.Fatal("mutation reached reader")
			}
			if response, err = client.Get("https://other.invalid/k8s/api/v1/pods"); err == nil {
				response.Body.Close()
				t.Fatal("unapproved authority allowed")
			}
			response, err = client.Get(server.URL + "/k8s/api/v1/pods")
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if response.StatusCode != 405 {
				t.Fatal("direct HTTP allowed")
			}
		})
	}
}
func TestReaderRouteRejectsUnreviewedConfiguration(t *testing.T) {
	for _, u := range []string{"https://reader.svc:8080", "http://reader.svc:80", "http://user:pass@reader.svc:8080", "http://reader.svc:8080/path", "http://127.0.0.1:8080", "http://reader.svc:8080?x=1"} {
		if (&ReaderRoute{Host: "reader.invalid", Upstream: u}).Validate() == nil {
			t.Fatal("unsafe route allowed", u)
		}
	}
}
