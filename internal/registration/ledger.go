package registration

import (
	"errors"
	"fmt"
	"strings"
)

// Client registration states stored in the control-namespace ledger ConfigMap.
const (
	StateAbsent     = ""
	StatePending    = "pending"
	StateRegistered = "registered"
	StateRevoked    = "revoked"
)

// KnownKinds lists builtin client kinds for documentation and older callers.
// Prefer KnownKind / LookupAdapter; custom kinds are registered at resolve time.
var KnownKinds = map[string]bool{
	"t3":   true,
	"orca": true,
}

// Ledger is the pure registration / ownership state for one workspace.
type Ledger struct {
	Clients map[string]ClientRecord
	Owners  map[string]string // worktree path -> owner id
}

// ClientRecord is one configured client's registration ack.
type ClientRecord struct {
	Kind  string
	State string
	ID    string // desktop project/repo id when registered
	Path  string // mount path that was registered
}

// NewLedger returns an empty ledger.
func NewLedger() *Ledger {
	return &Ledger{Clients: map[string]ClientRecord{}, Owners: map[string]string{}}
}

// ValidState reports whether state is one of the allowed ledger values.
func ValidState(state string) bool {
	switch state {
	case StateAbsent, StatePending, StateRegistered, StateRevoked:
		return true
	default:
		return false
	}
}

// SetClient records a client ack. Unknown kind or state fails closed.
func (l *Ledger) SetClient(kind, state, id, path string) error {
	kind = strings.TrimSpace(kind)
	if !KnownKind(kind) {
		return fmt.Errorf("unknown client kind %q", kind)
	}
	if !ValidState(state) {
		return fmt.Errorf("unknown client state %q", state)
	}
	if l.Clients == nil {
		l.Clients = map[string]ClientRecord{}
	}
	l.Clients[kind] = ClientRecord{Kind: kind, State: state, ID: id, Path: path}
	return nil
}

// ClaimOwner records an exclusive owner for a worktree path.
func (l *Ledger) ClaimOwner(worktree, owner string) error {
	worktree = strings.TrimSpace(worktree)
	owner = strings.TrimSpace(owner)
	if worktree == "" || owner == "" {
		return errors.New("worktree and owner required")
	}
	if l.Owners == nil {
		l.Owners = map[string]string{}
	}
	if existing, ok := l.Owners[worktree]; ok && existing != "" && existing != owner {
		return fmt.Errorf("worktree already claimed by %q", existing)
	}
	l.Owners[worktree] = owner
	return nil
}

// ReleaseOwner clears the owner for a worktree when the caller still holds it.
func (l *Ledger) ReleaseOwner(worktree, owner string) error {
	worktree = strings.TrimSpace(worktree)
	owner = strings.TrimSpace(owner)
	if worktree == "" || owner == "" {
		return errors.New("worktree and owner required")
	}
	existing := l.Owners[worktree]
	if existing == "" {
		return nil
	}
	if existing != owner {
		return fmt.Errorf("worktree claimed by %q", existing)
	}
	delete(l.Owners, worktree)
	return nil
}

// RemovalBlocked reports whether cleanup-verified must wait on clients.
// No clients / all absent or revoked → false. Pending, registered, or any
// unknown state → true with an error describing the blocker.
func (l *Ledger) RemovalBlocked() (bool, error) {
	if l == nil || len(l.Clients) == 0 {
		return false, nil
	}
	for kind, rec := range l.Clients {
		if !KnownKind(kind) && !KnownKind(rec.Kind) {
			return true, fmt.Errorf("unknown client kind %q blocks removal", kind)
		}
		state := rec.State
		if !ValidState(state) {
			return true, fmt.Errorf("client %q has unknown state %q", kind, state)
		}
		switch state {
		case StateAbsent, StateRevoked:
			continue
		case StatePending, StateRegistered:
			return true, fmt.Errorf("client %q is %s", kind, state)
		}
	}
	return false, nil
}

// ConfigMapName returns the control-namespace ledger name for a workspace UID.
func ConfigMapName(uid string) string {
	return "clients-" + strings.ReplaceAll(uid, "-", "")
}
