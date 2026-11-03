// Package store keeps glovecell-voice's records in PostgreSQL: Matrix
// threads and the agent tasks they started, approval cards and their codes,
// the gate's pause switch, and the approval audit. It follows the controller
// store (internal/store, decision D13): goose migrations under an advisory
// lock, verify-full TLS through store.Config, fail-closed writes, and one
// contract test suite run against Memory and Postgres.
//
// The tables use the voice_ prefix and their own migration table, so the
// service can share the controller's database or use its own. The Matrix
// crypto state (mautrix's crypto_* and mx_* tables) lives in the same
// database and is managed by mautrix.
package store

import (
	"context"
	"errors"
	"time"

	apcstore "git.cluster.aimingupward.com/steve/agent-project-controller/internal/store"
)

// Errors are the controller store's, so callers treat both alike.
var (
	ErrUnavailable = apcstore.ErrUnavailable
	ErrNotFound    = apcstore.ErrNotFound
	ErrConflict    = apcstore.ErrConflict
	ErrInvalid     = apcstore.ErrInvalid
)

// Task states.
const (
	TaskReceived  = "received"  // message stored, not yet sent
	TaskRunning   = "running"   // accepted by the target
	TaskSucceeded = "succeeded" // finished; Result holds the outcome
	TaskFailed    = "failed"    // the target or the service failed; Error says why
)

// Thread is one Matrix thread and the agent target it talks to.
type Thread struct {
	RoomID    string    `json:"roomID"`
	RootEvent string    `json:"rootEvent"`
	Target    string    `json:"target"`
	CreatedAt time.Time `json:"createdAt"`
}

// Task is one request to an agent target and its outcome.
type Task struct {
	ID          int64  `json:"id"`
	RoomID      string `json:"roomID"`
	ThreadRoot  string `json:"threadRoot"`
	SourceEvent string `json:"sourceEvent"`
	// Origin is voice, text or gate (a rejection or hold passed back).
	Origin      string    `json:"origin"`
	Target      string    `json:"target"`
	Repo        string    `json:"repo,omitempty"`
	ExternalID  string    `json:"externalID,omitempty"`
	State       string    `json:"state"`
	Transcript  string    `json:"transcript"`
	AudioSHA256 string    `json:"audioSHA256,omitempty"`
	Result      string    `json:"result,omitempty"`
	Error       string    `json:"error,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
	// ResultEventID remembers the text outcome while its voice note retries.
	ResultEventID string `json:"resultEventID,omitempty"`
	// Delivered is set once both the text and voice outcome were posted.
	Delivered bool `json:"delivered"`
}

// Card kinds and states.
const (
	CardPR   = "pr"   // one pull request at one head SHA
	CardList = "list" // a batch of PR cards, each at its SHA

	CardOpen        = "open"        // the code can be used
	CardApproved    = "approved"    // code consumed; merge in progress
	CardMerged      = "merged"      // merged by the gate
	CardFailed      = "failed"      // approved, but the merge failed
	CardRejected    = "rejected"    // Steve rejected it
	CardSuperseded  = "superseded"  // the PR changed; a new card went out
	CardInterrupted = "interrupted" // the service stopped mid-merge; a new card went out
)

// ListItem is one PR in a list card.
type ListItem struct {
	CardID  int64  `json:"cardID"`
	Repo    string `json:"repo"`
	PR      int    `json:"pr"`
	HeadSHA string `json:"headSHA"`
}

// Card is an approval card. The code is bound to Repo, PR and HeadSHA (or
// to Items for a list card).
type Card struct {
	ID          int64      `json:"id"`
	Kind        string     `json:"kind"`
	Code        string     `json:"code"`
	Repo        string     `json:"repo,omitempty"`
	PR          int        `json:"pr,omitempty"`
	HeadSHA     string     `json:"headSHA,omitempty"`
	Title       string     `json:"title,omitempty"`
	Items       []ListItem `json:"items,omitempty"`
	RoomID      string     `json:"roomID"`
	EventID     string     `json:"eventID,omitempty"`
	RequestedBy string     `json:"requestedBy"`
	State       string     `json:"state"`
	Outcome     string     `json:"outcome,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
	ExpiresAt   time.Time  `json:"expiresAt"`
	UsedAt      *time.Time `json:"usedAt,omitempty"`
}

// Audit decisions.
const (
	DecisionMerged      = "merged"
	DecisionRefused     = "refused"
	DecisionMergeFailed = "merge_failed"
	DecisionRejected    = "rejected"
	DecisionHeld        = "held"
	DecisionInfo        = "info"
	DecisionPaused      = "paused"
	DecisionResumed     = "resumed"
	DecisionRevert      = "revert_requested"
	DecisionCard        = "card"
	DecisionInterrupted = "interrupted"
)

