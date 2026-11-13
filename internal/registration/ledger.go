package registration

import (
	"errors"
	"fmt"
	"strings"
)

// State is a client's registration state, stored in the control-namespace
// ledger ConfigMap.
type State string

const (
	StateAbsent     State = ""
	StatePending    State = "pending"
	StateRegistered State = "registered"
	StateRevoked    State = "revoked"
)

// transitions are the moves SyncRegister and SyncRevoke make (sync.go).
// Pending is "a register or revoke command is running"; a registered client
// goes back to pending to move or revoke it.
var transitions = map[State][]State{
	StateAbsent:     {StatePending, StateRevoked},
	StatePending:    {StatePending, StateRegistered, StateRevoked},
	StateRegistered: {StatePending},
	StateRevoked:    {StatePending, StateRevoked},
}

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
	State State
	ID    string // desktop project/repo id when registered
	Path  string // mount path that was registered
}

// NewLedger returns an empty ledger.
func NewLedger() *Ledger {
	return &Ledger{Clients: map[string]ClientRecord{}, Owners: map[string]string{}}
}

// ValidState reports whether state is one of the allowed ledger values.
func ValidState(state State) bool {
	switch state {
	case StateAbsent, StatePending, StateRegistered, StateRevoked:
		return true
	default:
		return false
	}
}

// SetClient records a client ack as found, whatever the previous state (for
// decoding and fixtures). Unknown kind or state fails closed. The register
// and revoke flow uses Move, which also checks the transition.
func (l *Ledger) SetClient(kind string, state State, id, path string) error {
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

// Move records a client's next state, refusing a move the register/revoke
// flow never makes (for example registered straight to revoked, which would
// skip the revoke command). A record in an unknown state may only go to
// pending, so a damaged ledger can still be revoked.
func (l *Ledger) Move(kind string, next State, id, path string) error {
	from := l.Clients[strings.TrimSpace(kind)].State
	allowedNext := transitions[from]
	if !ValidState(from) {
		allowedNext = []State{StatePending}
	}
	for _, allowed := range allowedNext {
		if allowed == next {
			return l.SetClient(kind, next, id, path)
		}
	}
	return fmt.Errorf("client %q cannot move from %q to %q", kind, from, next)
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
