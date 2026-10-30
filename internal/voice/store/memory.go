package store

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Memory is the in-process Store used by unit tests. Down makes every call
// fail with ErrUnavailable, to test fail-closed behaviour.
type Memory struct {
	mu      sync.Mutex
	Down    bool
	events  map[string]bool
	threads map[[2]string]Thread
	tasks   []Task
	cards   []Card
	audit   []AuditEntry
	gate    GateState
}

var _ Store = (*Memory)(nil)

// NewMemory returns an empty store.
func NewMemory() *Memory {
	return &Memory{events: map[string]bool{}, threads: map[[2]string]Thread{}}
}

func (m *Memory) lock() (func(), error) {
	m.mu.Lock()
	if m.Down {
		m.mu.Unlock()
		return nil, fmt.Errorf("%w: test database down", ErrUnavailable)
	}
	return m.mu.Unlock, nil
}

func (m *Memory) Ping(context.Context) error {
	unlock, err := m.lock()
	if err != nil {
		return err
	}
	unlock()
	return nil
}

func (m *Memory) ClaimEvent(_ context.Context, id string) (bool, error) {
	if id == "" {
		return false, ErrInvalid
	}
	unlock, err := m.lock()
	if err != nil {
		return false, err
	}
	defer unlock()
	if m.events[id] {
		return false, nil
	}
	m.events[id] = true
	return true, nil
}

func (m *Memory) UpsertThread(_ context.Context, t Thread) error {
	if t.RoomID == "" || t.RootEvent == "" {
		return ErrInvalid
	}
	unlock, err := m.lock()
	if err != nil {
		return err
	}
	defer unlock()
	k := [2]string{t.RoomID, t.RootEvent}
	if old, ok := m.threads[k]; ok {
		t.CreatedAt = old.CreatedAt
	} else if t.CreatedAt.IsZero() {
		t.CreatedAt = time.Now()
	}
	m.threads[k] = t
	return nil
}

func (m *Memory) GetThread(_ context.Context, room, root string) (Thread, error) {
	unlock, err := m.lock()
	if err != nil {
		return Thread{}, err
	}
	defer unlock()
	t, ok := m.threads[[2]string{room, root}]
	if !ok {
		return Thread{}, ErrNotFound
	}
	return t, nil
}

func (m *Memory) CreateTask(_ context.Context, t Task) (Task, error) {
	if err := validTask(t); err != nil {
		return Task{}, err
	}
	unlock, err := m.lock()
	if err != nil {
		return Task{}, err
	}
	defer unlock()
	t.ID = int64(len(m.tasks) + 1)
	if t.CreatedAt.IsZero() {
		t.CreatedAt = time.Now()
	}
	t.UpdatedAt = t.CreatedAt
	m.tasks = append(m.tasks, t)
	return t, nil
}

func (m *Memory) GetTask(_ context.Context, id int64) (Task, error) {
	unlock, err := m.lock()
	if err != nil {
		return Task{}, err
	}
	defer unlock()
	if id < 1 || int(id) > len(m.tasks) {
		return Task{}, ErrNotFound
	}
	return m.tasks[id-1], nil
}

func (m *Memory) UpdateTask(_ context.Context, t Task) error {
	if err := validTask(t); err != nil {
		return err
	}
	unlock, err := m.lock()
	if err != nil {
		return err
	}
	defer unlock()
	if t.ID < 1 || int(t.ID) > len(m.tasks) {
		return ErrNotFound
	}
	cur := &m.tasks[t.ID-1]
	cur.Target, cur.ExternalID, cur.State, cur.Result, cur.Error, cur.Delivered, cur.Repo = t.Target, t.ExternalID, t.State, t.Result, t.Error, t.Delivered, t.Repo
	cur.UpdatedAt = time.Now()
	return nil
}

func (m *Memory) PendingTasks(context.Context) ([]Task, error) {
	unlock, err := m.lock()
	if err != nil {
		return nil, err
	}
	defer unlock()
	var out []Task
	for _, t := range m.tasks {
		if !t.Delivered {
			out = append(out, t)
		}
	}
	return out, nil
}

func (m *Memory) CreateCard(_ context.Context, c Card) (Card, error) {
	if err := validCard(c); err != nil {
		return Card{}, err
	}
	unlock, err := m.lock()
	if err != nil {
		return Card{}, err
	}
	defer unlock()
	if c.State == CardOpen {
		for _, o := range m.cards {
			if o.State == CardOpen && o.Code == c.Code {
				return Card{}, fmt.Errorf("%w: code in use", ErrConflict)
			}
		}
	}
	c.ID = int64(len(m.cards) + 1)
	if c.CreatedAt.IsZero() {
		c.CreatedAt = time.Now()
	}
	c.Items = append([]ListItem(nil), c.Items...)
	m.cards = append(m.cards, c)
	return c, nil
}

func (m *Memory) CardByCode(_ context.Context, code string) (Card, error) {
	unlock, err := m.lock()
	if err != nil {
		return Card{}, err
	}
	defer unlock()
	var best *Card
	for i := range m.cards {
		c := &m.cards[i]
		if c.Code != code {
			continue
		}
		if best == nil || (c.State == CardOpen && best.State != CardOpen) || (c.State == CardOpen) == (best.State == CardOpen) && !c.CreatedAt.Before(best.CreatedAt) {
			best = c
		}
	}
	if best == nil {
		return Card{}, ErrNotFound
	}
	return *best, nil
}

