package proxy

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/githubpolicy"
)

const (
	ghZero      = "0000000000000000000000000000000000000000"
	ghA         = "1111111111111111111111111111111111111111"
	ghB         = "2222222222222222222222222222222222222222"
	placeholder = "brokered-by-project-proxy"
)

// fakeGitHub serves the parts of github.com and api.github.com the broker
// uses. It checks the credential on every request and applies a push only
// after it has read the complete body, like receive-pack.
type fakeGitHub struct {
	mu       sync.Mutex
	token    string
	refs     map[string]string // "owner/repo ref" -> oid
	repos    map[string]bool
	nodes    map[string]string // node id -> owner/repo
	hits     []string
	auth     []string
	created  int
	pushBody []byte
}

func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.hits = append(f.hits, r.Method+" "+r.Host+r.URL.Path)
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	token := f.token
	f.mu.Unlock()
	host := strings.TrimSuffix(r.Host, ":443")
	if host == "github.com" {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "x-access-token" || pass != token {
			http.Error(w, "auth", http.StatusUnauthorized)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/git-receive-pack") {
			reader := bufio.NewReader(r.Body)
			updates, _, err := githubpolicy.ReadCommands(reader)
			if err != nil {
				http.Error(w, "bad push", 400)
				return
			}
			rest, err := io.ReadAll(reader)
			if err != nil || !bytes.HasSuffix(rest, []byte("PACK-END")) {
				return // Incomplete pack: nothing applies.
			}
			repo := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/"), ".git/git-receive-pack")
			f.mu.Lock()
			for _, u := range updates {
				f.refs[repo+" "+u.Ref] = u.New
			}
			f.mu.Unlock()
		}
		w.Write([]byte("0000"))
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+token {
		http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
		return
	}
	switch {
	case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/repos/"):
		json.NewEncoder(w).Encode(map[string]string{"default_branch": "main", "full_name": strings.TrimPrefix(r.URL.Path, "/repos/")})
	case r.Method == "POST" && r.URL.Path == "/user/repos":
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		name := fmt.Sprint(body["name"])
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.repos[name] {
			http.Error(w, `{"message":"name already exists on this account"}`, 422)
			return
		}
		f.repos[name] = true
		f.created++
		w.WriteHeader(201)
		w.Write([]byte(`{"full_name":"SkippySteve/` + name + `"}`))
	case r.Method == "POST" && r.URL.Path == "/graphql":
		var body struct {
			Query     string            `json:"query"`
			Variables map[string]string `json:"variables"`
		}
		raw, _ := io.ReadAll(r.Body)
		if json.Unmarshal(raw, &body) == nil && body.Query == nodeQuery {
			f.mu.Lock()
			repo := f.nodes[body.Variables["id"]]
			f.mu.Unlock()
			json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"node": map[string]any{"__typename": "Repository", "nameWithOwner": repo}}})
			return
		}
		w.Write([]byte(`{"data":{}}`))
	default:
		w.WriteHeader(201)
		w.Write([]byte(`{}`))
	}
}

func (f *fakeGitHub) ref(repoRef string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.refs[repoRef]
}
func (f *fakeGitHub) sawPath(substr string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, h := range f.hits {
		if strings.Contains(h, substr) {
			return true
		}
	}
	return false
}

type githubHarness struct {
	t      *testing.T
	fake   *fakeGitHub
	mu     sync.Mutex
	policy []byte
	lease  []byte
	proxy  *url.URL
	ca     *x509.CertPool
	client *http.Client
	audit  *lockedBuffer // the proxy writes from its own goroutines
	seen   bytes.Buffer  // every byte the worker received
}

func (h *githubHarness) set(policy *githubpolicy.Policy, lease *GitHubLease) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.policy, h.lease = nil, nil
	if policy != nil {
		h.policy, _ = json.Marshal(policy)
	}
	if lease != nil {
		h.lease, _ = json.Marshal(lease)
	}
}

