// Package store keeps the controller's operational records in PostgreSQL
// (decision D13 item 2): exact request approvals, access requests (pending
// and decided), lifecycle and decision history, and per-request proxy
// summaries. Declarative objects (the cell, AccessProfile, AccessGrant, the
// Class kinds and Kit) stay custom resources.
//
// Postgres implements Store against a real database; Memory is the fake used
// by unit tests. Both satisfy the same contract tests (contract_test.go).
//
// Every write fails closed: when the database cannot be reached, methods
// return an error wrapping ErrUnavailable and nothing is recorded, so callers
// refuse the new approval or request instead of granting without a record.
package store

import (
	"context"
	"errors"
	"strings"
	"time"
)

var (
	// ErrUnavailable wraps every failure to reach the database or to use it
	// before migrations have run. Callers fail closed on it.
	ErrUnavailable = errors.New("database unavailable")
	// ErrNotFound reports a request or approval that does not exist for the
	// workspace named.
	ErrNotFound = errors.New("not found")
	// ErrConflict reports a request that was already decided or an approval
	// that was already revoked.
	ErrConflict = errors.New("conflict")
	// ErrLimit reports a per-workspace cap (pending requests or active
	// approvals) that would be exceeded.
	ErrLimit = errors.New("limit reached")
	// ErrInvalid reports a record that fails validation.
	ErrInvalid = errors.New("invalid record")
)

const (
	// MaxActiveApprovals caps the active exact approvals per workspace, as
	// sandbox-control does today (glovecell.md §3).
	MaxActiveApprovals = 4096
	// MaxPendingRequests caps the pending access requests per workspace, so
	// a noisy agent cannot grow the table without bound.
	MaxPendingRequests = 1024
	// MaxPageSize bounds every list call.
	MaxPageSize = 1000
)

// Event is one history record: a lifecycle step, a policy change, a grant or
// a decision. Key makes writes idempotent; records imported from the audit
// ConfigMaps use the ConfigMap name, so a re-import never duplicates them.
type Event struct {
	ID           int64             `json:"id"`
	Key          string            `json:"key"`
	WorkspaceUID string            `json:"workspaceUID"`
	Workspace    string            `json:"workspace"`
	Generation   int64             `json:"generation"`
	Action       string            `json:"action"`
	Actor        string            `json:"actor"`
	Message      string            `json:"message,omitempty"`
	Details      map[string]string `json:"details,omitempty"`
	// Source says which component wrote the record: controller, console,
	// import (legacy audit ConfigMap) or spool (written to a ConfigMap while
	// the database was down and moved here afterwards).
	Source string    `json:"source"`
	Time   time.Time `json:"time"`
}

// HistoryFilter selects history records, newest first.
type HistoryFilter struct {
	// ActionPrefix keeps actions starting with it ("" keeps all).
	ActionPrefix string
	// Actor keeps one actor ("" keeps all).
	Actor string
	// Since and Before bound Time: Since inclusive, Before exclusive.
	Since, Before time.Time
	// Limit defaults to MaxPageSize and is capped by it.
	Limit int
}

// Request states.
const (
	StatePending  = "pending"
	StateApproved = "approved"
	StateDenied   = "denied"
)

