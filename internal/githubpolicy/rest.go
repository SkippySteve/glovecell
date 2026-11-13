package githubpolicy

import (
	"bytes"
	"encoding/json"
	"net/url"
	"strings"
)

// Verdict is the outcome of classifying one request.
type Verdict int

const (
	// NotBrokered: the request is not one the broker handles (another
	// repository, the web UI). The proxy applies the project's fixed rules to
	// it and never attaches the brokered credential.
	NotBrokered Verdict = iota
	// Deny: refused; Reason says why.
	Deny
	// Allow: forward with the brokered credential.
	Allow
	// NeedsApproval: forward only if a live operator approval matches
	// Approval exactly; otherwise refuse and report the request.
	NeedsApproval
	// Inspect: the body decides. Push requests carry ref updates
	// (CheckPush); GraphQL requests carry an operation (ClassifyGraphQL).
	Inspect
)

func (v Verdict) String() string {
	return [...]string{"not-brokered", "denied", "allowed", "approval-required", "inspect"}[v]
}

// Decision is what the proxy does with one request.
type Decision struct {
	Verdict Verdict
	// Action is a stable name for audit records, such as "pull.create".
	Action string
	// Repo is the "owner/name" the request acts on, if any.
	Repo string
	// Write is true for anything that changes state at GitHub.
	Write bool
	// Reason explains a denial. It is safe to show to the agent.
	Reason string
	// Approval is the canonical request for NeedsApproval.
	Approval *ApprovalRequest
	// Node is a GraphQL node ID the proxy must resolve to a repository
	// before allowing the mutation (ClassifyGraphQL).
	Node string
}

func deny(action, repo, reason string) Decision {
	return Decision{Verdict: Deny, Action: action, Repo: repo, Write: true, Reason: reason}
}
func allow(action, repo string, write bool) Decision {
	return Decision{Verdict: Allow, Action: action, Repo: repo, Write: write}
}

// MaxInspectedBody bounds REST and GraphQL bodies the proxy reads to decide.
const MaxInspectedBody = 1 << 20

// NeedsBody reports whether Classify needs the request body to decide. The
// proxy then reads at most MaxInspectedBody bytes and replays them upstream.
func NeedsBody(host, method string) bool {
	return host == "api.github.com" && method != "GET" && method != "HEAD"
}

// Classify decides a request to a brokered GitHub host. path must be the
// request's decoded path; the proxy has already refused escaped paths.
func Classify(p *Policy, host, method, path, rawQuery string, body []byte) Decision {
	if p == nil || !IsHost(host) {
		return Decision{}
	}
	segs, ok := segments(path)
	if !ok {
		return deny("invalid-path", "", "malformed path")
	}
	if host == "github.com" {
		return classifyGit(p, method, segs, rawQuery)
	}
	return classifyAPI(p, method, segs, rawQuery, body)
}

func segments(path string) ([]string, bool) {
	if !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "\\\x00%") {
		return nil, false
	}
	if path == "/" {
		return nil, true
	}
	segs := strings.Split(path[1:], "/")
	for _, s := range segs {
		if s == "" || s == "." || s == ".." {
			return nil, false
		}
	}
	return segs, true
}

// classifyGit handles git smart HTTP on github.com. Only the four transport
// routes of an allowed repository are brokered.
func classifyGit(p *Policy, method string, segs []string, rawQuery string) Decision {
	if len(segs) < 3 {
		return Decision{}
	}
	repo := segs[0] + "/" + strings.TrimSuffix(segs[1], ".git")
	if !p.RepoAllowed(repo) {
		return Decision{}
	}
	route := strings.Join(segs[2:], "/")
	query, err := url.ParseQuery(rawQuery)
	if err != nil {
		return deny("git", repo, "malformed query")
	}
	switch {
	case method == "GET" && route == "info/refs" && len(query) == 1 && len(query["service"]) == 1:
		switch query.Get("service") {
		case "git-upload-pack":
			return allow("git.fetch", repo, false)
		case "git-receive-pack":
			return allow("git.push-discovery", repo, false)
		}
	case method == "POST" && route == "git-upload-pack" && rawQuery == "":
		return allow("git.fetch", repo, false)
	case method == "POST" && route == "git-receive-pack" && rawQuery == "":
		return Decision{Verdict: Inspect, Action: "git.push", Repo: repo, Write: true}
	}
	if strings.HasPrefix(route, "info/lfs") {
		return deny("git.lfs", repo, "Git LFS is not brokered")
	}
	return Decision{}
}

