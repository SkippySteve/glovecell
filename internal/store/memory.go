package store

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

// Memory is an in-process Store for tests. It follows the same contract as
// Postgres (contract_test.go). SetDown makes every call fail with
// ErrUnavailable, to test fail-closed behaviour.
type Memory struct {
	mu        sync.Mutex
	down      bool
	now       func() time.Time
	nextID    int64
	events    []Event
	keys      map[string]bool
	requests  []AccessRequest
	approvals []Approval
	summaries []ProxySummary
	imports   map[string]int
}

var _ Store = (*Memory)(nil)

// NewMemory returns an empty, available Memory store.
func NewMemory() *Memory {
	return &Memory{keys: map[string]bool{}, imports: map[string]int{}, now: func() time.Time { return time.Now().UTC() }}
}

// SetDown makes the store unavailable (true) or available again.
func (m *Memory) SetDown(down bool) {
	m.mu.Lock()
	m.down = down
	m.mu.Unlock()
}

func (m *Memory) check() error {
	if m.down {
		return fmt.Errorf("%w: memory store is down", ErrUnavailable)
	}
	return nil
}

func (m *Memory) id() int64 {
	m.nextID++
	return m.nextID
}

// Ping implements Store.
func (m *Memory) Ping(context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.check()
}

func (m *Memory) appendLocked(e Event) bool {
	if m.keys[e.Key] {
		return false
	}
	if e.Source == "" {
		e.Source = "controller"
	}
	e.ID = m.id()
	e.Time = e.Time.UTC()
	e.Details = clone(e.Details)
	m.keys[e.Key] = true
	m.events = append(m.events, e)
	return true
}

func clone(d map[string]string) map[string]string {
	if d == nil {
		return map[string]string{}
	}
	out := make(map[string]string, len(d))
	for k, v := range d {
		out[k] = v
	}
	return out
}

// AppendEvent implements HistoryStore.
func (m *Memory) AppendEvent(_ context.Context, e Event) (bool, error) {
	if err := validateEvent(e); err != nil {
		return false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.check(); err != nil {
		return false, err
	}
	return m.appendLocked(e), nil
}

// Events implements HistoryStore.
func (m *Memory) Events(_ context.Context, uid string, f HistoryFilter) ([]Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.check(); err != nil {
		return nil, err
	}
	out := []Event{}
	for _, e := range m.events {
		if e.WorkspaceUID != uid || f.ActionPrefix != "" && !strings.HasPrefix(e.Action, f.ActionPrefix) || f.Actor != "" && e.Actor != f.Actor ||
			!f.Since.IsZero() && e.Time.Before(f.Since) || !f.Before.IsZero() && !e.Time.Before(f.Before) {
			continue
		}
		e.Details = clone(e.Details)
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Time.Equal(out[j].Time) {
			return out[i].ID > out[j].ID
		}
		return out[i].Time.After(out[j].Time)
	})
	if n := limit(f.Limit); len(out) > n {
		out = out[:n]
	}
	return out, nil
}

// HasAction implements HistoryStore.
func (m *Memory) HasAction(_ context.Context, uid string, actions ...string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.check(); err != nil {
		return false, err
	}
	for _, e := range m.events {
		if e.WorkspaceUID == uid && slices.Contains(actions, e.Action) {
			return true, nil
		}
	}
	return false, nil
}

func sameTarget(a, b Target) bool {
	return a.Scheme == b.Scheme && a.Host == b.Host && a.Port == b.Port && a.Method == b.Method && a.Path == b.Path && a.Fingerprint == b.Fingerprint
}

func copyRequest(r AccessRequest) AccessRequest {
	if r.Target.Package != nil {
		p := *r.Target.Package
		r.Target.Package = &p
	}
	if r.DecidedAt != nil {
		t := *r.DecidedAt
		r.DecidedAt = &t
	}
	if r.ApprovalID != nil {
		id := *r.ApprovalID
		r.ApprovalID = &id
	}
	return r
}

func copyApproval(a Approval) Approval {
	if a.Target.Package != nil {
		p := *a.Target.Package
		a.Target.Package = &p
	}
	if a.RevokedAt != nil {
		t := *a.RevokedAt
		a.RevokedAt = &t
	}
	if a.RequestID != nil {
		id := *a.RequestID
		a.RequestID = &id
	}
	return a
}

