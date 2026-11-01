package proxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/githubpolicy"
)

// GitHub brokering (spec.github). The project's proxy is the only holder of
// the brokered GitHub credential and the enforcement point: it decides every
// request to github.com and api.github.com with internal/githubpolicy and
// attaches the credential only to allowed requests. The worker uses a
// placeholder token; whatever it sends as Authorization is replaced.
//
// Two keys of the proxy-ca Secret (mounted at /tls) drive it, both reread on
// every request so a revocation applies to the next request:
//   - github-policy: the project's policy, written by the controller;
//   - github-access: the current access lease, copied by the controller from
//     the GitHub broker. Its expiry is capped at a few minutes, so a stopped
//     broker or a withdrawn lease ends access quickly.
//
// Without github-policy GitHub brokering is off and nothing here changes the
// proxy's behaviour.

const (
	// GitHubAccessKey is the proxy-ca key holding the access lease.
	GitHubAccessKey = "github-access"
	// GitHubPolicyKey is the proxy-ca key holding the project's policy.
	GitHubPolicyKey = "github-policy"
	// AgentIdentityHeader optionally names the agent inside the worker. It is
	// a claim (the worker sets it), recorded next to the project identity,
	// which comes from the proxy itself. It is never forwarded.
	AgentIdentityHeader = "X-Agent-Identity"
)

// GitHubLease is the access lease document. It never carries refresh
// material: unknown fields fail closed.
type GitHubLease struct {
	Enabled   bool   `json:"enabled"`
	Token     string `json:"token,omitempty"`
	ExpiresAt int64  `json:"expiresAt,omitempty"`
	Login     string `json:"login,omitempty"`
}

var githubTokenShape = regexp.MustCompile(`^[A-Za-z0-9_]{20,255}$`)

// ParseGitHubLease decodes a lease strictly.
func ParseGitHubLease(raw []byte) (GitHubLease, error) {
	var l GitHubLease
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || len(raw) > 4<<10 {
		return GitHubLease{}, errors.New("GitHub lease unavailable")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&l) != nil || d.Decode(new(any)) != io.EOF {
		return GitHubLease{}, errors.New("invalid GitHub lease")
	}
	if l.Enabled && (!githubTokenShape.MatchString(l.Token) || !githubpolicy.ValidOwner(l.Login)) {
		return GitHubLease{}, errors.New("invalid GitHub lease")
	}
	return l, nil
}

// Live reports an enabled lease valid for more than a minute.
func (l GitHubLease) Live(now time.Time) bool {
	return l.Enabled && l.Token != "" && l.ExpiresAt > now.Unix()+60
}

// githubBook holds per-proxy GitHub state: approvals in flight or used, and
// recently looked-up default branches.
type githubBook struct {
	mu        sync.Mutex
	approvals map[string]int // 1 in flight, 2 used
	branches  map[string]cachedBranch
}
type cachedBranch struct {
	name  string
	until time.Time
}

func newGitHubBook() *githubBook {
	return &githubBook{approvals: map[string]int{}, branches: map[string]cachedBranch{}}
}

// claim reserves an approval for one request. An approval is used at most
// once: a second request while the first is in flight, or after it
// succeeded, is refused. A proxy restart forgets used approvals; GitHub then
// refuses a second creation of the same name, and approvals expire.
func (b *githubBook) claim(key string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.approvals[key] != 0 {
		return false
	}
	b.approvals[key] = 1
	return true
}
func (b *githubBook) finish(key string, used bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if used {
		b.approvals[key] = 2
	} else if b.approvals[key] == 1 {
		delete(b.approvals, key)
	}
}