func liveLease(token string) *GitHubLease {
	return &GitHubLease{Enabled: true, Token: token, ExpiresAt: time.Now().Add(5 * time.Minute).Unix(), Login: "SkippySteve"}
}

func basePolicy() *githubpolicy.Policy {
	return &githubpolicy.Policy{Project: "demo", ProjectUID: "uid-1", Repositories: []string{"SkippySteve/app"}, ProtectedBranches: []string{"release/*"}}
}

const tokenOne = "ghu_FirstAccessTokenValue000000000001"
const tokenTwo = "ghu_SecondAccessTokenValue00000000002"

func newGitHubHarness(t *testing.T) *githubHarness {
	t.Helper()
	h := &githubHarness{t: t, audit: &lockedBuffer{}}
	h.fake = &fakeGitHub{token: tokenOne, refs: map[string]string{"SkippySteve/app refs/heads/main": ghA}, repos: map[string]bool{}, nodes: map[string]string{"R_app": "SkippySteve/app", "R_other": "Someone/else"}}
	upstream := httptest.NewTLSServer(h.fake)
	t.Cleanup(upstream.Close)
	cert, key, err := GenerateCA()
	if err != nil {
		t.Fatal(err)
	}
	p, err := New(Config{Rules: []api.HTTPSRule{{Host: "git.example.com", Methods: []string{"GET"}, Paths: []string{"/repo.git"}}}}, cert, key)
	if err != nil {
		t.Fatal(err)
	}
	p.GitHubPolicy = func() ([]byte, error) { h.mu.Lock(); defer h.mu.Unlock(); return h.policy, nil }
	p.GitHubAccess = func() ([]byte, error) { h.mu.Lock(); defer h.mu.Unlock(); return h.lease, nil }
	p.Audit = slog.New(slog.NewJSONHandler(h.audit, nil))
	upstreamURL, _ := url.Parse(upstream.URL)
	roots := x509.NewCertPool()
	roots.AddCert(upstream.Certificate())
	p.Transport.TLSClientConfig = &tls.Config{RootCAs: roots, ServerName: "example.com"}
	p.Transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, upstreamURL.Host)
	}
	server := httptest.NewServer(p)
	t.Cleanup(server.Close)
	h.proxy, _ = url.Parse(server.URL)
	h.ca = x509.NewCertPool()
	h.ca.AppendCertsFromPEM(cert)
	h.client = &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(h.proxy), TLSClientConfig: &tls.Config{RootCAs: h.ca}, DisableKeepAlives: true}}
	h.set(basePolicy(), liveLease(tokenOne))
	return h
}

// lockedBuffer is the audit log. The proxy may still be writing a line (for
// example the verdict of an interrupted push) while a test reads it, so reads
// take the same lock as writes.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// do sends a request as the worker would: with the placeholder credential.
func (h *githubHarness) do(method, target string, body []byte) (int, string) {
	h.t.Helper()
	req, _ := http.NewRequest(method, target, bytes.NewReader(body))
	if strings.HasPrefix(target, "https://github.com/") {
		req.SetBasicAuth("x-access-token", placeholder)
	} else {
		req.Header.Set("Authorization", "token "+placeholder)
	}
	req.Header.Set(AgentIdentityHeader, "claude-code")
	res, err := h.client.Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	var headers bytes.Buffer
	res.Header.Write(&headers)
	h.seen.Write(headers.Bytes())
	h.seen.Write(raw)
	return res.StatusCode, string(raw)
}

func pushBody(cmds ...string) []byte {
	var b bytes.Buffer
	for i, c := range cmds {
		if i == 0 {
			c += "\x00report-status side-band-64k"
		}
		fmt.Fprintf(&b, "%04x%s", len(c)+4, c)
	}
	b.WriteString("0000PACK-binary-objects-PACK-END")
	return b.Bytes()
}

const receivePack = "https://github.com/SkippySteve/app.git/git-receive-pack"

