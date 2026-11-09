package proxy

import (
	"bufio"
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/download"
)

// One-time download grants ("Download into sandbox").
//
// The controller adds a grant by exec'ing `agent-project-controller
// download-grant` in this proxy's pod; that command talks to the loopback
// control listener below, which no other pod can reach. A grant names one
// exact URL (host, escaped path and raw query) and the SHA-256 of a random
// token. The worker helper presents the token in download.TokenHeader. The
// first matching request consumes the grant, whatever its outcome; the grant
// also expires on its own. The proxy builds the upstream request itself: GET
// (fetch) or HEAD (probe), no body, and only its own fixed headers, so the
// worker cannot add headers, cookies or credentials, and no project credential
// (GitHub, Muse, provider tokens) is ever attached to it.
//
// Grants live only in this process. A proxy restart drops every grant, which
// fails closed.

// DownloadGrant is the controller's request.
type DownloadGrant struct {
	ID             string `json:"id"`
	URL            string `json:"url"`
	Mode           string `json:"mode"` // probe or fetch
	TokenSHA256    string `json:"tokenSha256"`
	Expires        int64  `json:"expires"`
	MaxBytes       int64  `json:"maxBytes,omitempty"`
	TimeoutSeconds int    `json:"timeoutSeconds,omitempty"`
}

// GrantStatus is what the proxy observed. Location is the redirect target the
// destination answered with, resolved against the request URL; the controller
// trusts it because the worker never touches it.
type GrantStatus struct {
	ID            string `json:"id"`
	Used          bool   `json:"used"`
	Done          bool   `json:"done"`
	Expired       bool   `json:"expired"`
	Method        string `json:"method,omitempty"`
	Status        int    `json:"status,omitempty"`
	Location      string `json:"location,omitempty"`
	ContentLength int64  `json:"contentLength"`
	ContentType   string `json:"contentType,omitempty"`
	Bytes         int64  `json:"bytes"`
	Failure       string `json:"failure,omitempty"`
}

type grantEntry struct {
	DownloadGrant
	host, uri string
	status    GrantStatus
}

type grantBook struct {
	mu     sync.Mutex
	grants map[string]*grantEntry
	now    func() time.Time
}

const maxGrants = 32

var grantIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
var hexHash = regexp.MustCompile(`^[0-9a-f]{64}$`)

func newGrantBook() *grantBook { return &grantBook{grants: map[string]*grantEntry{}, now: time.Now} }

func (b *grantBook) add(g DownloadGrant) error {
	if !grantIDPattern.MatchString(g.ID) {
		return errors.New("invalid grant id")
	}
	u, err := download.ParseURL(g.URL)
	if err != nil || download.Exact(u) != g.URL {
		return errors.New("grant URL must be an exact https URL")
	}
	if !hexHash.MatchString(g.TokenSHA256) {
		return errors.New("grant token hash required")
	}
	now := b.now()
	if g.Expires <= now.Unix() || g.Expires > now.Add(download.GrantTTL+time.Minute).Unix() {
		return errors.New("grant expiry must be within the grant lifetime")
	}
	switch g.Mode {
	case "probe":
		g.MaxBytes = 0
		if g.TimeoutSeconds <= 0 || g.TimeoutSeconds > 120 {
			g.TimeoutSeconds = 60
		}
	case "fetch":
		if g.MaxBytes <= 0 || g.MaxBytes > download.MaxMaxBytes {
			return errors.New("fetch grant needs a size limit")
		}
		if g.TimeoutSeconds <= 0 || time.Duration(g.TimeoutSeconds)*time.Second > download.MaxTimeout {
			return errors.New("fetch grant needs a timeout")
		}
	default:
		return errors.New("grant mode must be probe or fetch")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for id, e := range b.grants {
		if e.Expires < now.Add(-15*time.Minute).Unix() {
			delete(b.grants, id)
		}
	}
	if _, dup := b.grants[g.ID]; dup {
		return errors.New("grant id already used")
	}
	if len(b.grants) >= maxGrants {
		return errors.New("too many download grants")
	}
	b.grants[g.ID] = &grantEntry{DownloadGrant: g, host: u.Hostname(), uri: download.RequestURI(u), status: GrantStatus{ID: g.ID, ContentLength: -1}}
	return nil
}

func (b *grantBook) get(id string) (GrantStatus, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	e, ok := b.grants[id]
	if !ok {
		return GrantStatus{}, false
	}
	s := e.status
	s.Expired = !s.Used && e.Expires <= b.now().Unix()
	return s, true
}

func (b *grantBook) revoke(id string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.grants, id)
}