// githubState reads the policy and lease. policy is nil when brokering is
// off; err reports a broken policy, which refuses every GitHub request.
func (s *Server) githubState() (*githubpolicy.Policy, GitHubLease, error) {
	if s.GitHubPolicy == nil {
		return nil, GitHubLease{}, nil
	}
	raw, err := s.GitHubPolicy()
	if err != nil {
		return nil, GitHubLease{}, errors.New("GitHub policy unavailable")
	}
	policy, err := githubpolicy.Parse(raw)
	if err != nil || policy == nil {
		return policy, GitHubLease{}, err
	}
	if s.GitHubAccess == nil {
		return policy, GitHubLease{}, nil
	}
	raw, err = s.GitHubAccess()
	if err != nil {
		return policy, GitHubLease{}, nil
	}
	lease, err := ParseGitHubLease(raw)
	if err != nil {
		return policy, GitHubLease{}, nil
	}
	return policy, lease, nil
}

// githubHostLive admits a CONNECT to a GitHub host while brokering is on and
// its lease is live.
func (s *Server) githubHostLive(host string) bool {
	if !githubpolicy.IsHost(host) {
		return false
	}
	policy, lease, err := s.githubState()
	return err == nil && policy != nil && lease.Live(time.Now())
}

// githubCall is a brokered request that passed the policy.
type githubCall struct {
	decision githubpolicy.Decision
	policy   *githubpolicy.Policy
	token    string
	login    string
	agent    string
	refs     []string
	approval *githubpolicy.Approval
	started  time.Time
}

// githubDenial is a refusal written to the worker.
type githubDenial struct {
	status int
	body   []byte
	json   bool
}

func (d *githubDenial) writeTo(w io.Writer) {
	res := http.Response{StatusCode: d.status, Status: fmt.Sprintf("%d %s", d.status, http.StatusText(d.status)), ProtoMajor: 1, ProtoMinor: 1, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(d.body)), ContentLength: int64(len(d.body)), Close: true}
	if d.json {
		res.Header.Set("Content-Type", "application/json")
	}
	_ = res.Write(w)
}
func (d *githubDenial) writeHTTP(w http.ResponseWriter) {
	if d.json {
		w.Header().Set("Content-Type", "application/json")
	}
	w.WriteHeader(d.status)
	_, _ = w.Write(d.body)
}

// denial builds a GitHub-style JSON error, which gh and git print.
func denial(status int, message string, approval *githubpolicy.ApprovalRequest) *githubDenial {
	body := map[string]any{"message": message}
	if approval != nil {
		body["approval"] = approval
	}
	raw, _ := json.Marshal(body)
	return &githubDenial{status: status, body: raw, json: true}
}

var agentIdentity = regexp.MustCompile(`^[A-Za-z0-9._:@/-]{1,64}$`)

