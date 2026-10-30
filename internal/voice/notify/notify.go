// Package notify is glovecell-voice's agent-facing HTTP API:
//
//	POST /v1/notify
//	Authorization: Bearer <caller token>
//	{"kind": "message" | "question" | "blocked" | "done", "text": "...",
//	 "room": "<room ID or name>", "thread": "<event ID>",
//	 "task": 12, "final": true, "failed": false}
//	{"kind": "approval", "repo": "owner/name", "pr": 459, "text": "..."}
//
// This is the shape orca-pilot voice send posts. Each caller (an agent cell
// or orchestrator) has its own token: from a directory of token files (one
// file per caller, as the cluster Secret mounts them; only their SHA-256
// is kept in memory) or from a JSON file of hashes. Notes become a text
// plus voice note to Steve, marked with the caller's name; "done" in the
// thread of a task the caller speaks for finishes it. approval only asks
// the gate to post a card for a PR; the gate reads the PR itself, and the
// response never contains the code. Nothing here can approve or merge.
package notify

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/time/rate"

	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/approvalgate"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/voice/service"
)

// Kinds.
const (
	KindMessage  = "message"
	KindQuestion = "question"
	KindBlocked  = "blocked"
	KindDone     = "done"
	KindApproval = "approval"
)

// AllKinds are every kind a caller may be allowed.
var AllKinds = []string{KindMessage, KindQuestion, KindBlocked, KindDone, KindApproval}

// noteKinds are what a caller without an explicit list may send.
var noteKinds = []string{KindMessage, KindQuestion, KindBlocked, KindDone}

// Limits.
const (
	MaxBody = 16 << 10
	MaxText = 4000
)

// CallerConfig is one caller in the callers file.
type CallerConfig struct {
	Name string `json:"name"`
	// TokenSHA256 is the hex SHA-256 of the caller's bearer token.
	TokenSHA256 string `json:"tokenSHA256"`
	// Kinds the caller may use; empty means message, question, blocked and
	// done (no approval requests).
	Kinds []string `json:"kinds,omitempty"`
	// Room is the caller's room ID; empty means the Inbox room.
	Room string `json:"room,omitempty"`
	// Targets are the agent targets the caller speaks for.
	Targets []string `json:"targets,omitempty"`
	// PerHour bounds the caller's requests (default 60; approvals 20).
	PerHour int `json:"perHour,omitempty"`
}

var (
	callerName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	hexHash    = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// LoadCallers reads the callers file (JSON array). Errors never include
// file contents.
func LoadCallers(path string) ([]CallerConfig, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.New("notify callers file unreadable")
	}
	var callers []CallerConfig
	if err := json.Unmarshal(b, &callers); err != nil {
		return nil, errors.New("notify callers file is not a JSON array of callers")
	}
	return callers, ValidateCallers(callers)
}

// ValidateCallers checks names, hashes and kinds.
func ValidateCallers(callers []CallerConfig) error {
	seen := map[string]bool{}
	for i, c := range callers {
		if !callerName.MatchString(c.Name) || seen[c.Name] {
			return fmt.Errorf("notify caller %d: name must be a unique lowercase slug", i)
		}
		seen[c.Name] = true
		if !hexHash.MatchString(c.TokenSHA256) {
			return fmt.Errorf("notify caller %s: tokenSHA256 must be 64 lowercase hex characters (a hash, never the token)", c.Name)
		}
		for _, k := range c.Kinds {
			if !slices.Contains(AllKinds, k) {
				return fmt.Errorf("notify caller %s: unknown kind %q", c.Name, k)
			}
		}
	}
	return nil
}

