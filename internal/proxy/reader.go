package proxy

import (
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ReaderRoute is an operator-owned internal service behind HTTPS inspection.
// It never creates a worker network exception. The reader independently projects
// responses and authorizes methods and resources. Only its proxy may reach it.
type ReaderRoute struct {
	Host     string `json:"host"`
	Upstream string `json:"upstream"`
}

func (r *ReaderRoute) Validate() error {
	if r == nil {
		return nil
	}
	u, err := url.Parse(r.Upstream)
	if err != nil || u.Scheme != "http" || net.ParseIP(u.Hostname()) != nil || (!strings.HasSuffix(u.Hostname(), ".svc") && !strings.HasSuffix(u.Hostname(), ".svc.cluster.local")) || !dnsHost.MatchString(u.Hostname()) || u.Port() != "8080" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || !dnsHost.MatchString(r.Host) || !strings.HasSuffix(r.Host, ".invalid") {
		return errors.New("reader requires an exact reserved HTTPS host and internal HTTP service on port 8080")
	}
	return nil
}
func (s *Server) roundTrip(req *http.Request) (*http.Response, error) {
	if !s.runtimeReady() {
		return nil, errors.New("proxy recovery required")
	}
	route := s.Config.Reader
	if route == nil || req.URL.Hostname() != route.Host {
		return s.Transport.RoundTrip(req)
	}
	if (req.Method != "GET" && req.Method != "HEAD" && req.Method != "POST") || !(strings.HasPrefix(req.URL.Path, "/k8s/") || req.URL.Path == "/mcp") {
		return nil, errors.New("reader route denied")
	}
	upstream, _ := url.Parse(route.Upstream)
	clone := req.Clone(req.Context())
	clone.URL.Scheme = upstream.Scheme
	clone.URL.Host = upstream.Host
	clone.Host = upstream.Host
	clone.Header = make(http.Header)
	for _, name := range []string{"Accept", "Content-Type"} {
		if v := req.Header.Get(name); v != "" {
			clone.Header.Set(name, v)
		}
	}
	// Separate transport: no private-address exceptions in the ordinary HTTPS dialer.
	return s.ReaderTransport.RoundTrip(clone)
}
func readerTransport() *http.Transport {
	return &http.Transport{Proxy: nil, ResponseHeaderTimeout: 30 * time.Second, IdleConnTimeout: 30 * time.Second, MaxIdleConns: 8}
}