// AuditEntry is one gate decision, kept forever.
type AuditEntry struct {
	ID          int64     `json:"id"`
	Time        time.Time `json:"time"`
	Sender      string    `json:"sender"`
	DeviceID    string    `json:"deviceID"`
	Verified    bool      `json:"verified"`
	RoomID      string    `json:"roomID"`
	EventID     string    `json:"eventID"`
	Transcript  string    `json:"transcript"`
	AudioSHA256 string    `json:"audioSHA256,omitempty"`
	Repo        string    `json:"repo,omitempty"`
	PR          int       `json:"pr,omitempty"`
	HeadSHA     string    `json:"headSHA,omitempty"`
	Code        string    `json:"code,omitempty"`
	CardID      int64     `json:"cardID,omitempty"`
	Decision    string    `json:"decision"`
	Reason      string    `json:"reason,omitempty"`
}

// GateState is the gate's switchboard.
type GateState struct {
	Paused    bool      `json:"paused"`
	ChangedBy string    `json:"changedBy,omitempty"`
	ChangedAt time.Time `json:"changedAt"`
}

// Store is everything glovecell-voice persists.
type Store interface {
	Ping(context.Context) error

	// ClaimEvent records a Matrix event as handled. It returns false when
	// the event was claimed before, so a restart never handles it twice.
	ClaimEvent(ctx context.Context, eventID string) (bool, error)

	UpsertThread(context.Context, Thread) error
	GetThread(ctx context.Context, roomID, root string) (Thread, error)

	CreateTask(context.Context, Task) (Task, error)
	GetTask(context.Context, int64) (Task, error)
	// UpdateTask saves the mutable fields: Target, ExternalID, State,
	// Result, Error, Delivered and Repo.
	UpdateTask(context.Context, Task) error
	// PendingTasks are tasks still running or finished but not delivered.
	PendingTasks(context.Context) ([]Task, error)
	// TaskInThread returns the newest task started in the thread.
	TaskInThread(ctx context.Context, roomID, threadRoot string) (Task, error)

	// CreateCard stores a card. ErrConflict means the code is taken by
	// another open card.
	CreateCard(context.Context, Card) (Card, error)
	// CardByCode returns the newest card with the code, in any state.
	CardByCode(ctx context.Context, code string) (Card, error)
	GetCard(context.Context, int64) (Card, error)
	SetCardEvent(ctx context.Context, id int64, eventID string) error
	// OpenCards lists open, unexpired PR cards.
	OpenCards(ctx context.Context, now time.Time) ([]Card, error)
	// OpenCardFor returns the open PR card for repo and PR, if any.
	OpenCardFor(ctx context.Context, repo string, pr int) (Card, error)
	// UseCard consumes an open, unexpired card: exactly one caller wins,
	// every other gets ErrConflict.
	UseCard(ctx context.Context, id int64, now time.Time) (Card, error)
	// CloseCard moves a card from one state to another (for example
	// approved to merged). ErrConflict means it was not in state from.
	CloseCard(ctx context.Context, id int64, from, to, outcome string) error
	// CardsInState lists cards in a state (recovery of approved cards).
	CardsInState(ctx context.Context, state string) ([]Card, error)
	// RecentCodes returns codes of cards created since t, for collision
	// avoidance.
	RecentCodes(ctx context.Context, since time.Time) (map[string]bool, error)

	AppendAudit(context.Context, AuditEntry) (AuditEntry, error)
	ListAudit(ctx context.Context, limit int) ([]AuditEntry, error)
	// CountDecisions counts audit rows with the decision since t.
	CountDecisions(ctx context.Context, decision string, since time.Time) (int, error)
	// AudioSeen reports whether an audit row already carries the audio hash.
	AudioSeen(ctx context.Context, sha256 string) (bool, error)

	GetGateState(context.Context) (GateState, error)
	SetGateState(context.Context, GateState) error
}

func validTask(t Task) error {
	if t.RoomID == "" || t.Origin == "" || t.State == "" {
		return errors.Join(ErrInvalid, errors.New("task needs a room, origin and state"))
	}
	return nil
}

func validCard(c Card) error {
	if len(c.Code) != 4 || c.RoomID == "" || c.State == "" || c.ExpiresAt.IsZero() {
		return errors.Join(ErrInvalid, errors.New("card needs a 4-digit code, room, state and expiry"))
	}
	switch c.Kind {
	case CardPR:
		if c.Repo == "" || c.PR <= 0 || len(c.HeadSHA) < 7 {
			return errors.Join(ErrInvalid, errors.New("PR card needs a repository, PR and head SHA"))
		}
	case CardList:
		if len(c.Items) == 0 {
			return errors.Join(ErrInvalid, errors.New("list card needs items"))
		}
	default:
		return errors.Join(ErrInvalid, errors.New("unknown card kind"))
	}
	return nil
}