func (m *Memory) GetCard(_ context.Context, id int64) (Card, error) {
	unlock, err := m.lock()
	if err != nil {
		return Card{}, err
	}
	defer unlock()
	if id < 1 || int(id) > len(m.cards) {
		return Card{}, ErrNotFound
	}
	return m.cards[id-1], nil
}

func (m *Memory) SetCardEvent(_ context.Context, id int64, ev string) error {
	unlock, err := m.lock()
	if err != nil {
		return err
	}
	defer unlock()
	if id < 1 || int(id) > len(m.cards) {
		return ErrNotFound
	}
	m.cards[id-1].EventID = ev
	return nil
}

func (m *Memory) OpenCards(_ context.Context, now time.Time) ([]Card, error) {
	unlock, err := m.lock()
	if err != nil {
		return nil, err
	}
	defer unlock()
	var out []Card
	for _, c := range m.cards {
		if c.State == CardOpen && c.Kind == CardPR && c.ExpiresAt.After(now) {
			out = append(out, c)
		}
	}
	return out, nil
}

func (m *Memory) OpenCardFor(_ context.Context, repo string, pr int) (Card, error) {
	unlock, err := m.lock()
	if err != nil {
		return Card{}, err
	}
	defer unlock()
	for i := len(m.cards) - 1; i >= 0; i-- {
		c := m.cards[i]
		if c.State == CardOpen && c.Kind == CardPR && c.Repo == repo && c.PR == pr {
			return c, nil
		}
	}
	return Card{}, ErrNotFound
}

func (m *Memory) UseCard(_ context.Context, id int64, now time.Time) (Card, error) {
	unlock, err := m.lock()
	if err != nil {
		return Card{}, err
	}
	defer unlock()
	if id < 1 || int(id) > len(m.cards) {
		return Card{}, fmt.Errorf("%w: card is not open", ErrConflict)
	}
	c := &m.cards[id-1]
	if c.State != CardOpen || c.UsedAt != nil || !c.ExpiresAt.After(now) {
		return Card{}, fmt.Errorf("%w: card is not open", ErrConflict)
	}
	c.State = CardApproved
	t := now
	c.UsedAt = &t
	return *c, nil
}

func (m *Memory) CloseCard(_ context.Context, id int64, from, to, outcome string) error {
	unlock, err := m.lock()
	if err != nil {
		return err
	}
	defer unlock()
	if id < 1 || int(id) > len(m.cards) || m.cards[id-1].State != from {
		return fmt.Errorf("%w: card is not %s", ErrConflict, from)
	}
	m.cards[id-1].State = to
	m.cards[id-1].Outcome = outcome
	return nil
}

func (m *Memory) CardsInState(_ context.Context, state string) ([]Card, error) {
	unlock, err := m.lock()
	if err != nil {
		return nil, err
	}
	defer unlock()
	var out []Card
	for _, c := range m.cards {
		if c.State == state {
			out = append(out, c)
		}
	}
	return out, nil
}

func (m *Memory) RecentCodes(_ context.Context, since time.Time) (map[string]bool, error) {
	unlock, err := m.lock()
	if err != nil {
		return nil, err
	}
	defer unlock()
	out := map[string]bool{}
	for _, c := range m.cards {
		if !c.CreatedAt.Before(since) || c.State == CardOpen {
			out[c.Code] = true
		}
	}
	return out, nil
}

func (m *Memory) AppendAudit(_ context.Context, a AuditEntry) (AuditEntry, error) {
	if a.Decision == "" {
		return AuditEntry{}, ErrInvalid
	}
	unlock, err := m.lock()
	if err != nil {
		return AuditEntry{}, err
	}
	defer unlock()
	a.ID = int64(len(m.audit) + 1)
	if a.Time.IsZero() {
		a.Time = time.Now()
	}
	m.audit = append(m.audit, a)
	return a, nil
}

func (m *Memory) ListAudit(_ context.Context, limit int) ([]AuditEntry, error) {
	unlock, err := m.lock()
	if err != nil {
		return nil, err
	}
	defer unlock()
	out := append([]AuditEntry(nil), m.audit...)
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *Memory) CountDecisions(_ context.Context, d string, since time.Time) (int, error) {
	unlock, err := m.lock()
	if err != nil {
		return 0, err
	}
	defer unlock()
	n := 0
	for _, a := range m.audit {
		if a.Decision == d && !a.Time.Before(since) {
			n++
		}
	}
	return n, nil
}

func (m *Memory) AudioSeen(_ context.Context, sha string) (bool, error) {
	unlock, err := m.lock()
	if err != nil {
		return false, err
	}
	defer unlock()
	if sha == "" {
		return false, nil
	}
	for _, a := range m.audit {
		if a.AudioSHA256 == sha {
			return true, nil
		}
	}
	return false, nil
}

func (m *Memory) GetGateState(context.Context) (GateState, error) {
	unlock, err := m.lock()
	if err != nil {
		return GateState{}, err
	}
	defer unlock()
	return m.gate, nil
}

func (m *Memory) SetGateState(_ context.Context, g GateState) error {
	unlock, err := m.lock()
	if err != nil {
		return err
	}
	defer unlock()
	if g.ChangedAt.IsZero() {
		g.ChangedAt = time.Now()
	}
	m.gate = g
	return nil
}