func TestGitHubPushToBranchAllowedDefaultAndProtectedDenied(t *testing.T) {
	h := newGitHubHarness(t)
	if code, _ := h.do("GET", "https://github.com/SkippySteve/app.git/info/refs?service=git-receive-pack", nil); code != 200 {
		t.Fatal("push discovery refused", code)
	}
	if code, body := h.do("POST", receivePack, pushBody(ghZero+" "+ghB+" refs/heads/agent/x")); code != 200 {
		t.Fatal("push to a branch refused", code, body)
	}
	if h.fake.ref("SkippySteve/app refs/heads/agent/x") != ghB {
		t.Fatal("allowed push did not arrive intact")
	}
	for _, cmd := range []string{
		ghA + " " + ghB + " refs/heads/main",           // default branch (a fast-forward or a force: both refused)
		ghZero + " " + ghB + " refs/heads/release/1.0", // protected pattern
		ghA + " " + ghB + " refs/heads/release/1.0",    // forced update of a protected branch
		ghA + " " + ghZero + " refs/heads/agent/x",     // branch deletion
		ghZero + " " + ghB + " refs/tags/v1",           // tags off
	} {
		code, body := h.do("POST", receivePack, pushBody(cmd))
		if code != 403 || !strings.Contains(body, "message") {
			t.Fatal("push allowed", cmd, code, body)
		}
	}
	// A mixed push is refused as a whole: git would apply the allowed part.
	if code, _ := h.do("POST", receivePack, pushBody(ghB+" "+ghA+" refs/heads/agent/x", ghA+" "+ghB+" refs/heads/main")); code != 403 {
		t.Fatal("mixed push allowed")
	}
	if h.fake.ref("SkippySteve/app refs/heads/main") != ghA || h.fake.ref("SkippySteve/app refs/heads/agent/x") != ghB {
		t.Fatal("a refused push changed a ref")
	}
	// git's large-push probe (a lone flush) passes without a ref update.
	if code, _ := h.do("POST", receivePack, []byte("0000")); code != 200 {
		t.Fatal("probe refused", code)
	}
	// A repository outside the policy is not brokered: no credential.
	if code, _ := h.do("POST", "https://github.com/Someone/else.git/git-receive-pack", pushBody(ghZero+" "+ghB+" refs/heads/x")); code == 200 {
		t.Fatal("push to another repository succeeded")
	}
}

func TestGitHubDestructiveAPICallsDenied(t *testing.T) {
	h := newGitHubHarness(t)
	for _, c := range []struct{ method, path, body string }{
		{"DELETE", "/repos/SkippySteve/app", ""},
		{"PATCH", "/repos/SkippySteve/app", `{"private":false}`},
		{"PATCH", "/repos/SkippySteve/app", `{"visibility":"public"}`},
		{"PATCH", "/repos/SkippySteve/app", `{"archived":true}`},
		{"POST", "/repos/SkippySteve/app/transfer", `{"new_owner":"x"}`},
		{"PUT", "/repos/SkippySteve/app/collaborators/mallory", `{}`},
		{"POST", "/repos/SkippySteve/app/hooks", `{"config":{"url":"https://x"}}`},
		{"PUT", "/repos/SkippySteve/app/actions/secrets/TOKEN", `{}`},
		{"POST", "/repos/SkippySteve/app/keys", `{"key":"ssh-ed25519 x"}`},
		{"PUT", "/repos/SkippySteve/app/pulls/1/merge", `{}`},
		{"PATCH", "/repos/SkippySteve/app/git/refs/heads/main", `{"sha":"x","force":true}`},
		{"POST", "/graphql", `{"query":"mutation($input: X!) { updateRepository(input: $input) { clientMutationId } }","variables":{"input":{"repositoryId":"R_app","visibility":"PUBLIC"}}}`},
	} {
		if code, _ := h.do(c.method, "https://api.github.com"+c.path, []byte(c.body)); code != 403 {
			t.Fatal("destructive call allowed", c, code)
		}
	}
	for _, path := range []string{"/repos/SkippySteve/app/transfer", "/collaborators", "/hooks", "/actions", "/keys", "/merge", "/git/refs"} {
		if h.fake.sawPath(path) {
			t.Fatal("refused call reached GitHub", path)
		}
	}
	// Ordinary work still flows.
	if code, _ := h.do("POST", "https://api.github.com/repos/SkippySteve/app/pulls", []byte(`{"title":"t","head":"agent/x","base":"main"}`)); code != 201 {
		t.Fatal("pull request refused", code)
	}
	if code, _ := h.do("POST", "https://api.github.com/repos/SkippySteve/app/issues/1/comments", []byte(`{"body":"done"}`)); code != 201 {
		t.Fatal("comment refused", code)
	}
}