// hostLive admits a CONNECT to host while an unused grant for it waits.
func (b *grantBook) hostLive(host string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now().Unix()
	for _, e := range b.grants {
		if e.host == host && !e.status.Used && e.Expires > now {
			return true
		}
	}
	return false
}

// claim consumes the grant this request presents, or says why it cannot.
func (b *grantBook) claim(host string, req *http.Request) (*grantEntry, string) {
	hash := download.TokenHash(req.Header.Get(download.TokenHeader))
	b.mu.Lock()
	defer b.mu.Unlock()
	var found *grantEntry
	for _, e := range b.grants {
		if subtle.ConstantTimeCompare([]byte(e.TokenSHA256), []byte(hash)) == 1 {
			found = e
		}
	}
	switch {
	case found == nil:
		return nil, "unknown grant"
	case found.status.Used:
		return nil, "grant already used"
	case found.Expires <= b.now().Unix():
		return nil, "grant expired"
	case found.host != host || strings.TrimSuffix(req.Host, ":443") != host:
		return nil, "grant is for another host"
	case req.URL.IsAbs() || req.RequestURI != found.uri:
		return nil, "URL differs from the grant"
	case found.Mode == "probe" && req.Method != http.MethodHead, found.Mode == "fetch" && req.Method != http.MethodGet:
		return nil, "method differs from the grant"
	case req.ContentLength != 0 || len(req.TransferEncoding) > 0 || req.Header.Get("Upgrade") != "":
		return nil, "grant requests carry no body or upgrade"
	}
	found.status.Used = true
	return found, ""
}

func (b *grantBook) finish(e *grantEntry, s GrantStatus) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s.ID, s.Used, s.Done = e.ID, true, true
	e.status = s
}

// dialPublic is the grant dialer: port 443, public addresses only, with no
// private-host exception (grants never reach the cluster or the LAN).
func (s *Server) dialPublic(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, e := net.SplitHostPort(address)
	if e != nil || port != "443" {
		return nil, errors.New("only TLS port 443 is allowed")
	}
	ips, e := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if e != nil || len(ips) == 0 {
		return nil, errors.New("upstream resolution failed")
	}
	for _, ip := range ips {
		if !PublicIP(ip) {
			return nil, errors.New("upstream resolves to a prohibited address")
		}
	}
	d := net.Dialer{Timeout: 10 * time.Second}
	return d.DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
}

func newGrantTransport(dial func(context.Context, string, string) (net.Conn, error)) *http.Transport {
	return &http.Transport{Proxy: nil, DialContext: dial, TLSClientConfig: nil, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 60 * time.Second, DisableKeepAlives: true, DisableCompression: true, ForceAttemptHTTP2: false}
}

// headRefused are answers that often mean "this URL accepts GET only":
// signed object-store URLs bind the method into the signature (403), and some
// servers do not implement HEAD (405, 501) or reject it (400).
func headRefused(status int) bool {
	return status == 400 || status == 403 || status == 405 || status == 501
}

func upstreamRequest(ctx context.Context, method, host, uri string) *http.Request {
	u, _ := url.Parse("https://" + host + uri)
	req := (&http.Request{Method: method, URL: u, Host: host, Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1, Header: http.Header{}}).WithContext(ctx)
	req.Header.Set("User-Agent", download.UserAgent)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Encoding", "identity")
	return req
}