// prepareGitHub decides one request to a GitHub host. It returns (nil, nil)
// when the request is not brokered: the fixed rules then apply and any
// placeholder credential has been removed. A non-nil call has its body
// replayed and is ready for inject.
func (s *Server) prepareGitHub(ctx context.Context, host string, req *http.Request) (*githubCall, *githubDenial) {
	if !githubpolicy.IsHost(host) {
		return nil, nil
	}
	policy, lease, err := s.githubState()
	if policy == nil && err == nil {
		return nil, nil // Brokering is off.
	}
	agent := req.Header.Get(AgentIdentityHeader)
	req.Header.Del(AgentIdentityHeader)
	if !agentIdentity.MatchString(agent) {
		agent = ""
	}
	if err != nil {
		s.githubAudit(nil, policy, agent, "denied", "GitHub policy invalid", nil)
		return nil, denial(http.StatusForbidden, "GitHub access for this project is misconfigured", nil)
	}
	var body []byte
	if githubpolicy.NeedsBody(host, req.Method) {
		if enc := req.Header.Get("Content-Encoding"); enc != "" && enc != "identity" {
			return nil, denial(http.StatusUnsupportedMediaType, "compressed GitHub API requests are not brokered", nil)
		}
		body, err = io.ReadAll(io.LimitReader(req.Body, githubpolicy.MaxInspectedBody+1))
		req.Body.Close()
		if err != nil {
			return nil, denial(http.StatusBadRequest, "request body unreadable", nil)
		}
		if len(body) > githubpolicy.MaxInspectedBody {
			return nil, denial(http.StatusRequestEntityTooLarge, "GitHub API request too large", nil)
		}
		req.Body = io.NopCloser(bytes.NewReader(body))
		req.ContentLength = int64(len(body))
	}
	d := githubpolicy.Classify(policy, host, req.Method, req.URL.Path, req.URL.RawQuery, body)
	if d.Verdict == githubpolicy.NotBrokered {
		req.Header.Del("Authorization")
		return nil, nil
	}
	call := &githubCall{decision: d, policy: policy, token: lease.Token, login: lease.Login, agent: agent, started: time.Now()}
	if !lease.Live(time.Now()) {
		s.githubAudit(call, policy, agent, "denied", "GitHub credential expired or revoked", nil)
		return nil, denial(http.StatusForbidden, "GitHub access for this project is expired, revoked or unavailable", nil)
	}
	switch d.Verdict {
	case githubpolicy.Deny:
		s.githubAudit(call, policy, agent, "denied", d.Reason, nil)
		return nil, denial(http.StatusForbidden, d.Reason, nil)
	case githubpolicy.Inspect:
		if d.Action == "git.push" {
			if why := s.inspectPush(ctx, call, req); why != nil {
				return nil, why
			}
		} else if why := s.inspectGraphQL(ctx, call, body); why != nil {
			return nil, why
		}
	}
	if call.decision.Verdict == githubpolicy.NeedsApproval {
		if why := s.githubApproval(call); why != nil {
			return nil, why
		}
	}
	if call.decision.Verdict != githubpolicy.Allow && call.decision.Verdict != githubpolicy.NeedsApproval {
		s.githubAudit(call, policy, agent, "denied", "undecided request", nil)
		return nil, denial(http.StatusForbidden, "request denied", nil)
	}
	return call, nil
}

// inspectPush reads the ref updates at the head of a git-receive-pack body,
// checks them against the repository's current default branch, and replays
// the consumed bytes upstream in front of the rest of the body.
func (s *Server) inspectPush(ctx context.Context, call *githubCall, req *http.Request) *githubDenial {
	if enc := req.Header.Get("Content-Encoding"); enc != "" && enc != "identity" {
		s.githubAudit(call, call.policy, call.agent, "denied", "compressed push", nil)
		return denial(http.StatusUnsupportedMediaType, "compressed pushes are not brokered", nil)
	}
	reader := bufio.NewReaderSize(req.Body, 32<<10)
	updates, consumed, err := githubpolicy.ReadCommands(reader)
	if err != nil {
		s.githubAudit(call, call.policy, call.agent, "denied", err.Error(), nil)
		return denial(http.StatusBadRequest, err.Error(), nil)
	}
	for _, u := range updates {
		call.refs = append(call.refs, u.Ref)
	}
	if len(updates) > 0 {
		branch, err := s.githubDefaultBranch(ctx, call.decision.Repo, call.token)
		if err != nil {
			s.githubAudit(call, call.policy, call.agent, "denied", "default branch unknown", nil)
			return denial(http.StatusServiceUnavailable, "the repository's default branch could not be checked; push refused", nil)
		}
		if err := call.policy.CheckPush(updates, branch); err != nil {
			s.githubAudit(call, call.policy, call.agent, "denied", err.Error(), nil)
			return denial(http.StatusForbidden, err.Error(), nil)
		}
	}
	original := req.Body
	req.Body = struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(consumed), reader), original}
	call.decision.Verdict = githubpolicy.Allow
	return nil
}

