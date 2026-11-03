package store

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"

	apcstore "git.cluster.aimingupward.com/steve/agent-project-controller/internal/store"
)

//go:embed migrations/*.sql
var migrations embed.FS

// MigrationLockID serialises voice schema migrations between replicas
// ("GCVS" in ASCII); it differs from the controller's lock.
const MigrationLockID int64 = 0x47435653

// MigrationTable records applied voice schema versions, separately from the
// controller's apc_schema_migrations.
const MigrationTable = "glovecell_voice_schema_migrations"

// Postgres is the PostgreSQL Store. Until Migrate succeeds every method
// returns ErrUnavailable.
type Postgres struct {
	pool     *pgxpool.Pool
	migrated atomic.Bool
}

var _ Store = (*Postgres)(nil)

// Open creates the pool from the controller's Config (verify-full TLS).
func Open(ctx context.Context, c apcstore.Config) (*Postgres, error) {
	pc, err := c.PoolConfig()
	if err != nil {
		return nil, err
	}
	pc.ConnConfig.RuntimeParams["application_name"] = "glovecell-voice"
	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		return nil, errors.New("database pool could not be created")
	}
	return &Postgres{pool: pool}, nil
}

// NewPostgres wraps an existing pool (tests and tools).
func NewPostgres(pool *pgxpool.Pool) *Postgres { return &Postgres{pool: pool} }

// Pool returns the pool, for the Matrix crypto store.
func (p *Postgres) Pool() *pgxpool.Pool { return p.pool }

// Close closes the pool.
func (p *Postgres) Close() { p.pool.Close() }

// Migrations returns the embedded migration files.
func Migrations() fs.FS {
	sub, _ := fs.Sub(migrations, "migrations")
	return sub
}

// Migrate applies pending migrations under a session advisory lock.
func (p *Postgres) Migrate(ctx context.Context) error {
	db := stdlib.OpenDBFromPool(p.pool)
	defer db.Close()
	locker, err := lock.NewPostgresSessionLocker(lock.WithLockID(MigrationLockID), lock.WithLockTimeout(2, 60))
	if err != nil {
		return err
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, db, Migrations(),
		goose.WithSessionLocker(locker), goose.WithTableName(MigrationTable), goose.WithDisableGlobalRegistry(true))
	if err != nil {
		return err
	}
	results, err := provider.Up(ctx)
	if err != nil {
		return apcstore.Classify(fmt.Errorf("voice schema migration: %w", err))
	}
	for _, r := range results {
		slog.InfoContext(ctx, "voice database migration applied", "version", r.Source.Version, "duration", r.Duration.String())
	}
	p.migrated.Store(true)
	return nil
}

func (p *Postgres) ready() error {
	if !p.migrated.Load() {
		return fmt.Errorf("%w: schema migrations have not run", ErrUnavailable)
	}
	return nil
}

func classify(err error) error { return apcstore.Classify(err) }

// Ping checks connectivity and the schema.
func (p *Postgres) Ping(ctx context.Context) error {
	if err := p.ready(); err != nil {
		return err
	}
	return classify(p.pool.Ping(ctx))
}

func (p *Postgres) exec(ctx context.Context, q string, args ...any) (int64, error) {
	if err := p.ready(); err != nil {
		return 0, err
	}
	tag, err := p.pool.Exec(ctx, q, args...)
	if err != nil {
		return 0, classify(err)
	}
	return tag.RowsAffected(), nil
}

func (p *Postgres) ClaimEvent(ctx context.Context, eventID string) (bool, error) {
	if eventID == "" {
		return false, ErrInvalid
	}
	n, err := p.exec(ctx, `INSERT INTO voice_events (event_id) VALUES ($1) ON CONFLICT DO NOTHING`, eventID)
	return n == 1, err
}

func (p *Postgres) UpsertThread(ctx context.Context, t Thread) error {
	if t.RoomID == "" || t.RootEvent == "" {
		return ErrInvalid
	}
	if t.CreatedAt.IsZero() {
		t.CreatedAt = time.Now()
	}
	_, err := p.exec(ctx, `INSERT INTO voice_threads (room_id, root_event, target, created_at) VALUES ($1, $2, $3, $4)
ON CONFLICT (room_id, root_event) DO UPDATE SET target = EXCLUDED.target`, t.RoomID, t.RootEvent, t.Target, t.CreatedAt.UTC())
	return err
}