func classifyAPI(p *Policy, method string, segs []string, rawQuery string, body []byte) Decision {
	read := method == "GET" || method == "HEAD"
	path := strings.Join(segs, "/")
	switch {
	case path == "graphql" && method == "POST":
		return Decision{Verdict: Inspect, Action: "graphql", Write: true}
	case read && (path == "user" || path == "rate_limit"):
		return allow("read", "", false)
	case path == "user/repos" && method == "POST":
		return createRepository(body)
	}
	if len(segs) < 3 || segs[0] != "repos" {
		return Decision{}
	}
	repo := segs[1] + "/" + segs[2]
	if _, _, ok := SplitRepo(repo); !ok {
		return deny("invalid-repository", "", "malformed repository")
	}
	rest := segs[3:]
	// A fork's source need not be one of the project's repositories: the fork
	// lands in the brokered account and needs an approval naming the source.
	if method == "POST" && len(rest) == 1 && rest[0] == "forks" {
		return forkRepository(repo, body)
	}
	if !p.RepoAllowed(repo) {
		return Decision{}
	}
	if read {
		return allow("read", repo, false)
	}
	return classifyRepoWrite(p, repo, method, rest, rawQuery, body)
}

// route is one allow-listed repository write.
type route struct {
	method, pattern, action string
	// check returns a denial reason, or "".
	check func(p *Policy, body map[string]any) string
}

// keys refuses body fields outside allowed, so a reviewed route cannot carry
// an unreviewed side effect.
func keys(allowed ...string) func(*Policy, map[string]any) string {
	return func(_ *Policy, body map[string]any) string {
		for k := range body {
			if !contains(allowed, k) {
				return "field " + k + " is not brokered on this route"
			}
		}
		return ""
	}
}

func reviewEvent(_ *Policy, body map[string]any) string {
	switch body["event"] {
	case nil, "COMMENT", "REQUEST_CHANGES":
		return ""
	}
	return "approving pull requests is reserved for a human reviewer"
}

func needs(flag func(*Policy) bool, reason string) func(*Policy, map[string]any) string {
	return func(p *Policy, _ map[string]any) string {
		if !flag(p) {
			return reason
		}
		return ""
	}
}

var repoWrites = []route{
	{"POST", "pulls", "pull.create", keys("title", "head", "head_repo", "base", "body", "maintainer_can_modify", "draft", "issue")},
	{"PATCH", "pulls/#", "pull.update", keys("title", "body", "state", "base", "maintainer_can_modify")},
	{"PUT", "pulls/#/merge", "pull.merge", needs(func(p *Policy) bool { return p.AllowMerge }, "merging pull requests is off for this project")},
	{"POST", "pulls/#/comments", "pull.comment", nil},
	{"POST", "pulls/#/comments/#/replies", "pull.comment", nil},
	{"PATCH", "pulls/comments/#", "comment.update", keys("body")},
	{"POST", "pulls/#/reviews", "pull.review", reviewEvent},
	{"POST", "pulls/#/reviews/#/events", "pull.review", reviewEvent},
	{"POST", "pulls/#/requested_reviewers", "pull.reviewers", keys("reviewers", "team_reviewers")},
	{"POST", "issues", "issue.create", keys("title", "body", "labels", "assignees", "milestone", "type")},
	{"PATCH", "issues/#", "issue.update", keys("title", "body", "state", "state_reason", "labels", "assignees", "milestone")},
	{"POST", "issues/#/comments", "issue.comment", keys("body")},
	{"PATCH", "issues/comments/#", "comment.update", keys("body")},
	{"POST", "issues/#/labels", "issue.labels", nil},
	{"POST", "issues/#/reactions", "reaction", keys("content")},
	{"POST", "issues/comments/#/reactions", "reaction", keys("content")},
	{"POST", "pulls/comments/#/reactions", "reaction", keys("content")},
	{"POST", "releases", "release.create", needs(func(p *Policy) bool { return p.AllowReleases }, "releases are off for this project")},
	{"PATCH", "releases/#", "release.update", needs(func(p *Policy) bool { return p.AllowReleases }, "releases are off for this project")},
	{"POST", "releases/generate-notes", "release.notes", needs(func(p *Policy) bool { return p.AllowReleases }, "releases are off for this project")},
}

// neverBrokered names destructive or administrative areas with a clear reason.
var neverBrokered = map[string]string{
	"transfer":                        "repository transfer is never brokered",
	"collaborators":                   "collaborators are never brokered",
	"invitations":                     "collaborators are never brokered",
	"hooks":                           "webhooks are never brokered",
	"keys":                            "deploy keys are never brokered",
	"actions":                         "Actions settings, secrets and runs are never brokered",
	"environments":                    "environments and their secrets are never brokered",
	"dependabot":                      "Dependabot secrets are never brokered",
	"codespaces":                      "Codespaces secrets are never brokered",
	"rulesets":                        "rulesets are never brokered",
	"branches":                        "branch protection is never brokered",
	"pages":                           "repository settings are never brokered",
	"autolinks":                       "repository settings are never brokered",
	"topics":                          "repository settings are never brokered",
	"properties":                      "repository settings are never brokered",
	"interaction-limits":              "repository settings are never brokered",
	"vulnerability-alerts":            "repository settings are never brokered",
	"automated-security-fixes":        "repository settings are never brokered",
	"private-vulnerability-reporting": "repository settings are never brokered",
	"secret-scanning":                 "security settings are never brokered",
	"code-scanning":                   "security settings are never brokered",
	"contents":                        "write with git push to a branch, not the contents API",
	"git":                             "write with git push to a branch, not the Git data API",
	"merges":                          "direct merges are never brokered; open a pull request",
	"deployments":                     "deployments are never brokered",
}