// inspectGraphQL decides a GraphQL body and resolves the repository (or, for
// repository creation, the owner) a mutation acts on.
func (s *Server) inspectGraphQL(ctx context.Context, call *githubCall, body []byte) *githubDenial {
	d := githubpolicy.ClassifyGraphQL(call.policy, body)
	call.decision = d
	if d.Verdict == githubpolicy.Deny {
		s.githubAudit(call, call.policy, call.agent, "denied", d.Reason, nil)
		return denial(http.StatusForbidden, d.Reason, nil)
	}
	if d.Node == "" {
		if d.Verdict == githubpolicy.Inspect {
			return denial(http.StatusForbidden, "request denied", nil)
		}
		return nil
	}
	kind, name, err := s.githubResolveNode(ctx, d.Node, call.token)
	if err != nil {
		s.githubAudit(call, call.policy, call.agent, "denied", "node lookup failed", nil)
		return denial(http.StatusServiceUnavailable, "the mutation's target could not be checked; request refused", nil)
	}
	if d.Verdict == githubpolicy.NeedsApproval {
		// createRepository with ownerId: only the brokered account itself.
		if kind != "user" || !strings.EqualFold(name, call.login) {
			s.githubAudit(call, call.policy, call.agent, "denied", "repository owner is not the brokered account", nil)
			return denial(http.StatusForbidden, "repositories can only be created in the brokered account", nil)
		}
		return nil
	}
	call.decision.Repo = name
	if kind != "repo" || !call.policy.RepoAllowed(name) {
		s.githubAudit(call, call.policy, call.agent, "denied", "repository not allowed for this project", nil)
		return denial(http.StatusForbidden, "repository "+name+" is not allowed for this project", nil)
	}
	call.decision.Verdict = githubpolicy.Allow
	return nil
}

// githubApproval admits an action that needs approval only with a live,
// unused approval for exactly these arguments.
func (s *Server) githubApproval(call *githubCall) *githubDenial {
	req := *call.decision.Approval
	approval, ok := call.policy.Match(req, time.Now())
	if !ok {
		s.githubAudit(call, call.policy, call.agent, "approval-required", "", nil)
		return denial(http.StatusForbidden, "This GitHub action needs an operator approval for exactly these arguments. Ask the operator to approve it, then retry.", &req)
	}
	if !s.github.claim(approval.Key()) {
		s.githubAudit(call, call.policy, call.agent, "denied", "approval already used", nil)
		return denial(http.StatusConflict, "this approval has already been used; ask for a new one", &req)
	}
	call.approval = &approval
	call.decision.Verdict = githubpolicy.Allow
	return nil
}

// inject attaches the brokered credential. Git uses HTTP Basic with the token
// as password; the REST and GraphQL APIs take a bearer token.
func (c *githubCall) inject(req *http.Request) {
	req.Header.Del("Cookie")
	req.Header.Del("Authorization")
	if req.URL.Hostname() == "github.com" {
		req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("x-access-token:"+c.token)))
		return
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
}

// finishGitHub records the outcome and settles a claimed approval: it is
// used only when GitHub accepted the request.
func (s *Server) finishGitHub(call *githubCall, status int, err error) {
	if call == nil {
		return
	}
	ok := err == nil && status >= 200 && status < 300
	if call.approval != nil {
		s.github.finish(call.approval.Key(), ok)
	}
	if !call.decision.Write {
		return
	}
	outcome := "completed"
	if err != nil {
		outcome = "interrupted"
	} else if !ok {
		outcome = "refused-by-github"
	}
	s.githubAudit(call, call.policy, call.agent, outcome, "", &status)
}