func TestGitHubGraphQLMutationsResolveTheirRepository(t *testing.T) {
	h := newGitHubHarness(t)
	pr := func(repo string) []byte {
		return []byte(`{"query":"mutation PullRequestCreate($input: CreatePullRequestInput!) { createPullRequest(input: $input) { pullRequest { url } } }","variables":{"input":{"repositoryId":"` + repo + `","baseRefName":"main","headRefName":"agent/x","title":"t","body":"b"}}}`)
	}
	if code, body := h.do("POST", "https://api.github.com/graphql", pr("R_app")); code != 200 {
		t.Fatal("gh pr create refused", code, body)
	}
	if code, _ := h.do("POST", "https://api.github.com/graphql", pr("R_other")); code != 403 {
		t.Fatal("PR on another repository allowed", code)
	}
	if code, _ := h.do("POST", "https://api.github.com/graphql", []byte(`{"query":"query { viewer { login } }"}`)); code != 200 {
		t.Fatal("query refused", code)
	}
}

func TestGitHubRepositoryCreationNeedsApprovalAndRunsOnce(t *testing.T) {
	h := newGitHubHarness(t)
	create := []byte(`{"name":"new-tool","private":true,"auto_init":true}`)
	code, body := h.do("POST", "https://api.github.com/user/repos", create)
	if code != 403 || !strings.Contains(body, `"approval":{"action":"create-repository","name":"new-tool","visibility":"private"}`) {
		t.Fatal("creation without approval", code, body)
	}
	if !strings.Contains(h.audit.String(), `"verdict":"approval-required"`) {
		t.Fatal("pending approval not audited")
	}
	policy := basePolicy()
	policy.Approvals = []githubpolicy.Approval{{Action: githubpolicy.ActionCreateRepository, Name: "new-tool", Visibility: "private", ExpiresAt: time.Now().Add(time.Hour), ApprovedBy: "steve"}}
	h.set(policy, liveLease(tokenOne))
	// A private approval does not cover a public repository.
	if code, _ := h.do("POST", "https://api.github.com/user/repos", []byte(`{"name":"new-tool"}`)); code != 403 {
		t.Fatal("public creation matched a private approval", code)
	}
	if code, body := h.do("POST", "https://api.github.com/user/repos", create); code != 201 {
		t.Fatal("approved creation refused", code, body)
	}
	if code, _ := h.do("POST", "https://api.github.com/user/repos", create); code != 409 {
		t.Fatal("approval used twice", code)
	}
	if h.fake.created != 1 {
		t.Fatal("repository created", h.fake.created, "times")
	}
	if !strings.Contains(h.audit.String(), `"approved_by":"steve"`) {
		t.Fatal("approver not audited")
	}
	// Expired approvals never match.
	policy.Approvals[0].Name, policy.Approvals[0].ExpiresAt = "late", time.Now().Add(-time.Second)
	h.set(policy, liveLease(tokenOne))
	if code, _ := h.do("POST", "https://api.github.com/user/repos", []byte(`{"name":"late","private":true}`)); code != 403 {
		t.Fatal("expired approval used", code)
	}
}

