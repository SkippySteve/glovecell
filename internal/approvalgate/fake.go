package approvalgate

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// FakeForge is an in-memory Forge for tests.
type FakeForge struct {
	mu       sync.Mutex
	PRs      map[string]*PR // "owner/repo#n"
	FileList map[string][]File
	Statuses map[string][]Check // sha -> checks (latest per context)
	Merged   []string
	Comments []string
	// Approvals are "repo#n@sha" reviews; Merge requires one, as branch
	// protection does.
	Approvals  []string
	ApproveErr error
	MergeErr   error
	Down       bool
}

var _ Forge = (*FakeForge)(nil)

// NewFakeForge returns an empty fake.
func NewFakeForge() *FakeForge {
	return &FakeForge{PRs: map[string]*PR{}, FileList: map[string][]File{}, Statuses: map[string][]Check{}}
}

func key(repo string, n int) string { return fmt.Sprintf("%s#%d", repo, n) }

// AddPR adds an open, mergeable PR with green CI.
func (f *FakeForge) AddPR(repo string, n int, sha, title string) *PR {
	f.mu.Lock()
	defer f.mu.Unlock()
	pr := &PR{Repo: repo, Number: n, Title: title, State: "open", Mergeable: true, HeadSHA: sha, HeadRef: "task/x", BaseRef: "main", Author: "orca-agent",
		URL: "https://git.example.com/" + repo + "/pulls/" + fmt.Sprint(n), Additions: 10, Deletions: 2,
		Body: "## Risk\nLow.\n\n## Rollback\nRevert the PR.\n\nTwo-way door."}
	f.PRs[key(repo, n)] = pr
	f.FileList[key(repo, n)] = []File{{Name: "internal/x.go", Status: "modified", Additions: 10, Deletions: 2}}
	f.Statuses[sha] = []Check{{Context: "CI / checks (pull_request)", State: "success"}}
	return pr
}

// SetChecks replaces the checks on sha.
func (f *FakeForge) SetChecks(sha string, checks ...Check) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Statuses[sha] = checks
}

// Push moves a PR's head; the new head has green CI.
func (f *FakeForge) Push(repo string, n int, sha string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.PRs[key(repo, n)].HeadSHA = sha
	f.Statuses[sha] = []Check{{Context: "CI / checks (pull_request)", State: "success"}}
}

// Update changes a PR under the lock.
func (f *FakeForge) Update(repo string, n int, fn func(*PR)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f.PRs[key(repo, n)])
}

func (f *FakeForge) get(repo string, n int) (*PR, error) {
	if f.Down {
		return nil, errors.New("forgejo down")
	}
	pr, ok := f.PRs[key(repo, n)]
	if !ok {
		return nil, errors.New("Forgejo returned HTTP 404")
	}
	return pr, nil
}

func (f *FakeForge) PullRequest(_ context.Context, repo string, n int) (PR, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	pr, err := f.get(repo, n)
	if err != nil {
		return PR{}, err
	}
	return *pr, nil
}

func (f *FakeForge) Files(_ context.Context, repo string, n int) ([]File, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, err := f.get(repo, n); err != nil {
		return nil, err
	}
	return append([]File(nil), f.FileList[key(repo, n)]...), nil
}

func (f *FakeForge) Checks(_ context.Context, _ string, sha string) ([]Check, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Down {
		return nil, errors.New("forgejo down")
	}
	return append([]Check(nil), f.Statuses[sha]...), nil
}

func (f *FakeForge) SetStatus(_ context.Context, _ string, sha string, c Check) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Down {
		return errors.New("forgejo down")
	}
	list := f.Statuses[sha]
	for i := range list {
		if list[i].Context == c.Context {
			list[i] = c
			return nil
		}
	}
	f.Statuses[sha] = append(list, c)
	return nil
}

// Status returns the state of one status context on sha.
func (f *FakeForge) Status(sha, name string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.Statuses[sha] {
		if c.Context == name {
			return c.State
		}
	}
	return ""
}

func (f *FakeForge) Merge(_ context.Context, repo string, n int, sha, _, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	pr, err := f.get(repo, n)
	if err != nil {
		return err
	}
	if f.MergeErr != nil {
		return f.MergeErr
	}
	if pr.HeadSHA != sha {
		return ErrHeadChanged
	}
	approved := false
	for _, a := range f.Approvals {
		approved = approved || a == key(repo, n)+"@"+sha
	}
	if !approved {
		return errors.New("Forgejo returned HTTP 405: Does not have enough approvals")
	}
	if !pr.Mergeable || pr.Merged {
		return ErrNotMergeable
	}
	pr.Merged, pr.State = true, "closed"
	f.Merged = append(f.Merged, key(repo, n)+"@"+sha)
	return nil
}

func (f *FakeForge) Approve(_ context.Context, repo string, n int, sha, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, err := f.get(repo, n); err != nil {
		return err
	}
	if f.ApproveErr != nil {
		return f.ApproveErr
	}
	f.Approvals = append(f.Approvals, key(repo, n)+"@"+sha)
	return nil
}

func (f *FakeForge) Comment(_ context.Context, repo string, n int, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Down {
		return errors.New("forgejo down")
	}
	f.Comments = append(f.Comments, key(repo, n)+": "+body)
	return nil
}

// MergedCount returns how many merges happened.
func (f *FakeForge) MergedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.Merged)
}
