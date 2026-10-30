package approvalgate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// The controller's internal/forge.Forge (from #90) grants and revokes
// repository access for cells; it has no pull request, status or merge
// operations, so the gate has its own small Forgejo interface. The client
// follows the same conventions as internal/forgejo: an HTTPS origin, a
// token that never appears in errors, no redirects.

// PR is a pull request as the gate needs it.
type PR struct {
	Repo         string
	Number       int
	Title        string
	Body         string
	State        string // open or closed
	Merged       bool
	Mergeable    bool
	HeadSHA      string
	HeadRef      string
	BaseRef      string
	Author       string
	URL          string
	Additions    int
	Deletions    int
	ChangedFiles int
}

// File is one changed file.
type File struct {
	Name      string
	Status    string
	Additions int
	Deletions int
}

// Check is one commit status (Forgejo Actions report through these).
type Check struct {
	Context     string
	State       string // pending, success, error, failure, warning
	Description string
	URL         string
}

// Forge is what the gate needs from Forgejo, as the approval-gate user.
type Forge interface {
	PullRequest(ctx context.Context, repo string, number int) (PR, error)
	Files(ctx context.Context, repo string, number int) ([]File, error)
	Checks(ctx context.Context, repo, sha string) ([]Check, error)
	SetStatus(ctx context.Context, repo, sha string, c Check) error
	// Merge merges with a merge commit, only if the head is still sha.
	Merge(ctx context.Context, repo string, number int, sha, title, message string) error
	Comment(ctx context.Context, repo string, number int, body string) error
}

// ErrHeadChanged reports a merge refused because the head moved.
var ErrHeadChanged = errors.New("pull request head changed")

// ErrNotMergeable reports a merge Forgejo refused.
var ErrNotMergeable = errors.New("pull request is not mergeable")

var repoPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,99}/[A-Za-z0-9_][A-Za-z0-9_.-]{0,99}$`)

// ValidRepo reports an OWNER/NAME repository.
func ValidRepo(r string) bool { return repoPattern.MatchString(r) && !strings.Contains(r, "..") }

// Forgejo is the Forge over the Forgejo API.
type Forgejo struct {
	Origin string
	token  string
	HTTP   *http.Client
}

var _ Forge = (*Forgejo)(nil)

// NewForgejo returns a client for origin with the approval-gate token.
func NewForgejo(origin, token string) (*Forgejo, error) {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" {
		return nil, errors.New("forgejo origin must be an HTTPS origin")
	}
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("approval-gate token is empty")
	}
	return &Forgejo{Origin: strings.TrimSuffix(origin, "/"), token: strings.TrimSpace(token),
		HTTP: &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

type apiError struct {
	Status int
	Msg    string
}

func (e apiError) Error() string {
	if e.Msg != "" {
		return fmt.Sprintf("Forgejo returned HTTP %d: %s", e.Status, e.Msg)
	}
	return fmt.Sprintf("Forgejo returned HTTP %d", e.Status)
}

func (f *Forgejo) call(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, f.Origin+"/api/v1"+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "token "+f.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := f.HTTP.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return fmt.Errorf("forgejo unreachable: %w", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		var m struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(b, &m)
		msg := m.Message
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return apiError{Status: resp.StatusCode, Msg: msg}
	}
	if out != nil && len(b) > 0 {
		return json.Unmarshal(b, out)
	}
	return nil
}

func repoPath(repo string) (string, error) {
	if !ValidRepo(repo) {
		return "", errors.New("invalid repository")
	}
	owner, name, _ := strings.Cut(repo, "/")
	return "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name), nil
}

func (f *Forgejo) PullRequest(ctx context.Context, repo string, n int) (PR, error) {
	p, err := repoPath(repo)
	if err != nil {
		return PR{}, err
	}
	var raw struct {
		Number       int    `json:"number"`
		Title        string `json:"title"`
		Body         string `json:"body"`
		State        string `json:"state"`
		Merged       bool   `json:"merged"`
		Mergeable    bool   `json:"mergeable"`
		HTMLURL      string `json:"html_url"`
		Additions    int    `json:"additions"`
		Deletions    int    `json:"deletions"`
		ChangedFiles int    `json:"changed_files"`
		User         struct {
			Login string `json:"login"`
		} `json:"user"`
		Head struct {
			SHA string `json:"sha"`
			Ref string `json:"ref"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
		} `json:"base"`
	}
	if err := f.call(ctx, http.MethodGet, fmt.Sprintf("%s/pulls/%d", p, n), nil, &raw); err != nil {
		return PR{}, err
	}
	return PR{Repo: repo, Number: raw.Number, Title: raw.Title, Body: raw.Body, State: raw.State, Merged: raw.Merged, Mergeable: raw.Mergeable,
		HeadSHA: raw.Head.SHA, HeadRef: raw.Head.Ref, BaseRef: raw.Base.Ref, Author: raw.User.Login, URL: raw.HTMLURL,
		Additions: raw.Additions, Deletions: raw.Deletions, ChangedFiles: raw.ChangedFiles}, nil
}