func (p *Postgres) GetThread(ctx context.Context, roomID, root string) (Thread, error) {
	if err := p.ready(); err != nil {
		return Thread{}, err
	}
	t := Thread{RoomID: roomID, RootEvent: root}
	err := p.pool.QueryRow(ctx, `SELECT target, created_at FROM voice_threads WHERE room_id = $1 AND root_event = $2`, roomID, root).Scan(&t.Target, &t.CreatedAt)
	return t, classify(err)
}

const taskColumns = `id, room_id, thread_root, source_event, origin, target, repo, external_id, state, transcript, audio_sha256, result, error, delivered, created_at, updated_at, result_event_id`

func scanTask(row pgx.Row) (Task, error) {
	var t Task
	err := row.Scan(&t.ID, &t.RoomID, &t.ThreadRoot, &t.SourceEvent, &t.Origin, &t.Target, &t.Repo, &t.ExternalID, &t.State, &t.Transcript, &t.AudioSHA256, &t.Result, &t.Error, &t.Delivered, &t.CreatedAt, &t.UpdatedAt, &t.ResultEventID)
	return t, classify(err)
}

func (p *Postgres) CreateTask(ctx context.Context, t Task) (Task, error) {
	if err := validTask(t); err != nil {
		return Task{}, err
	}
	if err := p.ready(); err != nil {
		return Task{}, err
	}
	now := time.Now().UTC()
	if t.CreatedAt.IsZero() {
		t.CreatedAt = now
	}
	t.UpdatedAt = t.CreatedAt
	return scanTask(p.pool.QueryRow(ctx, `INSERT INTO voice_tasks (room_id, thread_root, source_event, origin, target, repo, external_id, state, transcript, audio_sha256, result, error, delivered, created_at, updated_at, result_event_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16) RETURNING `+taskColumns,
		t.RoomID, t.ThreadRoot, t.SourceEvent, t.Origin, t.Target, t.Repo, t.ExternalID, t.State, t.Transcript, t.AudioSHA256, t.Result, t.Error, t.Delivered, t.CreatedAt.UTC(), t.UpdatedAt.UTC(), t.ResultEventID))
}

func (p *Postgres) GetTask(ctx context.Context, id int64) (Task, error) {
	if err := p.ready(); err != nil {
		return Task{}, err
	}
	return scanTask(p.pool.QueryRow(ctx, `SELECT `+taskColumns+` FROM voice_tasks WHERE id = $1`, id))
}

func (p *Postgres) UpdateTask(ctx context.Context, t Task) error {
	if err := validTask(t); err != nil {
		return err
	}
	n, err := p.exec(ctx, `UPDATE voice_tasks SET target = $2, external_id = $3, state = $4, result = $5, error = $6, delivered = $7, repo = $8, updated_at = $9, result_event_id = $10 WHERE id = $1`,
		t.ID, t.Target, t.ExternalID, t.State, t.Result, t.Error, t.Delivered, t.Repo, time.Now().UTC(), t.ResultEventID)
	if err == nil && n == 0 {
		return ErrNotFound
	}
	return err
}

func (p *Postgres) PendingTasks(ctx context.Context) ([]Task, error) {
	if err := p.ready(); err != nil {
		return nil, err
	}
	rows, err := p.pool.Query(ctx, `SELECT `+taskColumns+` FROM voice_tasks WHERE NOT delivered ORDER BY id LIMIT 1000`)
	if err != nil {
		return nil, classify(err)
	}
	defer rows.Close()
	var out []Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, classify(rows.Err())
}

func (p *Postgres) TaskInThread(ctx context.Context, room, root string) (Task, error) {
	if err := p.ready(); err != nil {
		return Task{}, err
	}
	return scanTask(p.pool.QueryRow(ctx, `SELECT `+taskColumns+` FROM voice_tasks WHERE room_id = $1 AND thread_root = $2 ORDER BY id DESC LIMIT 1`, room, root))
}

const cardColumns = `id, kind, code, repo, pr, head_sha, title, items, room_id, event_id, requested_by, state, outcome, created_at, expires_at, used_at`

func scanCard(row pgx.Row) (Card, error) {
	var c Card
	var items []byte
	err := row.Scan(&c.ID, &c.Kind, &c.Code, &c.Repo, &c.PR, &c.HeadSHA, &c.Title, &items, &c.RoomID, &c.EventID, &c.RequestedBy, &c.State, &c.Outcome, &c.CreatedAt, &c.ExpiresAt, &c.UsedAt)
	if err != nil {
		return Card{}, classify(err)
	}
	if len(items) > 0 && string(items) != "[]" {
		if err := json.Unmarshal(items, &c.Items); err != nil {
			return Card{}, err
		}
	}
	return c, nil
}