// LoadTokenDir reads one plain token per file (the file name is the caller
// name), as a Secret volume mounts them, and keeps only their hashes.
// kinds applies to every caller (nil means all kinds: an orchestrator may
// request approval cards, which can never approve); targets maps caller
// names to the targets they speak for.
func LoadTokenDir(dir string, kinds []string, targets map[string][]string) ([]CallerConfig, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, errors.New("notify token directory unreadable")
	}
	if kinds == nil {
		kinds = AllKinds
	}
	var out []CallerConfig
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") || e.IsDir() {
			continue // the Secret volume's ..data links
		}
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("notify token %s unreadable", name)
		}
		token := strings.TrimSpace(string(b))
		if len(token) < 32 {
			return nil, fmt.Errorf("notify token %s is shorter than 32 characters", name)
		}
		out = append(out, CallerConfig{Name: name, TokenSHA256: HashToken(token), Kinds: kinds, Targets: targets[name]})
	}
	return out, ValidateCallers(out)
}

// ParseTargets reads "caller=target|target,caller2=target".
func ParseTargets(s string) (map[string][]string, error) {
	out := map[string][]string{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, list, ok := strings.Cut(part, "=")
		if !ok || !callerName.MatchString(strings.TrimSpace(name)) {
			return nil, fmt.Errorf("notify caller targets: %q is not caller=target|target", part)
		}
		for _, t := range strings.Split(list, "|") {
			if t = strings.TrimSpace(t); t != "" {
				out[strings.TrimSpace(name)] = append(out[strings.TrimSpace(name)], t)
			}
		}
	}
	return out, nil
}

// Backend is what notify calls; service.Service implements it.
type Backend interface {
	NotifyMessage(ctx context.Context, c service.Caller, n service.Note) (string, error)
	RequestCard(ctx context.Context, c service.Caller, repo string, pr int, note string) error
}

type caller struct {
	cfg      CallerConfig
	hash     []byte
	limiter  *rate.Limiter
	approval *rate.Limiter
}

// Handler serves POST /v1/notify.
type Handler struct {
	backend Backend
	callers []*caller
	// Requests counts outcomes for metrics (kind/outcome).
	OnRequest func(caller, kind, outcome string)
}

// New builds the handler.
func New(b Backend, callers []CallerConfig) (*Handler, error) {
	if err := ValidateCallers(callers); err != nil {
		return nil, err
	}
	h := &Handler{backend: b}
	for _, c := range callers {
		hash, _ := hex.DecodeString(c.TokenSHA256)
		per := c.PerHour
		if per <= 0 {
			per = 60
		}
		if len(c.Kinds) == 0 {
			c.Kinds = noteKinds
		}
		h.callers = append(h.callers, &caller{cfg: c, hash: hash,
			limiter:  rate.NewLimiter(rate.Every(time.Hour/time.Duration(per)), max(1, per/6)),
			approval: rate.NewLimiter(rate.Every(time.Hour/20), 5)})
	}
	return h, nil
}

// HashToken returns the hex SHA-256 to put in the callers file.
func HashToken(token string) string {
	s := sha256.Sum256([]byte(token))
	return hex.EncodeToString(s[:])
}

func (h *Handler) authenticate(r *http.Request) *caller {
	auth := r.Header.Get("Authorization")
	token, ok := strings.CutPrefix(auth, "Bearer ")
	if !ok || len(token) < 32 {
		return nil
	}
	sum := sha256.Sum256([]byte(token))
	var found *caller
	// Compare against every caller in constant time.
	for _, c := range h.callers {
		if subtle.ConstantTimeCompare(sum[:], c.hash) == 1 {
			found = c
		}
	}
	return found
}

// Request is the notify body.
type Request struct {
	Kind   string `json:"kind"`
	Text   string `json:"text"`
	Room   string `json:"room,omitempty"`
	Thread string `json:"thread,omitempty"`
	Task   int64  `json:"task,omitempty"`
	Final  bool   `json:"final,omitempty"`
	// Failed marks a final task report as a failure.
	Failed bool   `json:"failed,omitempty"`
	Repo   string `json:"repo,omitempty"`
	PR     int    `json:"pr,omitempty"`
}