func TestGitHubApprovalSurvivesAFailedAttempt(t *testing.T) {
	h := newGitHubHarness(t)
	policy := basePolicy()
	policy.Approvals = []githubpolicy.Approval{{Action: githubpolicy.ActionCreateRepository, Name: "taken", Visibility: "private", ExpiresAt: time.Now().Add(time.Hour)}}
	h.set(policy, liveLease(tokenOne))
	h.fake.mu.Lock()
	h.fake.repos["taken"] = true
	h.fake.mu.Unlock()
	if code, _ := h.do("POST", "https://api.github.com/user/repos", []byte(`{"name":"taken","private":true}`)); code != 422 {
		t.Fatal("expected GitHub's refusal", code)
	}
	// GitHub refused, so the approval was not used up.
	h.fake.mu.Lock()
	delete(h.fake.repos, "taken")
	h.fake.mu.Unlock()
	if code, _ := h.do("POST", "https://api.github.com/user/repos", []byte(`{"name":"taken","private":true}`)); code != 201 {
		t.Fatal("approval lost after a refused attempt", code)
	}
}

func TestGitHubTokenNeverReachesTheWorker(t *testing.T) {
	h := newGitHubHarness(t)
	h.do("GET", "https://api.github.com/repos/SkippySteve/app", nil)
	h.do("POST", receivePack, pushBody(ghZero+" "+ghB+" refs/heads/agent/y"))
	h.do("POST", receivePack, pushBody(ghA+" "+ghB+" refs/heads/main"))
	h.do("DELETE", "https://api.github.com/repos/SkippySteve/app", nil)
	h.do("POST", "https://api.github.com/user/repos", []byte(`{"name":"x"}`))
	for _, where := range []string{h.seen.String(), h.audit.String()} {
		if strings.Contains(where, tokenOne) || strings.Contains(where, base64.StdEncoding.EncodeToString([]byte("x-access-token:"+tokenOne))) {
			t.Fatal("token visible to the worker or in the audit log")
		}
	}
	h.fake.mu.Lock()
	defer h.fake.mu.Unlock()
	for _, a := range h.fake.auth {
		if strings.Contains(a, placeholder) {
			t.Fatal("placeholder forwarded to GitHub")
		}
		if a != "" && a != "Bearer "+tokenOne && a != "Basic "+base64.StdEncoding.EncodeToString([]byte("x-access-token:"+tokenOne)) {
			t.Fatal("unexpected credential at GitHub", a)
		}
	}
	if !strings.Contains(h.audit.String(), `"agent_claim":"claude-code"`) || !strings.Contains(h.audit.String(), `"project_uid":"uid-1"`) {
		t.Fatal("audit lacks identity", h.audit.String())
	}
}

