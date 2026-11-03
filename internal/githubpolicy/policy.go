// Package githubpolicy decides which GitHub requests a project's agents may
// make with a brokered GitHub credential. It is pure: no network, no clock
// except where a caller passes one. The project proxy (internal/proxy) is the
// enforcement point; it injects the credential only after a Decision allows
// the request, so the token never reaches the project's worker.
//
// The policy is allow-listed by method and path. Anything it does not
// recognise is denied. Pushes are decided from the ref updates in the
// git-receive-pack request (receivepack.go), GraphQL from the operation in the
// request body (graphql.go). Repository creation and forks need an operator
// approval bound to the exact arguments (Approval).
package githubpolicy

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"
	"time"
)

// Hosts are the only GitHub hosts the broker injects a credential for.
// uploads.github.com (release assets), codeload, raw and LFS hosts are never
// brokered.
var Hosts = []string{"github.com", "api.github.com"}

// IsHost reports whether host is a brokered GitHub host.
func IsHost(host string) bool { return host == "github.com" || host == "api.github.com" }

// Limits keep the policy document and its matching cheap.
const (
	MaxRepositories      = 32
	MaxProtectedBranches = 16
	MaxApprovals         = 16
	// MaxApprovalLifetime bounds how long an approval may stay usable.
	MaxApprovalLifetime = 24 * time.Hour
)

// Approval actions.
const (
	ActionCreateRepository = "create-repository"
	ActionForkRepository   = "fork-repository"
)

// Policy is one project's GitHub policy. The controller writes it as JSON to
// the proxy's Secret (key "github-policy"); the proxy rereads it on every
// request, so a change applies without a restart. Unknown fields fail closed.
type Policy struct {
	// Project and ProjectUID identify the project in audit records. They are
	// written by the controller, never by the project.
	Project    string `json:"project"`
	ProjectUID string `json:"projectUID"`
	// Repositories are "owner/name" or "owner/*". Matching is case-insensitive,
	// like GitHub's.
	Repositories []string `json:"repositories"`
	// ProtectedBranches are branch names or "prefix/*" patterns no push may
	// create, update or delete. The repository's default branch is always
	// protected, whatever this list says.
	ProtectedBranches []string `json:"protectedBranches,omitempty"`
	// PushBranches, when set, confines pushes to branches matching these
	// names or "prefix/*" patterns (for example "agent/*"). The proxy cannot
	// tell a forced update from a fast-forward, so this is how an operator
	// keeps agents off branches people work on.
	PushBranches []string `json:"pushBranches,omitempty"`
	// AllowMerge lets agents merge pull requests (PUT .../pulls/{n}/merge).
	AllowMerge bool `json:"allowMerge,omitempty"`
	// AllowReleases lets agents create and edit releases and push new tags.
	AllowReleases bool `json:"allowReleases,omitempty"`
	// Approvals are operator approvals for actions that need one.
	Approvals []Approval `json:"approvals,omitempty"`
}

// Approval is one operator approval of an action with exact arguments.
type Approval struct {
	Action string `json:"action"`
	// Name is the repository to create, or the fork's name.
	Name string `json:"name"`
	// Visibility is "private" or "public" for create-repository. Forks inherit
	// their source's visibility and leave this empty.
	Visibility string `json:"visibility,omitempty"`
	// Source is the "owner/name" being forked.
	Source    string    `json:"source,omitempty"`
	ExpiresAt time.Time `json:"expiresAt"`
	// ApprovedBy is the operator who approved it, for the audit record.
	ApprovedBy string `json:"approvedBy,omitempty"`
}

