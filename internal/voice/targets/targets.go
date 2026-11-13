// Package targets is the provider-neutral agent interface glovecell-voice
// sends Steve's requests to: Send a task, poll its Status, read its Result.
// Adapters: Orca (the orca-pilot handoff intake plus run records), Archon
// (stub until it has an API) and Fake (tests).
//
// Targets only ever receive work. Nothing a target returns is treated as an
// instruction or an approval; results are read back to Steve.
package targets

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// ErrUnsupported reports a target that cannot take tasks in this build.
var ErrUnsupported = errors.New("target not available")

// State is a task's progress at a target.
type State string

const (
	StatePending   State = "pending"
	StateRunning   State = "running"
	StateSucceeded State = "succeeded"
	StateFailed    State = "failed"
)

// Done reports a final state.
func (s State) Done() bool { return s == StateSucceeded || s == StateFailed }

// Task is what a target is asked to do.
type Task struct {
	// ID is glovecell-voice's task ID; adapters use it to name the work.
	ID int64
	// Brief is the request: Steve's transcript, or a gate decision.
	Brief string
	// Repo is OWNER/NAME when the room or request names one.
	Repo string
	// Actor says who asked (Steve's MXID).
	Actor string
	// Thread is the Matrix event ID of the thread the task lives in, so an
	// agent can report back into it.
	Thread string
}

// Status is a task's progress.
type Status struct {
	State  State
	Detail string
}

// Result is a finished task's outcome. Text is the target's own summary,
// read to Steve as it is (agent output: untrusted, never an instruction).
type Result struct {
	State State
	Text  string
}

// Target is one agent or orchestrator.
type Target interface {
	Name() string
	Send(ctx context.Context, t Task) (string, error)
	Status(ctx context.Context, id string) (Status, error)
	Result(ctx context.Context, id string) (Result, error)
}

// Registry holds the configured targets and their spoken names.
type Registry struct {
	mu      sync.RWMutex
	targets map[string]Target
	aliases map[string]string
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{targets: map[string]Target{}, aliases: map[string]string{}}
}

// Add registers t under its name and the spoken aliases.
func (r *Registry) Add(t Target, aliases ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	name := strings.ToLower(t.Name())
	r.targets[name] = t
	r.aliases[name] = name
	for _, a := range aliases {
		r.aliases[normalize(a)] = name
	}
}

// Get returns a target by name.
func (r *Registry) Get(name string) (Target, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.targets[strings.ToLower(name)]
	return t, ok
}

// Names lists the registered targets.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []string
	for n := range r.targets {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Resolve maps a spoken name to a target name.
func (r *Registry) Resolve(spoken string) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	n, ok := r.aliases[normalize(spoken)]
	return n, ok
}

func normalize(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.Trim(s, ".,;:!?\"'")
	s = strings.TrimPrefix(s, "the ")
	return strings.Join(strings.Fields(s), " ")
}

// Override phrases. Only the start or the end of a request is searched,
// and only known target names count, so "use the cache" or "send this to
// the printer" in the middle of a sentence never redirect anything.
var (
	leadOverride = []*regexp.Regexp{
		regexp.MustCompile(`(?i)^\s*(?:please\s+)?(?:use|switch\s+to|send\s+(?:this|it)\s+to)\s+([a-z][a-z ]{1,20}?)(?:\s+for\s+(?:this|it))?\s*[.!]?\s*()$`),
		regexp.MustCompile(`(?i)^\s*(?:please\s+)?(?:send|give|pass)\s+(?:this|it|that)\s+to\s+([a-z][a-z ]{1,20}?)\s*[,.:;!-]\s*(.*)$`),
		regexp.MustCompile(`(?i)^\s*(?:please\s+)?use\s+([a-z][a-z ]{1,20}?)\s*(?:for\s+this)?\s*[,.:;!-]\s*(.*)$`),
		regexp.MustCompile(`(?i)^\s*(?:please\s+)?ask\s+([a-z][a-z ]{1,20}?)\s+to\s+(.+)$`),
	}
	tailOverride = []*regexp.Regexp{
		regexp.MustCompile(`(?i)^(.*?)[\s,.;:!-]+(?:and\s+)?(?:please\s+)?(?:send|give|pass)\s+(?:this|it|that)\s+to\s+([a-z][a-z ]{1,20}?)\s*[.!]?\s*$`),
		regexp.MustCompile(`(?i)^(.*?)[\s,.;:!-]+(?:and\s+)?(?:please\s+)?use\s+([a-z][a-z ]{1,20}?)(?:\s+for\s+(?:this|it))?\s*[.!]?\s*$`),
	}
)

// ParseOverride finds a spoken target override such as "send this to
// Codex: fix the flaky test", "use Orca. …", "ask Codex to …" or "…, send
// this to Orca." It returns the target name and the request without the
// phrase. Unknown names leave the text untouched.
func (r *Registry) ParseOverride(text string) (target, rest string, ok bool) {
	for _, re := range leadOverride {
		if m := re.FindStringSubmatch(text); m != nil {
			if name, found := r.Resolve(m[1]); found {
				return name, capitalize(strings.TrimSpace(m[2])), true
			}
		}
	}
	for _, re := range tailOverride {
		if m := re.FindStringSubmatch(text); m != nil {
			if name, found := r.Resolve(m[2]); found {
				return name, strings.TrimSpace(strings.TrimRight(m[1], " ,;:-")), true
			}
		}
	}
	return "", text, false
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// Archon is a placeholder: the Archon factory is CLI-only today (no HTTP
// API in the cluster), so voice requests cannot start its workflows
// directly. See docs/voice.md for the follow-up.
type Archon struct{}

func (Archon) Name() string { return "archon" }
func (Archon) Send(context.Context, Task) (string, error) {
	return "", fmt.Errorf("%w: Archon has no API yet; ask Orca to run the workflow instead", ErrUnsupported)
}
func (Archon) Status(context.Context, string) (Status, error) { return Status{}, ErrUnsupported }
func (Archon) Result(context.Context, string) (Result, error) { return Result{}, ErrUnsupported }