func (f *Forgejo) Files(ctx context.Context, repo string, n int) ([]File, error) {
	p, err := repoPath(repo)
	if err != nil {
		return nil, err
	}
	var out []File
	for page := 1; page <= 10; page++ {
		var raw []struct {
			Filename  string `json:"filename"`
			Status    string `json:"status"`
			Additions int    `json:"additions"`
			Deletions int    `json:"deletions"`
		}
		if err := f.call(ctx, http.MethodGet, fmt.Sprintf("%s/pulls/%d/files?limit=50&page=%d", p, n, page), nil, &raw); err != nil {
			return nil, err
		}
		for _, r := range raw {
			out = append(out, File{Name: r.Filename, Status: r.Status, Additions: r.Additions, Deletions: r.Deletions})
		}
		if len(raw) < 50 {
			break
		}
	}
	return out, nil
}

func (f *Forgejo) Checks(ctx context.Context, repo, sha string) ([]Check, error) {
	p, err := repoPath(repo)
	if err != nil {
		return nil, err
	}
	var raw struct {
		Statuses []struct {
			Context     string `json:"context"`
			Status      string `json:"status"`
			State       string `json:"state"`
			Description string `json:"description"`
			TargetURL   string `json:"target_url"`
		} `json:"statuses"`
	}
	if err := f.call(ctx, http.MethodGet, fmt.Sprintf("%s/commits/%s/status?limit=100", p, url.PathEscape(sha)), nil, &raw); err != nil {
		return nil, err
	}
	var out []Check
	for _, s := range raw.Statuses {
		state := s.Status
		if state == "" {
			state = s.State
		}
		out = append(out, Check{Context: s.Context, State: state, Description: s.Description, URL: s.TargetURL})
	}
	return out, nil
}

func (f *Forgejo) SetStatus(ctx context.Context, repo, sha string, c Check) error {
	p, err := repoPath(repo)
	if err != nil {
		return err
	}
	body := map[string]string{"state": c.State, "context": c.Context, "description": truncate(c.Description, 140)}
	if c.URL != "" {
		body["target_url"] = c.URL
	}
	return f.call(ctx, http.MethodPost, fmt.Sprintf("%s/statuses/%s", p, url.PathEscape(sha)), body, nil)
}

func (f *Forgejo) Merge(ctx context.Context, repo string, n int, sha, title, message string) error {
	p, err := repoPath(repo)
	if err != nil {
		return err
	}
	body := map[string]any{"Do": "merge", "MergeTitleField": title, "MergeMessageField": message, "head_commit_id": sha, "delete_branch_after_merge": false}
	err = f.call(ctx, http.MethodPost, fmt.Sprintf("%s/pulls/%d/merge", p, n), body, nil)
	var ae apiError
	if errors.As(err, &ae) {
		switch ae.Status {
		case http.StatusConflict:
			return fmt.Errorf("%w: %v", ErrHeadChanged, err)
		case http.StatusMethodNotAllowed, http.StatusUnprocessableEntity:
			return fmt.Errorf("%w: %v", ErrNotMergeable, err)
		}
	}
	return err
}

func (f *Forgejo) Comment(ctx context.Context, repo string, n int, body string) error {
	p, err := repoPath(repo)
	if err != nil {
		return err
	}
	return f.call(ctx, http.MethodPost, fmt.Sprintf("%s/issues/%d/comments", p, n), map[string]string{"body": body}, nil)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && (s[n]&0xC0) == 0x80 {
		n--
	}
	return s[:n] + "…"
}