// Target is one exact web request, as the cell proxy sees it
// (glovecell.md §4.3). Fingerprint is the canonical hash of the sorted query
// parameters and bounded body; values are never stored.
type Target struct {
	Scheme      string `json:"scheme"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Method      string `json:"method"`
	Path        string `json:"path"`
	Fingerprint string `json:"fingerprint"`
	// Package is set when the proxy recognised a registry download.
	Package *Package `json:"package,omitempty"`
}

// Package names a registry artifact behind a Target.
type Package struct {
	Ecosystem string `json:"ecosystem"`
	Name      string `json:"name"`
	Version   string `json:"version"`
}

// AccessRequest is a pending or decided human decision about one Target.
// Repeated observations of the same pending Target increment Observations
// instead of adding rows.
type AccessRequest struct {
	ID             int64      `json:"id"`
	WorkspaceUID   string     `json:"workspaceUID"`
	Workspace      string     `json:"workspace"`
	Target         Target     `json:"target"`
	Reason         string     `json:"reason,omitempty"`
	RequestedBy    string     `json:"requestedBy"`
	State          string     `json:"state"`
	Observations   int64      `json:"observations"`
	CreatedAt      time.Time  `json:"createdAt"`
	LastSeenAt     time.Time  `json:"lastSeenAt"`
	DecidedBy      string     `json:"decidedBy,omitempty"`
	DecidedAt      *time.Time `json:"decidedAt,omitempty"`
	DecisionReason string     `json:"decisionReason,omitempty"`
	ApprovalID     *int64     `json:"approvalID,omitempty"`
}

// NewRequest asks for one Target on one workspace.
type NewRequest struct {
	WorkspaceUID, Workspace string
	Target                  Target
	Reason, RequestedBy     string
}

// RequestFilter selects access requests, newest first.
type RequestFilter struct {
	// State is pending, approved, denied or "" for all.
	State string
	// Host keeps one host ("" keeps all).
	Host string
	// BeforeID pages backwards: only IDs lower than it (0 for the first page).
	BeforeID int64
	Limit    int
}

// Decision approves or denies one pending request. Approving creates (or
// reuses) the matching active Approval in the same transaction, and the
// decision is written to history in that transaction too.
type Decision struct {
	WorkspaceUID string
	RequestID    int64
	Approve      bool
	// Actor is the approver's identity; Group is the approver group that
	// allowed the decision. Both are recorded.
	Actor, Group, Reason string
}

// Approval sources.
const (
	SourceApproved = "approved"
	SourceLearned  = "learned"
)

// Approval is one remembered exact request (glovecell.md §4.3).
type Approval struct {
	ID           int64      `json:"id"`
	WorkspaceUID string     `json:"workspaceUID"`
	Workspace    string     `json:"workspace"`
	Target       Target     `json:"target"`
	Source       string     `json:"source"`
	RequestID    *int64     `json:"requestID,omitempty"`
	CreatedBy    string     `json:"createdBy"`
	CreatedAt    time.Time  `json:"createdAt"`
	RevokedBy    string     `json:"revokedBy,omitempty"`
	RevokedAt    *time.Time `json:"revokedAt,omitempty"`
	RevokeReason string     `json:"revokeReason,omitempty"`
}

// ApprovalFilter selects approvals, newest first.
type ApprovalFilter struct {
	IncludeRevoked bool
	Host           string
	BeforeID       int64
	Limit          int
}

// Revocation withdraws one active approval; it is recorded in history in the
// same transaction.
type Revocation struct {
	WorkspaceUID         string
	ApprovalID           int64
	Actor, Group, Reason string
}

// Decisions a proxy reports for one request.
const (
	ProxyAllow = "allow"
	ProxyDeny  = "deny"
)

// ProxySummary is the metadata of one proxied request (glovecell.md §4.8):
// never query values, bodies or credential values.
type ProxySummary struct {
	ID           int64  `json:"id"`
	WorkspaceUID string `json:"workspaceUID"`
	// RequestID is the proxy's own ID; with WorkspaceUID it makes delivery
	// from the proxy's outbox idempotent.
	RequestID         string    `json:"requestID"`
	Time              time.Time `json:"time"`
	Target            Target    `json:"target"`
	Decision          string    `json:"decision"`
	Matched           string    `json:"matched,omitempty"`
	Window            string    `json:"window,omitempty"`
	Status            int       `json:"status,omitempty"`
	BytesSent         int64     `json:"bytesSent"`
	BytesReceived     int64     `json:"bytesReceived"`
	DurationMillis    int64     `json:"durationMillis"`
	CredentialProfile string    `json:"credentialProfile,omitempty"`
}

// ProxyFilter selects proxy summaries, newest first.
type ProxyFilter struct {
	Decision      string
	Host          string
	Since, Before time.Time
	Limit         int
}

// Retention bounds how long records are kept. Zero keeps them forever.
// Approvals are never pruned (they are live policy), pending requests are
// never pruned, and LedgerActions are never pruned from history.
type Retention struct {
	History, DecidedRequests, ProxySummaries time.Duration
}

// Pruned counts the rows one Prune removed.
type Pruned struct {
	History, Requests, ProxySummaries int64
}

// LedgerActions are history actions the controller reads back as safety
// evidence (for example, "an identity was created, so a missing credential
// ledger needs operator recovery"). Retention never removes them.
var LedgerActions = []string{"grant-requested", "access-granted", "orchestrator-bound", "remove-requested", "access-revoked", "cleanup-verified"}

// HistoryStore keeps history records.
type HistoryStore interface {
	// AppendEvent records e unless a record with the same Key exists. It
	// reports whether a row was inserted.
	AppendEvent(ctx context.Context, e Event) (bool, error)
	Events(ctx context.Context, workspaceUID string, f HistoryFilter) ([]Event, error)
	// HasAction reports whether any record of the workspace has one of the
	// actions.
	HasAction(ctx context.Context, workspaceUID string, actions ...string) (bool, error)
}

// RequestStore keeps access requests and their decisions.
type RequestStore interface {
	// CreateRequest records a pending request, or counts one more
	// observation of the identical pending request. created is false for
	// the latter.
	CreateRequest(ctx context.Context, r NewRequest) (req AccessRequest, created bool, err error)
	Request(ctx context.Context, workspaceUID string, id int64) (AccessRequest, error)
	Requests(ctx context.Context, workspaceUID string, f RequestFilter) ([]AccessRequest, error)
	Decide(ctx context.Context, d Decision) (AccessRequest, *Approval, error)
}

// ApprovalStore keeps exact approvals.
type ApprovalStore interface {
	Approvals(ctx context.Context, workspaceUID string, f ApprovalFilter) ([]Approval, error)
	// ActiveApprovals is the full active set of one workspace (at most
	// MaxActiveApprovals), the snapshot a proxy enforces and caches.
	ActiveApprovals(ctx context.Context, workspaceUID string) ([]Approval, error)
	RevokeApproval(ctx context.Context, r Revocation) (Approval, error)
}

// ProxySummaryStore keeps per-request proxy summaries.
type ProxySummaryStore interface {
	// RecordProxySummaries inserts summaries not yet recorded and returns
	// how many were new.
	RecordProxySummaries(ctx context.Context, s []ProxySummary) (int, error)
	ProxySummaries(ctx context.Context, workspaceUID string, f ProxyFilter) ([]ProxySummary, error)
}

// ImportStore remembers one-time imports.
type ImportStore interface {
	ImportDone(ctx context.Context, name string) (bool, error)
	MarkImported(ctx context.Context, name string, records int) error
}

// Store is everything the controller keeps in the database.
type Store interface {
	HistoryStore
	RequestStore
	ApprovalStore
	ProxySummaryStore
	ImportStore
	// Ping checks the database is reachable and migrated.
	Ping(ctx context.Context) error
	// Prune deletes records older than the retention settings.
	Prune(ctx context.Context, r Retention, now time.Time) (Pruned, error)
}

func limit(n int) int {
	if n <= 0 || n > MaxPageSize {
		return MaxPageSize
	}
	return n
}

// Validate checks a Target. Hosts are lower case; paths are the escaped
// request path without a query.
func (t Target) Validate() error {
	switch {
	case t.Scheme != "https" && t.Scheme != "http":
		return errors.New("scheme must be http or https")
	case t.Host == "" || len(t.Host) > 253 || strings.ToLower(t.Host) != t.Host || strings.ContainsAny(t.Host, "/:@ \t\r\n"):
		return errors.New("host must be a lower-case DNS name or address")
	case t.Port < 1 || t.Port > 65535:
		return errors.New("port out of range")
	case t.Method == "" || len(t.Method) > 16 || strings.ToUpper(t.Method) != t.Method || strings.ContainsAny(t.Method, " \t\r\n"):
		return errors.New("method must be an upper-case HTTP method")
	case !strings.HasPrefix(t.Path, "/") || len(t.Path) > 4096 || strings.ContainsAny(t.Path, "?# \t\r\n"):
		return errors.New("path must be an escaped absolute path without query")
	case len(t.Fingerprint) > 128:
		return errors.New("fingerprint too long")
	}
	if p := t.Package; p != nil && (p.Ecosystem == "" || p.Name == "" || p.Version == "" || len(p.Ecosystem)+len(p.Name)+len(p.Version) > 512) {
		return errors.New("package needs ecosystem, name and version")
	}
	return nil
}

func validateEvent(e Event) error {
	if e.Key == "" || len(e.Key) > 253 || e.WorkspaceUID == "" || e.Action == "" || e.Time.IsZero() {
		return errors.Join(ErrInvalid, errors.New("history record needs key, workspace UID, action and time"))
	}
	return nil
}