func reply(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	name, kind := "", ""
	outcome := func(o string) {
		if h.OnRequest != nil {
			h.OnRequest(name, kind, o)
		}
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		reply(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}
	c := h.authenticate(r)
	if c == nil {
		w.Header().Set("WWW-Authenticate", "Bearer")
		reply(w, http.StatusUnauthorized, map[string]string{"error": "caller token required"})
		outcome("unauthorized")
		return
	}
	name = c.cfg.Name
	if !c.limiter.Allow() {
		w.Header().Set("Retry-After", "60")
		reply(w, http.StatusTooManyRequests, map[string]string{"error": "rate limited"})
		outcome("rate_limited")
		return
	}
	var req Request
	dec := json.NewDecoder(io.LimitReader(r.Body, MaxBody+1))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		reply(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		outcome("bad_request")
		return
	}
	kind = req.Kind
	if !slices.Contains(c.cfg.Kinds, req.Kind) {
		reply(w, http.StatusForbidden, map[string]string{"error": "kind not allowed for this caller"})
		outcome("forbidden")
		return
	}
	req.Text = strings.TrimSpace(req.Text)
	if utf8.RuneCountInString(req.Text) > MaxText || !utf8.ValidString(req.Text) {
		reply(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "text too long"})
		outcome("bad_request")
		return
	}
	if len(req.Room) > 255 || len(req.Thread) > 255 || strings.ContainsFunc(req.Room+req.Thread, func(r rune) bool { return r <= ' ' || r == 0x7f }) {
		reply(w, http.StatusBadRequest, map[string]string{"error": "room and thread must be one word of at most 255 characters"})
		outcome("bad_request")
		return
	}
	sc := service.Caller{Name: c.cfg.Name, Room: c.cfg.Room, Targets: c.cfg.Targets}
	ctx := r.Context()
	switch req.Kind {
	case KindMessage, KindQuestion, KindBlocked, KindDone:
		if req.Text == "" || req.Repo != "" || req.PR != 0 {
			reply(w, http.StatusBadRequest, map[string]string{"error": "notes need text and no repo or pr"})
			outcome("bad_request")
			return
		}
		id, err := h.backend.NotifyMessage(ctx, sc, service.Note{Kind: req.Kind, Text: req.Text, Room: req.Room, Thread: req.Thread, Task: req.Task, Final: req.Final, Failed: req.Failed})
		switch {
		case errors.Is(err, service.ErrUnknownTask):
			reply(w, http.StatusNotFound, map[string]string{"error": "no such task for this caller"})
			outcome("not_found")
		case errors.Is(err, service.ErrBadRequest):
			reply(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			outcome("bad_request")
		case err != nil:
			slog.Warn("notify delivery failed", "caller", c.cfg.Name, "error", err.Error())
			reply(w, http.StatusServiceUnavailable, map[string]string{"error": "not delivered"})
			outcome("error")
		default:
			reply(w, http.StatusAccepted, map[string]string{"id": id, "message": "sent to Steve"})
			outcome("delivered")
		}
	case KindApproval:
		if !approvalgate.ValidRepo(req.Repo) || req.PR <= 0 || req.Task != 0 || req.Final || req.Thread != "" || req.Room != "" {
			reply(w, http.StatusBadRequest, map[string]string{"error": "approval needs repo (OWNER/NAME) and pr"})
			outcome("bad_request")
			return
		}
		if !c.approval.Allow() {
			w.Header().Set("Retry-After", "300")
			reply(w, http.StatusTooManyRequests, map[string]string{"error": "rate limited"})
			outcome("rate_limited")
			return
		}
		err := h.backend.RequestCard(ctx, sc, req.Repo, req.PR, req.Text)
		switch {
		case errors.Is(err, approvalgate.ErrNotAllowed):
			reply(w, http.StatusForbidden, map[string]string{"error": "repository not covered by the approval gate"})
			outcome("forbidden")
		case err != nil:
			slog.Warn("approval card not posted", "caller", c.cfg.Name, "repo", req.Repo, "pr", req.PR, "error", err.Error())
			reply(w, http.StatusServiceUnavailable, map[string]string{"error": "card not posted"})
			outcome("error")
		default:
			// Deliberately no code: only Steve hears it.
			reply(w, http.StatusAccepted, map[string]string{"message": "approval card posted; Steve decides"})
			outcome("card")
		}
	default:
		reply(w, http.StatusBadRequest, map[string]string{"error": "unknown kind"})
		outcome("bad_request")
	}
}