func observed(res *http.Response, method string) GrantStatus {
	s := GrantStatus{Method: method, Status: res.StatusCode, ContentLength: res.ContentLength, ContentType: res.Header.Get("Content-Type")}
	if len(s.ContentType) > 200 {
		s.ContentType = s.ContentType[:200]
	}
	if res.StatusCode >= 300 && res.StatusCode < 400 {
		if loc, err := res.Location(); err == nil && len(loc.String()) <= download.MaxURLLength {
			s.Location = loc.String()
		} else if raw := res.Header.Get("Location"); raw != "" {
			s.Failure = "redirect Location unusable"
		}
	}
	return s
}

// capped returns an error once more than max bytes were read, so a cut-off
// body is never delivered as complete (HTTP/1.1 chunked framing stays open).
type capped struct {
	r     io.Reader
	max   int64
	count int64
}

var errSizeLimit = errors.New("download exceeds its size limit")

func (c *capped) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.count += int64(n)
	if c.count > c.max {
		return 0, errSizeLimit
	}
	return n, err
}

// serveGrant answers one claimed grant request on the inspected connection.
func (s *Server) serveGrant(parent context.Context, conn net.Conn, raw net.Conn, host string, e *grantEntry) {
	timeout := time.Duration(e.TimeoutSeconds) * time.Second
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	_ = raw.SetDeadline(time.Now().Add(timeout + 30*time.Second))
	transport := s.GrantTransport
	result := GrantStatus{ContentLength: -1}
	recorded := false
	// Record before answering when no body follows, so the controller (which
	// asks after the helper exits) always reads the final status and Location.
	record := func() { s.grants.finish(e, result); recorded = true }
	defer func() {
		if !recorded {
			s.grants.finish(e, result)
		}
		u, _ := url.Parse("https://" + host + e.uri)
		slog.Info("download grant used", "grant", e.ID, "mode", e.Mode, "host", host, "path", u.EscapedPath(), "query_sha256", download.QueryHash(u), "method", result.Method, "status", result.Status, "bytes", result.Bytes, "failure", result.Failure)
	}()
	method := http.MethodGet
	if e.Mode == "probe" {
		method = http.MethodHead
	}
	res, err := transport.RoundTrip(upstreamRequest(ctx, method, host, e.uri))
	if err == nil && e.Mode == "probe" && headRefused(res.StatusCode) {
		res.Body.Close()
		method = http.MethodGet
		res, err = transport.RoundTrip(upstreamRequest(ctx, method, host, e.uri))
	}
	if err != nil {
		result = GrantStatus{Method: method, ContentLength: -1, Failure: "upstream " + failureKind(err)}
		record()
		writeError(conn, 502, "upstream unavailable")
		return
	}
	defer res.Body.Close()
	result = observed(res, method)
	reply := &http.Response{StatusCode: res.StatusCode, ProtoMajor: 1, ProtoMinor: 1, Header: http.Header{}, Close: true, Body: http.NoBody, ContentLength: 0}
	if e.Mode == "probe" || res.StatusCode != http.StatusOK {
		// Probes and non-200 answers never carry a body into the sandbox.
		record()
		_ = reply.Write(conn)
		return
	}
	if res.ContentLength > e.MaxBytes {
		result.Failure = "size limit"
		record()
		writeError(conn, http.StatusRequestEntityTooLarge, "download exceeds its size limit")
		return
	}
	body := &capped{r: res.Body, max: e.MaxBytes}
	reply.Body, reply.ContentLength = io.NopCloser(body), res.ContentLength
	if res.ContentLength < 0 {
		// Chunked framing: a body cut at the size limit lacks its final chunk,
		// so the helper sees an error rather than a complete short file.
		reply.TransferEncoding = []string{"chunked"}
	}
	if ct := res.Header.Get("Content-Type"); ct != "" {
		reply.Header.Set("Content-Type", ct)
	}
	err = reply.Write(conn)
	result.Bytes = body.count
	if result.Bytes > e.MaxBytes {
		result.Bytes = e.MaxBytes
	}
	if errors.Is(err, errSizeLimit) {
		result.Failure = "size limit"
	} else if err != nil {
		result.Failure = "transfer " + failureKind(err)
	}
}