// CreateRequest implements RequestStore.
func (m *Memory) CreateRequest(_ context.Context, n NewRequest) (AccessRequest, bool, error) {
	if err := validateNewRequest(n); err != nil {
		return AccessRequest{}, false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.check(); err != nil {
		return AccessRequest{}, false, err
	}
	pending := 0
	for i := range m.requests {
		r := &m.requests[i]
		if r.WorkspaceUID != n.WorkspaceUID || r.State != StatePending {
			continue
		}
		if sameTarget(r.Target, n.Target) {
			r.Observations++
			r.LastSeenAt = m.now()
			return copyRequest(*r), false, nil
		}
		pending++
	}
	if pending >= MaxPendingRequests {
		return AccessRequest{}, false, fmt.Errorf("%w: %d pending requests for this workspace", ErrLimit, pending)
	}
	now := m.now()
	r := AccessRequest{ID: m.id(), WorkspaceUID: n.WorkspaceUID, Workspace: n.Workspace, Target: n.Target, Reason: n.Reason, RequestedBy: n.RequestedBy,
		State: StatePending, Observations: 1, CreatedAt: now, LastSeenAt: now}
	r = copyRequest(r)
	m.requests = append(m.requests, r)
	m.appendLocked(requestEvent(r))
	return copyRequest(r), true, nil
}

// Request implements RequestStore.
func (m *Memory) Request(_ context.Context, uid string, id int64) (AccessRequest, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.check(); err != nil {
		return AccessRequest{}, err
	}
	for _, r := range m.requests {
		if r.WorkspaceUID == uid && r.ID == id {
			return copyRequest(r), nil
		}
	}
	return AccessRequest{}, ErrNotFound
}

// Requests implements RequestStore.
func (m *Memory) Requests(_ context.Context, uid string, f RequestFilter) ([]AccessRequest, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.check(); err != nil {
		return nil, err
	}
	out := []AccessRequest{}
	for i := len(m.requests) - 1; i >= 0 && len(out) < limit(f.Limit); i-- {
		r := m.requests[i]
		if r.WorkspaceUID != uid || f.State != "" && r.State != f.State || f.Host != "" && r.Target.Host != f.Host || f.BeforeID > 0 && r.ID >= f.BeforeID {
			continue
		}
		out = append(out, copyRequest(r))
	}
	return out, nil
}

// Decide implements RequestStore.
func (m *Memory) Decide(_ context.Context, d Decision) (AccessRequest, *Approval, error) {
	if err := validateDecision(d); err != nil {
		return AccessRequest{}, nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.check(); err != nil {
		return AccessRequest{}, nil, err
	}
	var r *AccessRequest
	for i := range m.requests {
		if m.requests[i].WorkspaceUID == d.WorkspaceUID && m.requests[i].ID == d.RequestID {
			r = &m.requests[i]
		}
	}
	if r == nil {
		return AccessRequest{}, nil, ErrNotFound
	}
	if r.State != StatePending {
		return copyRequest(*r), nil, fmt.Errorf("%w: request already %s", ErrConflict, r.State)
	}
	var approval *Approval
	if d.Approve {
		active := 0
		for i := range m.approvals {
			a := &m.approvals[i]
			if a.WorkspaceUID != d.WorkspaceUID || a.RevokedAt != nil {
				continue
			}
			active++
			if sameTarget(a.Target, r.Target) {
				c := copyApproval(*a)
				approval = &c
			}
		}
		if approval == nil {
			if active >= MaxActiveApprovals {
				return AccessRequest{}, nil, fmt.Errorf("%w: %d active approvals for this workspace", ErrLimit, active)
			}
			reqID := r.ID
			a := copyApproval(Approval{ID: m.id(), WorkspaceUID: r.WorkspaceUID, Workspace: r.Workspace, Target: r.Target, Source: SourceApproved, RequestID: &reqID, CreatedBy: d.Actor, CreatedAt: m.now()})
			m.approvals = append(m.approvals, a)
			c := copyApproval(a)
			approval = &c
		}
	}
	now := m.now()
	r.DecidedAt, r.DecidedBy, r.DecisionReason = &now, d.Actor, d.Reason
	r.State = StateDenied
	if d.Approve {
		r.State = StateApproved
		id := approval.ID
		r.ApprovalID = &id
	}
	m.appendLocked(decisionEvent(*r, approval, d))
	return copyRequest(*r), approval, nil
}

// Approvals implements ApprovalStore.
func (m *Memory) Approvals(_ context.Context, uid string, f ApprovalFilter) ([]Approval, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.check(); err != nil {
		return nil, err
	}
	out := []Approval{}
	for i := len(m.approvals) - 1; i >= 0 && len(out) < limit(f.Limit); i-- {
		a := m.approvals[i]
		if a.WorkspaceUID != uid || !f.IncludeRevoked && a.RevokedAt != nil || f.Host != "" && a.Target.Host != f.Host || f.BeforeID > 0 && a.ID >= f.BeforeID {
			continue
		}
		out = append(out, copyApproval(a))
	}
	return out, nil
}

// ActiveApprovals implements ApprovalStore.
func (m *Memory) ActiveApprovals(_ context.Context, uid string) ([]Approval, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.check(); err != nil {
		return nil, err
	}
	out := []Approval{}
	for _, a := range m.approvals {
		if a.WorkspaceUID == uid && a.RevokedAt == nil && len(out) < MaxActiveApprovals {
			out = append(out, copyApproval(a))
		}
	}
	return out, nil
}

// RevokeApproval implements ApprovalStore.
func (m *Memory) RevokeApproval(_ context.Context, r Revocation) (Approval, error) {
	if r.WorkspaceUID == "" || r.ApprovalID <= 0 || r.Actor == "" {
		return Approval{}, fmt.Errorf("%w: revocation needs workspace, approval and actor", ErrInvalid)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.check(); err != nil {
		return Approval{}, err
	}
	for i := range m.approvals {
		a := &m.approvals[i]
		if a.WorkspaceUID != r.WorkspaceUID || a.ID != r.ApprovalID {
			continue
		}
		if a.RevokedAt != nil {
			return copyApproval(*a), fmt.Errorf("%w: approval already revoked", ErrConflict)
		}
		now := m.now()
		a.RevokedAt, a.RevokedBy, a.RevokeReason = &now, r.Actor, r.Reason
		m.appendLocked(revocationEvent(*a, r))
		return copyApproval(*a), nil
	}
	return Approval{}, ErrNotFound
}

// RecordProxySummaries implements ProxySummaryStore.
func (m *Memory) RecordProxySummaries(_ context.Context, s []ProxySummary) (int, error) {
	if len(s) > MaxPageSize {
		return 0, fmt.Errorf("%w: at most %d summaries per call", ErrInvalid, MaxPageSize)
	}
	for _, x := range s {
		if err := validateSummary(x); err != nil {
			return 0, err
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.check(); err != nil {
		return 0, err
	}
	n := 0
next:
	for _, x := range s {
		for _, y := range m.summaries {
			if y.WorkspaceUID == x.WorkspaceUID && y.RequestID == x.RequestID {
				continue next
			}
		}
		x.ID = m.id()
		x.Time = x.Time.UTC()
		x.Target.Package = nil
		m.summaries = append(m.summaries, x)
		n++
	}
	return n, nil
}

// ProxySummaries implements ProxySummaryStore.
func (m *Memory) ProxySummaries(_ context.Context, uid string, f ProxyFilter) ([]ProxySummary, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.check(); err != nil {
		return nil, err
	}
	out := []ProxySummary{}
	for _, x := range m.summaries {
		if x.WorkspaceUID != uid || f.Decision != "" && x.Decision != f.Decision || f.Host != "" && x.Target.Host != f.Host ||
			!f.Since.IsZero() && x.Time.Before(f.Since) || !f.Before.IsZero() && !x.Time.Before(f.Before) {
			continue
		}
		out = append(out, x)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Time.Equal(out[j].Time) {
			return out[i].ID > out[j].ID
		}
		return out[i].Time.After(out[j].Time)
	})
	if n := limit(f.Limit); len(out) > n {
		out = out[:n]
	}
	return out, nil
}

// ImportDone implements ImportStore.
func (m *Memory) ImportDone(_ context.Context, name string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.check(); err != nil {
		return false, err
	}
	_, ok := m.imports[name]
	return ok, nil
}

// MarkImported implements ImportStore.
func (m *Memory) MarkImported(_ context.Context, name string, records int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.check(); err != nil {
		return err
	}
	if _, ok := m.imports[name]; !ok {
		m.imports[name] = records
	}
	return nil
}

// Prune implements Store.
func (m *Memory) Prune(_ context.Context, r Retention, now time.Time) (Pruned, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out Pruned
	if err := m.check(); err != nil {
		return out, err
	}
	if r.History > 0 {
		cut := now.Add(-r.History)
		kept := m.events[:0]
		for _, e := range m.events {
			if e.Time.Before(cut) && !slices.Contains(LedgerActions, e.Action) {
				delete(m.keys, e.Key)
				out.History++
				continue
			}
			kept = append(kept, e)
		}
		m.events = kept
	}
	if r.DecidedRequests > 0 {
		cut := now.Add(-r.DecidedRequests)
		referenced := map[int64]bool{}
		for _, a := range m.approvals {
			if a.RequestID != nil {
				referenced[*a.RequestID] = true
			}
		}
		kept := m.requests[:0]
		for _, q := range m.requests {
			if q.State != StatePending && q.DecidedAt != nil && q.DecidedAt.Before(cut) && !referenced[q.ID] {
				out.Requests++
				continue
			}
			kept = append(kept, q)
		}
		m.requests = kept
	}
	if r.ProxySummaries > 0 {
		cut := now.Add(-r.ProxySummaries)
		kept := m.summaries[:0]
		for _, x := range m.summaries {
			if x.Time.Before(cut) {
				out.ProxySummaries++
				continue
			}
			kept = append(kept, x)
		}
		m.summaries = kept
	}
	return out, nil
}