func TestGitHubLeaseExpiryRefreshAndRevocation(t *testing.T) {
	h := newGitHubHarness(t)
	read := "https://api.github.com/repos/SkippySteve/app"
	if code, _ := h.do("GET", read, nil); code != 200 {
		t.Fatal("live lease refused", code)
	}
	// Expiring within a minute: refused, and the tunnel itself is closed.
	expiring := liveLease(tokenOne)
	expiring.ExpiresAt = time.Now().Add(30 * time.Second).Unix()
	h.set(basePolicy(), expiring)
	if code, _ := h.do("GET", read, nil); code == 200 {
		t.Fatal("expiring lease used")
	}
	// The broker refreshed: the new token is used, the old one never again.
	h.fake.mu.Lock()
	h.fake.token = tokenTwo
	h.fake.auth = nil
	h.fake.mu.Unlock()
	h.set(basePolicy(), liveLease(tokenTwo))
	if code, _ := h.do("GET", read, nil); code != 200 {
		t.Fatal("refreshed lease refused", code)
	}
	if code, _ := h.do("POST", receivePack, pushBody(ghZero+" "+ghB+" refs/heads/agent/z")); code != 200 {
		t.Fatal("push after refresh refused", code)
	}
	// Revoked for the project (lease withdrawn), then opted out entirely.
	for _, step := range []struct {
		policy *githubpolicy.Policy
		lease  *GitHubLease
	}{{basePolicy(), &GitHubLease{Enabled: false}}, {basePolicy(), nil}, {nil, nil}} {
		h.set(step.policy, step.lease)
		if code, _ := h.do("GET", read, nil); code == 200 {
			t.Fatal("revoked project reached GitHub", step)
		}
		if code, _ := h.do("POST", receivePack, pushBody(ghZero+" "+ghB+" refs/heads/agent/w")); code == 200 {
			t.Fatal("revoked project pushed", step)
		}
	}
	// Leases carrying refresh material or a malformed token are refused.
	for _, raw := range []string{`{"enabled":true,"token":"` + tokenTwo + `","expiresAt":9999999999,"login":"SkippySteve","refreshToken":"ghr_x"}`, `{"enabled":true,"token":"bad\r\nX: y","expiresAt":9999999999,"login":"SkippySteve"}`} {
		if _, err := ParseGitHubLease([]byte(raw)); err == nil {
			t.Fatal("accepted", raw)
		}
	}
	// A broken policy refuses every GitHub request instead of falling open.
	h.mu.Lock()
	h.policy, h.lease = []byte(`{"repositories":["SkippySteve/app"],"allowEverything":true}`), mustJSON(liveLease(tokenTwo))
	h.mu.Unlock()
	if code, _ := h.do("GET", read, nil); code == 200 {
		t.Fatal("broken policy fell open")
	}
}

func mustJSON(v any) []byte { raw, _ := json.Marshal(v); return raw }

// An interrupted push (the worker disconnects mid-pack) changes nothing at
// GitHub and is audited; the next push works.
func TestGitHubInterruptedPush(t *testing.T) {
	h := newGitHubHarness(t)
	body := pushBody(ghZero + " " + ghB + " refs/heads/agent/cut")
	conn, err := net.Dial("tcp", h.proxy.Host)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(conn, "CONNECT github.com:443 HTTP/1.1\r\nHost: github.com:443\r\n\r\n")
	br := bufio.NewReader(conn)
	res, err := http.ReadResponse(br, nil)
	if err != nil || res.StatusCode != 200 {
		t.Fatal("tunnel refused", err)
	}
	tlsConn := tls.Client(conn, &tls.Config{RootCAs: h.ca, ServerName: "github.com", NextProtos: []string{"http/1.1"}})
	if err := tlsConn.Handshake(); err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(tlsConn, "POST /SkippySteve/app.git/git-receive-pack HTTP/1.1\r\nHost: github.com\r\nContent-Type: application/x-git-receive-pack-request\r\nContent-Length: %d\r\n\r\n", len(body)+4096)
	tlsConn.Write(body[:len(body)-len("PACK-END")])
	time.Sleep(200 * time.Millisecond)
	tlsConn.Close()
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(h.audit.String(), `"verdict":"interrupted"`) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(h.audit.String(), `"verdict":"interrupted"`) {
		t.Fatal("interrupted push not audited", h.audit.String())
	}
	if h.fake.ref("SkippySteve/app refs/heads/agent/cut") != "" {
		t.Fatal("interrupted push changed a ref")
	}
	if code, _ := h.do("POST", receivePack, body); code != 200 || h.fake.ref("SkippySteve/app refs/heads/agent/cut") != ghB {
		t.Fatal("retry after interruption failed", code)
	}
}

func TestGitHubBrokeringIsInertWithoutPolicy(t *testing.T) {
	h := newGitHubHarness(t)
	h.set(nil, liveLease(tokenOne))
	if code, _ := h.do("GET", "https://api.github.com/repos/SkippySteve/app", nil); code != 0 {
		t.Fatal("GitHub reachable without a policy", code)
	}
	if len(h.fake.hits) != 0 {
		t.Fatal("GitHub contacted")
	}
}