// githubAudit writes one structured audit record. It never includes the
// token, request bodies or GitHub's response.
func (s *Server) githubAudit(call *githubCall, policy *githubpolicy.Policy, agent, verdict, reason string, status *int) {
	log := s.Audit
	if log == nil {
		log = slog.Default()
	}
	attrs := []any{"audit", "github", "verdict", verdict}
	if policy != nil {
		attrs = append(attrs, "project", policy.Project, "project_uid", policy.ProjectUID)
	}
	if agent != "" {
		attrs = append(attrs, "agent_claim", agent)
	}
	if call != nil {
		attrs = append(attrs, "action", call.decision.Action, "account", call.login)
		if call.decision.Repo != "" {
			attrs = append(attrs, "repository", call.decision.Repo)
		}
		if len(call.refs) > 0 {
			attrs = append(attrs, "refs", strings.Join(call.refs, ","))
		}
		if call.decision.Approval != nil {
			attrs = append(attrs, "approval_action", call.decision.Approval.Action, "approval_name", call.decision.Approval.Name, "approval_visibility", call.decision.Approval.Visibility, "approval_source", call.decision.Approval.Source)
		}
		if call.approval != nil && call.approval.ApprovedBy != "" {
			attrs = append(attrs, "approved_by", call.approval.ApprovedBy)
		}
	}
	if reason != "" {
		attrs = append(attrs, "reason", reason)
	}
	if status != nil {
		attrs = append(attrs, "status", *status)
	}
	log.Info("github brokered request", attrs...)
}

// githubAPI is where the proxy looks up default branches and node IDs. Tests
// point the transport elsewhere; the URL stays GitHub's.
const githubAPI = "https://api.github.com"

// githubDefaultBranch returns the repository's default branch, cached for a
// minute. Any failure returns an error, and the caller refuses the push.
func (s *Server) githubDefaultBranch(ctx context.Context, repo, token string) (string, error) {
	key := strings.ToLower(repo)
	s.github.mu.Lock()
	cached, ok := s.github.branches[key]
	s.github.mu.Unlock()
	if ok && time.Now().Before(cached.until) {
		return cached.name, nil
	}
	var out struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := s.githubAPICall(ctx, "GET", "/repos/"+repo, token, nil, &out); err != nil {
		return "", err
	}
	if out.DefaultBranch == "" || strings.ContainsAny(out.DefaultBranch, " \x00\n") {
		return "", errors.New("default branch unknown")
	}
	s.github.mu.Lock()
	s.github.branches[key] = cachedBranch{out.DefaultBranch, time.Now().Add(time.Minute)}
	s.github.mu.Unlock()
	return out.DefaultBranch, nil
}

const nodeQuery = `query($id: ID!) { node(id: $id) { __typename ... on Repository { nameWithOwner } ... on Issue { repository { nameWithOwner } } ... on PullRequest { repository { nameWithOwner } } ... on User { login } } }`

// githubResolveNode maps a GraphQL node ID to ("repo", "owner/name") for
// repositories, issues and pull requests, or ("user", login) for users.
func (s *Server) githubResolveNode(ctx context.Context, id, token string) (string, string, error) {
	var out struct {
		Data struct {
			Node *struct {
				Type          string `json:"__typename"`
				NameWithOwner string `json:"nameWithOwner"`
				Login         string `json:"login"`
				Repository    *struct {
					NameWithOwner string `json:"nameWithOwner"`
				} `json:"repository"`
			} `json:"node"`
		} `json:"data"`
	}
	payload, _ := json.Marshal(map[string]any{"query": nodeQuery, "variables": map[string]string{"id": id}})
	if err := s.githubAPICall(ctx, "POST", "/graphql", token, payload, &out); err != nil || out.Data.Node == nil {
		return "", "", errors.New("node lookup failed")
	}
	n := out.Data.Node
	switch n.Type {
	case "Repository":
		return "repo", n.NameWithOwner, nil
	case "Issue", "PullRequest":
		if n.Repository != nil {
			return "repo", n.Repository.NameWithOwner, nil
		}
	case "User":
		return "user", n.Login, nil
	}
	return "", "", errors.New("unsupported node")
}

func (s *Server) githubAPICall(ctx context.Context, method, path, token string, body []byte, out any) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, githubAPI+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "agent-project-controller-proxy")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := s.Transport.RoundTrip(req)
	if err != nil {
		return errors.New("GitHub lookup failed")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return errors.New("GitHub lookup failed")
	}
	return json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(out)
}

func gatewayFile(name string) func() ([]byte, error) {
	return func() ([]byte, error) {
		raw, err := os.ReadFile("/tls/" + name)
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return raw, err
	}
}