func (p *Postgres) cards(ctx context.Context, q string, args ...any) ([]Card, error) {
	if err := p.ready(); err != nil {
		return nil, err
	}
	rows, err := p.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, classify(err)
	}
	defer rows.Close()
	var out []Card
	for rows.Next() {
		c, err := scanCard(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, classify(rows.Err())
}

func (p *Postgres) CreateCard(ctx context.Context, c Card) (Card, error) {
	if err := validCard(c); err != nil {
		return Card{}, err
	}
	if err := p.ready(); err != nil {
		return Card{}, err
	}
	if c.CreatedAt.IsZero() {
		c.CreatedAt = time.Now()
	}
	items, _ := json.Marshal(c.Items)
	if c.Items == nil {
		items = []byte("[]")
	}
	return scanCard(p.pool.QueryRow(ctx, `INSERT INTO voice_approval_cards (kind, code, repo, pr, head_sha, title, items, room_id, event_id, requested_by, state, outcome, created_at, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14) RETURNING `+cardColumns,
		c.Kind, c.Code, c.Repo, c.PR, c.HeadSHA, c.Title, items, c.RoomID, c.EventID, c.RequestedBy, c.State, c.Outcome, c.CreatedAt.UTC(), c.ExpiresAt.UTC()))
}

func (p *Postgres) CardByCode(ctx context.Context, code string) (Card, error) {
	if err := p.ready(); err != nil {
		return Card{}, err
	}
	// An open card wins over older closed ones with the same code.
	return scanCard(p.pool.QueryRow(ctx, `SELECT `+cardColumns+` FROM voice_approval_cards WHERE code = $1 ORDER BY (state = 'open') DESC, created_at DESC, id DESC LIMIT 1`, code))
}

func (p *Postgres) GetCard(ctx context.Context, id int64) (Card, error) {
	if err := p.ready(); err != nil {
		return Card{}, err
	}
	return scanCard(p.pool.QueryRow(ctx, `SELECT `+cardColumns+` FROM voice_approval_cards WHERE id = $1`, id))
}

func (p *Postgres) SetCardEvent(ctx context.Context, id int64, eventID string) error {
	n, err := p.exec(ctx, `UPDATE voice_approval_cards SET event_id = $2 WHERE id = $1`, id, eventID)
	if err == nil && n == 0 {
		return ErrNotFound
	}
	return err
}

func (p *Postgres) OpenCards(ctx context.Context, now time.Time) ([]Card, error) {
	return p.cards(ctx, `SELECT `+cardColumns+` FROM voice_approval_cards WHERE state = 'open' AND kind = 'pr' AND expires_at > $1 ORDER BY id`, now.UTC())
}

func (p *Postgres) OpenCardFor(ctx context.Context, repo string, pr int) (Card, error) {
	if err := p.ready(); err != nil {
		return Card{}, err
	}
	return scanCard(p.pool.QueryRow(ctx, `SELECT `+cardColumns+` FROM voice_approval_cards WHERE state = 'open' AND kind = 'pr' AND repo = $1 AND pr = $2 ORDER BY id DESC LIMIT 1`, repo, pr))
}

func (p *Postgres) UseCard(ctx context.Context, id int64, now time.Time) (Card, error) {
	if err := p.ready(); err != nil {
		return Card{}, err
	}
	c, err := scanCard(p.pool.QueryRow(ctx, `UPDATE voice_approval_cards SET state = 'approved', used_at = $2 WHERE id = $1 AND state = 'open' AND used_at IS NULL AND expires_at > $2 RETURNING `+cardColumns, id, now.UTC()))
	if errors.Is(err, ErrNotFound) {
		return Card{}, fmt.Errorf("%w: card is not open", ErrConflict)
	}
	return c, err
}

func (p *Postgres) CloseCard(ctx context.Context, id int64, from, to, outcome string) error {
	n, err := p.exec(ctx, `UPDATE voice_approval_cards SET state = $3, outcome = $4 WHERE id = $1 AND state = $2`, id, from, to, outcome)
	if err == nil && n == 0 {
		return fmt.Errorf("%w: card is not %s", ErrConflict, from)
	}
	return err
}

func (p *Postgres) CardsInState(ctx context.Context, state string) ([]Card, error) {
	return p.cards(ctx, `SELECT `+cardColumns+` FROM voice_approval_cards WHERE state = $1 ORDER BY id LIMIT 1000`, state)
}

func (p *Postgres) RecentCodes(ctx context.Context, since time.Time) (map[string]bool, error) {
	if err := p.ready(); err != nil {
		return nil, err
	}
	rows, err := p.pool.Query(ctx, `SELECT DISTINCT code FROM voice_approval_cards WHERE created_at >= $1 OR state = 'open'`, since.UTC())
	if err != nil {
		return nil, classify(err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, classify(err)
		}
		out[c] = true
	}
	return out, classify(rows.Err())
}

const auditColumns = `id, occurred_at, sender, device_id, verified, room_id, event_id, transcript, audio_sha256, repo, pr, head_sha, code, card_id, decision, reason`

func (p *Postgres) AppendAudit(ctx context.Context, a AuditEntry) (AuditEntry, error) {
	if a.Decision == "" {
		return AuditEntry{}, ErrInvalid
	}
	if err := p.ready(); err != nil {
		return AuditEntry{}, err
	}
	if a.Time.IsZero() {
		a.Time = time.Now()
	}
	err := p.pool.QueryRow(ctx, `INSERT INTO voice_approval_audit (occurred_at, sender, device_id, verified, room_id, event_id, transcript, audio_sha256, repo, pr, head_sha, code, card_id, decision, reason)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15) RETURNING id`,
		a.Time.UTC(), a.Sender, a.DeviceID, a.Verified, a.RoomID, a.EventID, a.Transcript, a.AudioSHA256, a.Repo, a.PR, a.HeadSHA, a.Code, a.CardID, a.Decision, a.Reason).Scan(&a.ID)
	return a, classify(err)
}

func (p *Postgres) ListAudit(ctx context.Context, limit int) ([]AuditEntry, error) {
	if err := p.ready(); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	rows, err := p.pool.Query(ctx, `SELECT `+auditColumns+` FROM voice_approval_audit ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, classify(err)
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var a AuditEntry
		if err := rows.Scan(&a.ID, &a.Time, &a.Sender, &a.DeviceID, &a.Verified, &a.RoomID, &a.EventID, &a.Transcript, &a.AudioSHA256, &a.Repo, &a.PR, &a.HeadSHA, &a.Code, &a.CardID, &a.Decision, &a.Reason); err != nil {
			return nil, classify(err)
		}
		out = append(out, a)
	}
	return out, classify(rows.Err())
}

func (p *Postgres) CountDecisions(ctx context.Context, decision string, since time.Time) (int, error) {
	if err := p.ready(); err != nil {
		return 0, err
	}
	var n int
	err := p.pool.QueryRow(ctx, `SELECT count(*) FROM voice_approval_audit WHERE decision = $1 AND occurred_at >= $2`, decision, since.UTC()).Scan(&n)
	return n, classify(err)
}

func (p *Postgres) AudioSeen(ctx context.Context, sha string) (bool, error) {
	if err := p.ready(); err != nil {
		return false, err
	}
	if sha == "" {
		return false, nil
	}
	var seen bool
	err := p.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM voice_approval_audit WHERE audio_sha256 = $1)`, sha).Scan(&seen)
	return seen, classify(err)
}

func (p *Postgres) GetGateState(ctx context.Context) (GateState, error) {
	if err := p.ready(); err != nil {
		return GateState{}, err
	}
	var g GateState
	err := p.pool.QueryRow(ctx, `SELECT paused, changed_by, changed_at FROM voice_gate_state WHERE id = 1`).Scan(&g.Paused, &g.ChangedBy, &g.ChangedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return GateState{}, nil
	}
	return g, classify(err)
}

func (p *Postgres) SetGateState(ctx context.Context, g GateState) error {
	if g.ChangedAt.IsZero() {
		g.ChangedAt = time.Now()
	}
	_, err := p.exec(ctx, `INSERT INTO voice_gate_state (id, paused, changed_by, changed_at) VALUES (1, $1, $2, $3)
ON CONFLICT (id) DO UPDATE SET paused = EXCLUDED.paused, changed_by = EXCLUDED.changed_by, changed_at = EXCLUDED.changed_at`, g.Paused, g.ChangedBy, g.ChangedAt.UTC())
	return err
}