func classifyRepoWrite(p *Policy, repo, method string, rest []string, rawQuery string, raw []byte) Decision {
	if len(rest) == 0 {
		switch method {
		case "DELETE":
			return deny("repo.delete", repo, "repository deletion is never brokered")
		case "PATCH":
			return deny("repo.settings", repo, "repository settings, visibility, renames and archiving are never brokered")
		}
		return deny("repo.write", repo, "this GitHub API write is not brokered")
	}
	if rawQuery != "" {
		return deny("repo.write", repo, "query parameters are not brokered on writes")
	}
	if method == "DELETE" {
		if rest[0] == "releases" {
			return deny("release.delete", repo, "deleting releases is never brokered")
		}
		return deny("delete", repo, "deletion is never brokered")
	}
	for _, r := range repoWrites {
		if r.method != method || !matchPattern(r.pattern, rest) {
			continue
		}
		var body map[string]any
		if len(bytes.TrimSpace(raw)) > 0 {
			if json.Unmarshal(raw, &body) != nil || body == nil {
				return deny(r.action, repo, "request body must be a JSON object")
			}
		}
		if r.check != nil {
			if why := r.check(p, body); why != "" {
				return deny(r.action, repo, why)
			}
		}
		return allow(r.action, repo, true)
	}
	if why, ok := neverBrokered[rest[0]]; ok {
		return deny("repo."+rest[0], repo, why)
	}
	return deny("repo.write", repo, "this GitHub API write is not brokered")
}

// matchPattern matches "pulls/#/merge" against path segments; # is a number.
func matchPattern(pattern string, segs []string) bool {
	parts := strings.Split(pattern, "/")
	if len(parts) != len(segs) {
		return false
	}
	for i, part := range parts {
		if part == "#" {
			if !number(segs[i]) {
				return false
			}
		} else if part != segs[i] {
			return false
		}
	}
	return true
}

func number(s string) bool {
	if s == "" || len(s) > 19 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

var createRepositoryFields = []string{"name", "description", "homepage", "private", "visibility", "has_issues", "has_projects", "has_wiki", "has_discussions", "has_downloads", "auto_init", "gitignore_template", "license_template", "allow_squash_merge", "allow_merge_commit", "allow_rebase_merge", "allow_auto_merge", "delete_branch_on_merge", "squash_merge_commit_title", "squash_merge_commit_message", "merge_commit_title", "merge_commit_message", "is_template"}

// createRepository canonicalises POST /user/repos. GitHub creates a public
// repository when neither "private" nor "visibility" says otherwise, so a
// request that does not ask for private needs a public approval.
func createRepository(raw []byte) Decision {
	var body map[string]any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if d.Decode(&body) != nil || body == nil {
		return deny("repo.create", "", "request body must be a JSON object")
	}
	for k := range body {
		if !contains(createRepositoryFields, k) {
			return deny("repo.create", "", "field "+k+" is not brokered for repository creation")
		}
	}
	name, _ := body["name"].(string)
	if !ValidRepoName(name) {
		return deny("repo.create", "", "repository name missing or invalid")
	}
	visibility := "public"
	private, hasPrivate := body["private"]
	if hasPrivate {
		b, ok := private.(bool)
		if !ok {
			return deny("repo.create", "", "private must be true or false")
		}
		if b {
			visibility = "private"
		}
	}
	if v, ok := body["visibility"]; ok {
		s, _ := v.(string)
		if s != "private" && s != "public" || hasPrivate && s != visibility {
			return deny("repo.create", "", "visibility must be private or public and agree with private")
		}
		visibility = s
	}
	return Decision{Verdict: NeedsApproval, Action: "repo.create", Write: true, Approval: &ApprovalRequest{Action: ActionCreateRepository, Name: name, Visibility: visibility}}
}

// forkRepository canonicalises POST /repos/{owner}/{repo}/forks. Forks always
// land in the brokered account: an organization target is refused.
func forkRepository(source string, raw []byte) Decision {
	var body map[string]any
	if len(bytes.TrimSpace(raw)) > 0 {
		if json.Unmarshal(raw, &body) != nil || body == nil {
			return deny("repo.fork", source, "request body must be a JSON object")
		}
	}
	name := strings.SplitN(source, "/", 2)[1]
	for k, v := range body {
		switch k {
		case "name":
			s, _ := v.(string)
			if !ValidRepoName(s) {
				return deny("repo.fork", source, "fork name invalid")
			}
			name = s
		case "default_branch_only":
			if _, ok := v.(bool); !ok {
				return deny("repo.fork", source, "default_branch_only must be true or false")
			}
		default:
			return deny("repo.fork", source, "field "+k+" is not brokered for forks; forks go to the brokered account")
		}
	}
	return Decision{Verdict: NeedsApproval, Action: "repo.fork", Repo: source, Write: true, Approval: &ApprovalRequest{Action: ActionForkRepository, Name: name, Source: source}}
}
