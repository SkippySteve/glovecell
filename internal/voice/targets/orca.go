package targets

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Orca sends tasks through the orca-pilot handoff intake (POST /handoffs on
// :8791, controller bearer token): each task becomes a handoff that clones
// the repository, makes a worktree on a new branch and launches one agent
// (claude or codex) with the brief.
//
// The intake reports preparing, started or failed, and "started" only means
// the agent was launched. Completion comes from either of two places:
//   - the run-record publisher (GET /runs on :8790), when an Orca
//     orchestration run worked on the handoff's branch: the last attempt's
//     outcome and its worker_done summary;
//   - a notify call from the agent naming the task (handled by the service,
//     not here).
type Orca struct {
	name string
	cfg  OrcaConfig
	http *http.Client

	mu   sync.Mutex
	runs map[string]runCache // run ID -> cached record by content hash
}

// OrcaConfig configures one Orca target.
type OrcaConfig struct {
	// Name is the target name, for example "orca" or "codex".
	Name string
	// HandoffURL is the intake origin, for example
	// http://orca-handoff.agent-workspace-orca-pilot.svc.cluster.local:8791.
	HandoffURL string
	// Token is the intake's bearer token (at least 32 bytes).
	Token string
	// RunRecordsURL is the run-record publisher origin; empty disables
	// run-record results.
	RunRecordsURL string
	// Agent is claude or codex.
	Agent string
	// DefaultRepo is used when a task names no repository.
	DefaultRepo string
	// Base is an optional base branch.
	Base string
	// BriefFooter is appended to every brief (for example how to report
	// back through the notify API); {thread} and {task} are replaced.
	BriefFooter string
	Timeout     time.Duration
}

type runCache struct {
	sha    string
	record runRecord
}

var (
	handoffName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	handoffRepo = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*/[A-Za-z0-9_][A-Za-z0-9_.-]*$`)
	runID       = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
)

const (
	handoffMaxBrief = 64 << 10
	handoffMaxActor = 1024
	maxReply        = 4 << 20
)

// NewOrca validates the configuration.
func NewOrca(c OrcaConfig) (*Orca, error) {
	if c.Name == "" {
		c.Name = "orca"
	}
	if c.Agent == "" {
		c.Agent = "claude"
	}
	if c.Agent != "claude" && c.Agent != "codex" {
		return nil, errors.New("orca agent must be claude or codex")
	}
	for i, u := range []string{c.HandoffURL, c.RunRecordsURL} {
		if u == "" && i == 1 {
			continue // run records are optional
		}
		p, err := url.Parse(u)
		if err != nil || (p.Scheme != "http" && p.Scheme != "https") || p.Host == "" || p.User != nil {
			return nil, errors.New("orca URLs must be http(s) origins without credentials")
		}
	}
	if len(strings.TrimSpace(c.Token)) < 32 {
		return nil, errors.New("orca handoff token must be at least 32 bytes")
	}
	if c.DefaultRepo != "" && !validRepo(c.DefaultRepo) {
		return nil, errors.New("orca default repository must be OWNER/NAME")
	}
	if c.Timeout <= 0 {
		c.Timeout = 30 * time.Second
	}
	return &Orca{name: c.Name, cfg: c, runs: map[string]runCache{},
		http: &http.Client{Timeout: c.Timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func validRepo(r string) bool { return handoffRepo.MatchString(r) && !strings.Contains(r, "..") }

func (o *Orca) Name() string { return o.name }

type handoffRequest struct {
	Repo  string `json:"repo"`
	Name  string `json:"name"`
	Agent string `json:"agent"`
	Base  string `json:"base,omitempty"`
	Brief string `json:"brief"`
	Actor string `json:"actor"`
}

type handoffRecord struct {
	Name      string `json:"name"`
	Repo      string `json:"repo"`
	Agent     string `json:"agent"`
	Status    string `json:"status"` // preparing, started or failed
	Step      string `json:"step,omitempty"`
	Error     string `json:"error,omitempty"`
	Worktree  string `json:"worktree,omitempty"`
	Branch    string `json:"branch,omitempty"`
	Handle    string `json:"handle,omitempty"`
	StartedAt string `json:"startedAt,omitempty"`
}

func (o *Orca) call(ctx context.Context, method, u string, body any, auth bool, out any) (int, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if auth {
		req.Header.Set("Authorization", "Bearer "+o.cfg.Token)
	}
	resp, err := o.http.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return 0, fmt.Errorf("orca unreachable: %w", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxReply))
	if err != nil {
		return resp.StatusCode, err
	}
	if resp.StatusCode/100 != 2 && resp.StatusCode != http.StatusConflict {
		msg := strings.TrimSpace(string(b))
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return resp.StatusCode, fmt.Errorf("orca returned HTTP %d: %s", resp.StatusCode, msg)
	}
	if out != nil && len(b) > 0 {
		if err := json.Unmarshal(b, out); err != nil {
			return resp.StatusCode, errors.New("orca returned unreadable JSON")
		}
	}
	return resp.StatusCode, nil
}

// HandoffName is the handoff name for a task: stable per task, so a retry
// after a restart finds the existing handoff (409) instead of starting a
// second agent.
func HandoffName(prefix string, taskID int64) string {
	n := fmt.Sprintf("voice-%s-%d", prefix, taskID)
	if !handoffName.MatchString(n) {
		var b [4]byte
		_, _ = rand.Read(b[:])
		n = "voice-" + hex.EncodeToString(b[:])
	}
	return n
}

// Send creates the handoff. Its name is the task's ID at this target.
func (o *Orca) Send(ctx context.Context, t Task) (string, error) {
	repo := t.Repo
	if repo == "" {
		repo = o.cfg.DefaultRepo
	}
	if !validRepo(repo) {
		return "", errors.New("orca needs a repository: set a default for the room or name one")
	}
	brief := buildBrief(t, o.cfg.BriefFooter)
	if len(brief) > handoffMaxBrief {
		return "", errors.New("request too long for an Orca handoff")
	}
	actor := t.Actor
	if actor == "" {
		actor = "glovecell-voice"
	}
	if len(actor) > handoffMaxActor {
		actor = actor[:handoffMaxActor]
	}
	name := HandoffName(o.name, t.ID)
	req := handoffRequest{Repo: repo, Name: name, Agent: o.cfg.Agent, Base: o.cfg.Base, Brief: brief, Actor: "glovecell-voice:" + actor}
	var rec handoffRecord
	code, err := o.call(ctx, http.MethodPost, strings.TrimSuffix(o.cfg.HandoffURL, "/")+"/handoffs", req, true, &rec)
	if err != nil {
		return "", err
	}
	if code == http.StatusConflict && rec.Repo != "" && rec.Repo != repo {
		return "", fmt.Errorf("handoff %s already exists for another repository", name)
	}
	return name, nil
}

func buildBrief(t Task, footer string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Voice task %d from Steve, sent through glovecell-voice.\n", t.ID)
	b.WriteString("The request below was transcribed from speech and may contain recognition errors; ")
	b.WriteString("if something is ambiguous, pick the most sensible reading and say what you assumed.\n\n")
	b.WriteString(strings.TrimSpace(t.Brief))
	b.WriteString("\n\nWork through a pull request as usual; never merge. ")
	b.WriteString("Finish with a short plain-language summary (two to five sentences) of what you did and anything Steve must do; it will be read aloud to him.\n")
	if footer = strings.TrimSpace(footer); footer != "" {
		footer = strings.NewReplacer("{thread}", t.Thread, "{task}", strconv.FormatInt(t.ID, 10)).Replace(footer)
		b.WriteString("\n" + footer + "\n")
	}
	return b.String()
}

func (o *Orca) handoff(ctx context.Context, id string) (handoffRecord, error) {
	if !handoffName.MatchString(id) {
		return handoffRecord{}, errors.New("invalid handoff name")
	}
	var rec handoffRecord
	_, err := o.call(ctx, http.MethodGet, strings.TrimSuffix(o.cfg.HandoffURL, "/")+"/handoffs/"+id, nil, true, &rec)
	return rec, err
}

// Status maps the handoff and, once the agent started, a matching run
// record.
func (o *Orca) Status(ctx context.Context, id string) (Status, error) {
	rec, err := o.handoff(ctx, id)
	if err != nil {
		return Status{}, err
	}
	switch rec.Status {
	case "preparing":
		return Status{State: StatePending, Detail: "preparing: " + rec.Step}, nil
	case "failed":
		return Status{State: StateFailed, Detail: fmt.Sprintf("handoff failed at %s: %s", rec.Step, rec.Error)}, nil
	case "started":
		if r, ok := o.runResult(ctx, rec.Branch); ok {
			return Status{State: r.State, Detail: "run record"}, nil
		}
		return Status{State: StateRunning, Detail: fmt.Sprintf("%s agent working on %s", rec.Agent, rec.Branch)}, nil
	}
	return Status{State: StatePending, Detail: rec.Status}, nil
}

// Result returns the outcome once known.
func (o *Orca) Result(ctx context.Context, id string) (Result, error) {
	rec, err := o.handoff(ctx, id)
	if err != nil {
		return Result{}, err
	}
	if rec.Status == "failed" {
		return Result{State: StateFailed, Text: fmt.Sprintf("Orca couldn't start the agent (%s): %s", rec.Step, rec.Error)}, nil
	}
	if r, ok := o.runResult(ctx, rec.Branch); ok {
		return r, nil
	}
	return Result{State: StateRunning}, nil
}

type publishedRun struct {
	ID     string `json:"id"`
	SHA256 string `json:"sha256"`
}

type runRecord struct {
	Tasks []struct {
		Status   string `json:"status"`
		Attempts []struct {
			Outcome   string `json:"outcome,omitempty"`
			LastError string `json:"lastError,omitempty"`
			Summary   string `json:"summary,omitempty"`
		} `json:"attempts"`
		Git *struct {
			Branch string `json:"branch"`
		} `json:"git,omitempty"`
	} `json:"tasks"`
}

// runResult looks for a finished run-record task on branch. Records are
// cached by content hash, so a poll costs one list request plus changed
// records only.
func (o *Orca) runResult(ctx context.Context, branch string) (Result, bool) {
	if o.cfg.RunRecordsURL == "" || branch == "" {
		return Result{}, false
	}
	base := strings.TrimSuffix(o.cfg.RunRecordsURL, "/")
	var runs []publishedRun
	if _, err := o.call(ctx, http.MethodGet, base+"/runs", nil, false, &runs); err != nil {
		return Result{}, false
	}
	for _, pr := range runs {
		if !runID.MatchString(pr.ID) {
			continue
		}
		o.mu.Lock()
		cached, ok := o.runs[pr.ID]
		o.mu.Unlock()
		if !ok || cached.sha != pr.SHA256 {
			var rec runRecord
			if _, err := o.call(ctx, http.MethodGet, base+"/runs/"+pr.ID, nil, false, &rec); err != nil {
				continue
			}
			cached = runCache{sha: pr.SHA256, record: rec}
			o.mu.Lock()
			o.runs[pr.ID] = cached
			o.mu.Unlock()
		}
		for _, t := range cached.record.Tasks {
			if t.Git == nil || t.Git.Branch != branch || len(t.Attempts) == 0 {
				continue
			}
			last := t.Attempts[len(t.Attempts)-1]
			switch last.Outcome {
			case "succeeded":
				return Result{State: StateSucceeded, Text: last.Summary}, true
			case "failed":
				text := last.Summary
				if text == "" {
					text = last.LastError
				}
				return Result{State: StateFailed, Text: text}, true
			}
		}
	}
	return Result{}, false
}
