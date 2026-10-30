// Package notify is glovecell-voice's agent-facing HTTP API:
//
//	POST /v1/notify
//	Authorization: Bearer <caller token>
//	{"kind": "message" | "question" | "approval", "text": "...",
//	 "task": 12, "final": true,              (message: report on a task)
//	 "repo": "owner/name", "pr": 459}        (approval: request a card)
//
// Each caller (an agent cell or orchestrator) has its own token. The service
// stores only SHA-256 hashes of tokens, from a mounted Secret. message and
// question become a text plus voice note to Steve, marked with the caller's
// name. approval only asks the gate to post a card for a PR; the gate reads
// the PR itself, and the response never contains the code. Nothing here can
// approve or merge.
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
	"regexp"
	"slices"
	"strings"
	"time"

	"golang.org/x/time/rate"

	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/approvalgate"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/voice/service"
)

// Kinds.
const (
	KindMessage  = "message"
	KindQuestion = "question"
	KindApproval = "approval"
)

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
	// Kinds the caller may use; empty means message and question.
	Kinds []string `json:"kinds,omitempty"`
	// Room is the caller's room ID; empty means the Inbox room.
	Room string `json:"room,omitempty"`
	// Target is the agent target the caller speaks for.
	Target string `json:"target,omitempty"`
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
			if k != KindMessage && k != KindQuestion && k != KindApproval {
				return fmt.Errorf("notify caller %s: unknown kind %q", c.Name, k)
			}
		}
	}
	return nil
}

// Backend is what notify calls; service.Service implements it.
type Backend interface {
	NotifyMessage(ctx context.Context, c service.Caller, kind, text string, task int64, final bool) (string, error)
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
			c.Kinds = []string{KindMessage, KindQuestion}
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
	Kind  string `json:"kind"`
	Text  string `json:"text"`
	Task  int64  `json:"task,omitempty"`
	Final bool   `json:"final,omitempty"`
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
	if len(req.Text) > MaxText {
		reply(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "text too long"})
		outcome("bad_request")
		return
	}
	sc := service.Caller{Name: c.cfg.Name, Room: c.cfg.Room, Target: c.cfg.Target}
	ctx := r.Context()
	switch req.Kind {
	case KindMessage, KindQuestion:
		if req.Text == "" || req.Repo != "" || req.PR != 0 {
			reply(w, http.StatusBadRequest, map[string]string{"error": "message and question need text and no repo or pr"})
			outcome("bad_request")
			return
		}
		k := req.Kind
		if req.Final && req.Failed {
			k = "failure"
		}
		_, err := h.backend.NotifyMessage(ctx, sc, k, req.Text, req.Task, req.Final)
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
			reply(w, http.StatusAccepted, map[string]string{"status": "delivered"})
			outcome("delivered")
		}
	case KindApproval:
		if !approvalgate.ValidRepo(req.Repo) || req.PR <= 0 || req.Task != 0 || req.Final {
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
			reply(w, http.StatusAccepted, map[string]string{"status": "card posted; Steve decides"})
			outcome("card")
		}
	default:
		reply(w, http.StatusBadRequest, map[string]string{"error": "unknown kind"})
		outcome("bad_request")
	}
}