var (
	ownerName  = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)
	repoName   = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
	branchPart = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,200}$`)
)

// ValidOwner reports whether s is a syntactically valid GitHub account name.
func ValidOwner(s string) bool { return ownerName.MatchString(s) }

// ValidRepoName reports whether s is a syntactically valid repository name.
func ValidRepoName(s string) bool { return repoName.MatchString(s) && s != "." && s != ".." }

// SplitRepo splits "owner/name" and validates both parts.
func SplitRepo(full string) (string, string, bool) {
	owner, name, ok := strings.Cut(full, "/")
	if !ok || !ValidOwner(owner) || !ValidRepoName(name) {
		return "", "", false
	}
	return owner, name, true
}

// Validate checks the policy. Expired approvals are valid but never match.
func (p *Policy) Validate() error {
	if len(p.Repositories) == 0 || len(p.Repositories) > MaxRepositories {
		return errors.New("GitHub access needs between 1 and 32 repositories")
	}
	seen := map[string]bool{}
	for _, r := range p.Repositories {
		owner, name, ok := strings.Cut(r, "/")
		if !ok || !ValidOwner(owner) || name != "*" && !ValidRepoName(name) {
			return errors.New("GitHub repositories must be owner/name or owner/*")
		}
		if seen[strings.ToLower(r)] {
			return errors.New("repeated GitHub repository")
		}
		seen[strings.ToLower(r)] = true
	}
	if len(p.ProtectedBranches) > MaxProtectedBranches || len(p.PushBranches) > MaxProtectedBranches {
		return errors.New("too many branch patterns")
	}
	for _, b := range append(append([]string{}, p.ProtectedBranches...), p.PushBranches...) {
		if !validBranchPattern(b) {
			return errors.New("branch patterns must be names or prefix/* patterns")
		}
	}
	if len(p.Approvals) > MaxApprovals {
		return errors.New("too many GitHub approvals")
	}
	for _, a := range p.Approvals {
		if err := a.Validate(); err != nil {
			return err
		}
	}
	if len(p.Project) > 253 || len(p.ProjectUID) > 64 {
		return errors.New("invalid project identity")
	}
	return nil
}

// Validate checks one approval's shape.
func (a Approval) Validate() error {
	if !ValidRepoName(a.Name) || a.ExpiresAt.IsZero() || len(a.ApprovedBy) > 512 {
		return errors.New("GitHub approvals need a valid name and an expiry")
	}
	switch a.Action {
	case ActionCreateRepository:
		if a.Visibility != "private" && a.Visibility != "public" || a.Source != "" {
			return errors.New("repository creation approvals need visibility private or public and no source")
		}
	case ActionForkRepository:
		if _, _, ok := SplitRepo(a.Source); !ok || a.Visibility != "" {
			return errors.New("fork approvals need a source owner/name and no visibility")
		}
	default:
		return errors.New("unsupported GitHub approval action")
	}
	return nil
}

func validBranchPattern(b string) bool {
	b = strings.TrimSuffix(b, "/*")
	return branchPart.MatchString(b) && !strings.Contains(b, "..") && !strings.HasPrefix(b, "/") && !strings.HasSuffix(b, "/") && !strings.Contains(b, "//") && !strings.Contains(b, "*")
}

// Parse decodes and validates a policy document. Empty input means GitHub
// access is off and returns nil without error.
func Parse(raw []byte) (*Policy, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil, nil
	}
	if len(raw) > 64<<10 {
		return nil, errors.New("GitHub policy too large")
	}
	var p Policy
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&p); err != nil {
		return nil, errors.New("invalid GitHub policy")
	}
	if d.Decode(new(any)) != io.EOF {
		return nil, errors.New("invalid GitHub policy")
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return &p, nil
}

// RepoAllowed reports whether "owner/name" is one of the policy's repositories.
func (p *Policy) RepoAllowed(full string) bool {
	owner, name, ok := SplitRepo(full)
	if !ok {
		return false
	}
	for _, r := range p.Repositories {
		o, n, _ := strings.Cut(r, "/")
		if strings.EqualFold(o, owner) && (n == "*" || strings.EqualFold(n, name)) {
			return true
		}
	}
	return false
}

// BranchProtected reports whether a push may not touch branch. The default
// branch is always protected; an unknown default branch protects everything.
func (p *Policy) BranchProtected(branch, defaultBranch string) bool {
	if defaultBranch == "" || branch == defaultBranch {
		return true
	}
	if len(p.PushBranches) > 0 && !matchBranch(p.PushBranches, branch) {
		return true
	}
	return matchBranch(p.ProtectedBranches, branch)
}

func matchBranch(patterns []string, branch string) bool {
	for _, pattern := range patterns {
		if prefix, ok := strings.CutSuffix(pattern, "/*"); ok {
			if strings.HasPrefix(branch, prefix+"/") {
				return true
			}
		} else if branch == pattern {
			return true
		}
	}
	return false
}

// ApprovalRequest is the canonical form of an action that needs approval. It
// is what the operator approves and what the proxy matches.
type ApprovalRequest struct {
	Action     string `json:"action"`
	Name       string `json:"name"`
	Visibility string `json:"visibility,omitempty"`
	Source     string `json:"source,omitempty"`
}

// Match returns the live approval for req, if any. Names compare
// case-insensitively, like GitHub's.
func (p *Policy) Match(req ApprovalRequest, now time.Time) (Approval, bool) {
	for _, a := range p.Approvals {
		if a.Action == req.Action && strings.EqualFold(a.Name, req.Name) && a.Visibility == req.Visibility && strings.EqualFold(a.Source, req.Source) && now.Before(a.ExpiresAt) {
			return a, true
		}
	}
	return Approval{}, false
}

// Key identifies one approval so it can be consumed once.
func (a Approval) Key() string {
	return strings.Join([]string{a.Action, strings.ToLower(a.Name), a.Visibility, strings.ToLower(a.Source), a.ExpiresAt.UTC().Format(time.RFC3339)}, "|")
}