// ControlHandler serves the loopback-only grant API.
func (s *Server) ControlHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ready", s.readiness)
	mux.HandleFunc("POST /grants", func(w http.ResponseWriter, r *http.Request) {
		var g DownloadGrant
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
		d.DisallowUnknownFields()
		if err := d.Decode(&g); err != nil {
			http.Error(w, "invalid grant", 400)
			return
		}
		if err := s.grants.add(g); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		u, _ := url.Parse(g.URL)
		slog.Info("download grant added", "grant", g.ID, "mode", g.Mode, "host", u.Hostname(), "path", u.EscapedPath(), "query_sha256", download.QueryHash(u), "expires", g.Expires, "max_bytes", g.MaxBytes)
		w.WriteHeader(201)
		writeControlJSON(w, map[string]string{"id": g.ID})
	})
	mux.HandleFunc("GET /grants/{id}", func(w http.ResponseWriter, r *http.Request) {
		st, ok := s.grants.get(r.PathValue("id"))
		if !ok {
			http.Error(w, "no such grant", 404)
			return
		}
		writeControlJSON(w, st)
	})
	mux.HandleFunc("DELETE /grants/{id}", func(w http.ResponseWriter, r *http.Request) {
		s.grants.revoke(r.PathValue("id"))
		writeControlJSON(w, map[string]string{"id": r.PathValue("id")})
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.runtimeReady() {
			http.Error(w, "proxy recovery required", 503)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func writeControlJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// ControlAddress is loopback only: reachable from processes in the proxy pod
// (the controller's exec), never from the worker or other pods.
const ControlAddress = "127.0.0.1:3129"

var grantControlURL = "http://" + ControlAddress

// GrantCommand is one request of `agent-project-controller download-grant`.
type GrantCommand struct {
	Op    string         `json:"op"` // add, status, revoke
	ID    string         `json:"id,omitempty"`
	Grant *DownloadGrant `json:"grant,omitempty"`
}

// GrantCLI reads one GrantCommand line from stdin and prints the control
// listener's JSON answer. The controller runs it through exec in the proxy pod.
func GrantCLI(ctx context.Context, stdin io.Reader, stdout io.Writer) error {
	line, err := bufio.NewReaderSize(stdin, 16<<10).ReadSlice('\n')
	if err != nil {
		return errors.New("command line missing")
	}
	var cmd GrantCommand
	d := json.NewDecoder(bytes.NewReader(line))
	d.DisallowUnknownFields()
	if d.Decode(&cmd) != nil {
		return errors.New("command does not parse")
	}
	var req *http.Request
	switch cmd.Op {
	case "add":
		if cmd.Grant == nil {
			return errors.New("grant required")
		}
		body, _ := json.Marshal(cmd.Grant)
		req, _ = http.NewRequestWithContext(ctx, "POST", grantControlURL+"/grants", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	case "status", "revoke":
		if !grantIDPattern.MatchString(cmd.ID) {
			return errors.New("grant id required")
		}
		method := "GET"
		if cmd.Op == "revoke" {
			method = "DELETE"
		}
		req, _ = http.NewRequestWithContext(ctx, method, grantControlURL+"/grants/"+cmd.ID, nil)
	default:
		return errors.New("op must be add, status or revoke")
	}
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: nil}}
	res, err := client.Do(req)
	if err != nil {
		return errors.New("proxy control listener unavailable")
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	if res.StatusCode >= 300 {
		return fmt.Errorf("proxy refused: %s", strings.TrimSpace(string(raw)))
	}
	_, err = stdout.Write(raw)
	return err
}
