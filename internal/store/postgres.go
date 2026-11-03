package store

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
)

//go:embed migrations/*.sql
var migrations embed.FS

// MigrationLockID is the PostgreSQL advisory lock key that serialises schema
// migrations between controller replicas ("APCS" in ASCII).
const MigrationLockID int64 = 0x41504353 // "APCS"

// MigrationTable records applied schema versions.
const MigrationTable = "apc_schema_migrations"

// Postgres is the PostgreSQL Store. Until Migrate succeeds every method
// returns ErrUnavailable.
type Postgres struct {
	pool     *pgxpool.Pool
	migrated atomic.Bool
}

var _ Store = (*Postgres)(nil)

// Open creates the connection pool. It does not connect: the first query
// (normally Migrate) does, so the controller starts even while the database
// is down and keeps enforcing what it already has.
func Open(ctx context.Context, c Config) (*Postgres, error) {
	pc, err := c.PoolConfig()
	if err != nil {
		return nil, err
	}
	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		return nil, errors.New("database pool could not be created")
	}
	return &Postgres{pool: pool}, nil
}

// NewPostgres wraps an existing pool (tests and tools).
func NewPostgres(pool *pgxpool.Pool) *Postgres { return &Postgres{pool: pool} }

// Close closes the pool.
func (p *Postgres) Close() { p.pool.Close() }

// Migrations returns the embedded migration files.
func Migrations() fs.FS {
	sub, _ := fs.Sub(migrations, "migrations")
	return sub
}

// Migrate applies pending migrations under a session advisory lock, so two
// replicas starting together apply each version once. It is safe to call
// repeatedly.
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
		return classify(fmt.Errorf("schema migration: %w", err))
	}
	for _, r := range results {
		slog.InfoContext(ctx, "database migration applied", "version", r.Source.Version, "duration", r.Duration.String())
	}
	current, target, err := provider.GetVersions(ctx)
	if err != nil {
		return classify(err)
	}
	if current > target {
		// A newer controller migrated the schema; migrations only add, so an
		// older controller keeps working, but say so.
		slog.WarnContext(ctx, "database schema is newer than this controller", "schema", current, "controller", target)
	}
	p.migrated.Store(true)
	return nil
}

// SchemaVersion returns the applied schema version.
func (p *Postgres) SchemaVersion(ctx context.Context) (int64, error) {
	var v int64
	err := p.pool.QueryRow(ctx, "SELECT coalesce(max(version_id), 0) FROM "+MigrationTable+" WHERE is_applied").Scan(&v)
	return v, classify(err)
}

func (p *Postgres) ready() error {
	if !p.migrated.Load() {
		return fmt.Errorf("%w: schema migrations have not run", ErrUnavailable)
	}
	return nil
}

// Ping checks connectivity and the schema.
func (p *Postgres) Ping(ctx context.Context) error {
	if err := p.ready(); err != nil {
		return err
	}
	return classify(p.pool.Ping(ctx))
}

// classify maps driver errors to the package errors. Anything that means the
// database cannot be used right now becomes ErrUnavailable.
func classify(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrUnavailable) || errors.Is(err, ErrNotFound) || errors.Is(err, ErrConflict) || errors.Is(err, ErrLimit) || errors.Is(err, ErrInvalid) {
		return err
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		switch {
		case pg.Code == "23505":
			return fmt.Errorf("%w: %s", ErrConflict, pg.Message)
		case strings.HasPrefix(pg.Code, "08"), strings.HasPrefix(pg.Code, "53"), strings.HasPrefix(pg.Code, "57P"), pg.Code == "25006", pg.Code == "55P03":
			// connection, resources, shutdown, read-only (failover), lock timeout
			return fmt.Errorf("%w: %s (%s)", ErrUnavailable, pg.Message, pg.Code)
		}
		return err
	}
	var ne net.Error
	var ce *pgconn.ConnectError
	if errors.As(err, &ce) || errors.As(err, &ne) || pgconn.Timeout(err) || pgconn.SafeToRetry(err) ||
		errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed) || strings.Contains(err.Error(), "closed pool") {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return err
}

// workspaceLock serialises writes for one workspace inside a transaction, so
// the per-workspace caps hold under concurrency.
func workspaceLock(ctx context.Context, tx pgx.Tx, uid string) error {
	_, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 1095779155))", uid)
	return err
}

func (p *Postgres) inTx(ctx context.Context, fn func(pgx.Tx) error) error {
	if err := p.ready(); err != nil {
		return err
	}
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return classify(err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	if err := fn(tx); err != nil {
		return classify(err)
	}
	return classify(tx.Commit(ctx))
}

func detailsJSON(d map[string]string) []byte {
	if d == nil {
		d = map[string]string{}
	}
	b, _ := json.Marshal(d)
	return b
}

const insertEvent = `INSERT INTO audit_events (record_key, workspace_uid, workspace, generation, action, actor, message, details, source, occurred_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) ON CONFLICT (record_key) DO NOTHING`

func eventArgs(e Event) []any {
	if e.Source == "" {
		e.Source = "controller"
	}
	return []any{e.Key, e.WorkspaceUID, e.Workspace, e.Generation, e.Action, e.Actor, e.Message, detailsJSON(e.Details), e.Source, e.Time.UTC()}
}

// AppendEvent implements HistoryStore.
func (p *Postgres) AppendEvent(ctx context.Context, e Event) (bool, error) {
	if err := validateEvent(e); err != nil {
		return false, err
	}
	if err := p.ready(); err != nil {
		return false, err
	}
	tag, err := p.pool.Exec(ctx, insertEvent, eventArgs(e)...)
	if err != nil {
		return false, classify(err)
	}
	return tag.RowsAffected() == 1, nil
}

func likePrefix(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s) + "%"
}

// Events implements HistoryStore.
func (p *Postgres) Events(ctx context.Context, uid string, f HistoryFilter) ([]Event, error) {
	if err := p.ready(); err != nil {
		return nil, err
	}
	q := `SELECT id, record_key, workspace_uid, workspace, generation, action, actor, message, details, source, occurred_at FROM audit_events WHERE workspace_uid = $1`
	args := []any{uid}
	if f.ActionPrefix != "" {
		args = append(args, likePrefix(f.ActionPrefix))
		q += fmt.Sprintf(" AND action LIKE $%d", len(args))
	}
	if f.Actor != "" {
		args = append(args, f.Actor)
		q += fmt.Sprintf(" AND actor = $%d", len(args))
	}
	if !f.Since.IsZero() {
		args = append(args, f.Since)
		q += fmt.Sprintf(" AND occurred_at >= $%d", len(args))
	}
	if !f.Before.IsZero() {
		args = append(args, f.Before)
		q += fmt.Sprintf(" AND occurred_at < $%d", len(args))
	}
	args = append(args, limit(f.Limit))
	q += fmt.Sprintf(" ORDER BY occurred_at DESC, id DESC LIMIT $%d", len(args))
	rows, err := p.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, classify(err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Event, error) {
		var e Event
		var details []byte
		if err := row.Scan(&e.ID, &e.Key, &e.WorkspaceUID, &e.Workspace, &e.Generation, &e.Action, &e.Actor, &e.Message, &details, &e.Source, &e.Time); err != nil {
			return e, err
		}
		_ = json.Unmarshal(details, &e.Details)
		e.Time = e.Time.UTC()
		return e, nil
	})
	return out, classify(err)
}

// HasAction implements HistoryStore.
func (p *Postgres) HasAction(ctx context.Context, uid string, actions ...string) (bool, error) {
	if err := p.ready(); err != nil {
		return false, err
	}
	var ok bool
	err := p.pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM audit_events WHERE workspace_uid = $1 AND action = ANY($2))", uid, actions).Scan(&ok)
	return ok, classify(err)
}

const requestColumns = `id, workspace_uid, workspace, scheme, host, port, method, path, fingerprint, package_ecosystem, package_name, package_version,
reason, requested_by, state, observations, created_at, last_seen_at, coalesce(decided_by, ''), decided_at, decision_reason, approval_id`

func scanRequest(row pgx.Row) (AccessRequest, error) {
	var r AccessRequest
	var eco, name, version *string
	err := row.Scan(&r.ID, &r.WorkspaceUID, &r.Workspace, &r.Target.Scheme, &r.Target.Host, &r.Target.Port, &r.Target.Method, &r.Target.Path, &r.Target.Fingerprint,
		&eco, &name, &version, &r.Reason, &r.RequestedBy, &r.State, &r.Observations, &r.CreatedAt, &r.LastSeenAt, &r.DecidedBy, &r.DecidedAt, &r.DecisionReason, &r.ApprovalID)
	if eco != nil && name != nil && version != nil {
		r.Target.Package = &Package{Ecosystem: *eco, Name: *name, Version: *version}
	}
	r.CreatedAt, r.LastSeenAt = r.CreatedAt.UTC(), r.LastSeenAt.UTC()
	if r.DecidedAt != nil {
		t := r.DecidedAt.UTC()
		r.DecidedAt = &t
	}
	return r, err
}

func packageArgs(p *Package) (any, any, any) {
	if p == nil {
		return nil, nil, nil
	}
	return p.Ecosystem, p.Name, p.Version
}

const targetMatch = `workspace_uid = $1 AND scheme = $2 AND host = $3 AND port = $4 AND method = $5 AND path = $6 AND fingerprint = $7`

func targetArgs(uid string, t Target) []any {
	return []any{uid, t.Scheme, t.Host, t.Port, t.Method, t.Path, t.Fingerprint}
}

// CreateRequest implements RequestStore.
func (p *Postgres) CreateRequest(ctx context.Context, n NewRequest) (AccessRequest, bool, error) {
	if err := validateNewRequest(n); err != nil {
		return AccessRequest{}, false, err
	}
	var out AccessRequest
	created := false
	err := p.inTx(ctx, func(tx pgx.Tx) error {
		if err := workspaceLock(ctx, tx, n.WorkspaceUID); err != nil {
			return err
		}
		r, err := scanRequest(tx.QueryRow(ctx, `UPDATE access_requests SET observations = observations + 1, last_seen_at = now()
WHERE `+targetMatch+` AND state = 'pending' RETURNING `+requestColumns, targetArgs(n.WorkspaceUID, n.Target)...))
		if err == nil {
			out = r
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var pending int
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM access_requests WHERE workspace_uid = $1 AND state = 'pending'", n.WorkspaceUID).Scan(&pending); err != nil {
			return err
		}
		if pending >= MaxPendingRequests {
			return fmt.Errorf("%w: %d pending requests for this workspace", ErrLimit, pending)
		}
		eco, name, version := packageArgs(n.Target.Package)
		args := append(targetArgs(n.WorkspaceUID, n.Target), n.Workspace, eco, name, version, n.Reason, n.RequestedBy)
		out, err = scanRequest(tx.QueryRow(ctx, `INSERT INTO access_requests (workspace_uid, scheme, host, port, method, path, fingerprint, workspace, package_ecosystem, package_name, package_version, reason, requested_by)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13) RETURNING `+requestColumns, args...))
		if err != nil {
			return err
		}
		created = true
		e := requestEvent(out)
		_, err = tx.Exec(ctx, insertEvent, eventArgs(e)...)
		return err
	})
	return out, created, err
}

// Request implements RequestStore.
func (p *Postgres) Request(ctx context.Context, uid string, id int64) (AccessRequest, error) {
	if err := p.ready(); err != nil {
		return AccessRequest{}, err
	}
	r, err := scanRequest(p.pool.QueryRow(ctx, "SELECT "+requestColumns+" FROM access_requests WHERE workspace_uid = $1 AND id = $2", uid, id))
	return r, classify(err)
}

// Requests implements RequestStore.
func (p *Postgres) Requests(ctx context.Context, uid string, f RequestFilter) ([]AccessRequest, error) {
	if err := p.ready(); err != nil {
		return nil, err
	}
	q := "SELECT " + requestColumns + " FROM access_requests WHERE workspace_uid = $1"
	args := []any{uid}
	if f.State != "" {
		args = append(args, f.State)
		q += fmt.Sprintf(" AND state = $%d", len(args))
	}
	if f.Host != "" {
		args = append(args, f.Host)
		q += fmt.Sprintf(" AND host = $%d", len(args))
	}
	if f.BeforeID > 0 {
		args = append(args, f.BeforeID)
		q += fmt.Sprintf(" AND id < $%d", len(args))
	}
	args = append(args, limit(f.Limit))
	q += fmt.Sprintf(" ORDER BY id DESC LIMIT $%d", len(args))
	rows, err := p.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, classify(err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (AccessRequest, error) { return scanRequest(row) })
	return out, classify(err)
}

const approvalColumns = `id, workspace_uid, workspace, scheme, host, port, method, path, fingerprint, package_ecosystem, package_name, package_version,
source, request_id, created_by, created_at, coalesce(revoked_by, ''), revoked_at, revoke_reason`

func scanApproval(row pgx.Row) (Approval, error) {
	var a Approval
	var eco, name, version *string
	err := row.Scan(&a.ID, &a.WorkspaceUID, &a.Workspace, &a.Target.Scheme, &a.Target.Host, &a.Target.Port, &a.Target.Method, &a.Target.Path, &a.Target.Fingerprint,
		&eco, &name, &version, &a.Source, &a.RequestID, &a.CreatedBy, &a.CreatedAt, &a.RevokedBy, &a.RevokedAt, &a.RevokeReason)
	if eco != nil && name != nil && version != nil {
		a.Target.Package = &Package{Ecosystem: *eco, Name: *name, Version: *version}
	}
	a.CreatedAt = a.CreatedAt.UTC()
	if a.RevokedAt != nil {
		t := a.RevokedAt.UTC()
		a.RevokedAt = &t
	}
	return a, err
}

// Decide implements RequestStore.
func (p *Postgres) Decide(ctx context.Context, d Decision) (AccessRequest, *Approval, error) {
	if err := validateDecision(d); err != nil {
		return AccessRequest{}, nil, err
	}
	var out AccessRequest
	var approval *Approval
	err := p.inTx(ctx, func(tx pgx.Tx) error {
		if err := workspaceLock(ctx, tx, d.WorkspaceUID); err != nil {
			return err
		}
		r, err := scanRequest(tx.QueryRow(ctx, "SELECT "+requestColumns+" FROM access_requests WHERE workspace_uid = $1 AND id = $2 FOR UPDATE", d.WorkspaceUID, d.RequestID))
		if err != nil {
			return err
		}
		out = r
		if r.State != StatePending {
			return fmt.Errorf("%w: request already %s", ErrConflict, r.State)
		}
		state := StateDenied
		var approvalID *int64
		if d.Approve {
			state = StateApproved
			a, err := scanApproval(tx.QueryRow(ctx, "SELECT "+approvalColumns+" FROM approvals WHERE "+targetMatch+" AND revoked_at IS NULL", targetArgs(r.WorkspaceUID, r.Target)...))
			if errors.Is(err, pgx.ErrNoRows) {
				var active int
				if err := tx.QueryRow(ctx, "SELECT count(*) FROM approvals WHERE workspace_uid = $1 AND revoked_at IS NULL", r.WorkspaceUID).Scan(&active); err != nil {
					return err
				}
				if active >= MaxActiveApprovals {
					return fmt.Errorf("%w: %d active approvals for this workspace", ErrLimit, active)
				}
				eco, name, version := packageArgs(r.Target.Package)
				args := append(targetArgs(r.WorkspaceUID, r.Target), r.Workspace, eco, name, version, r.ID, d.Actor)
				a, err = scanApproval(tx.QueryRow(ctx, `INSERT INTO approvals (workspace_uid, scheme, host, port, method, path, fingerprint, workspace, package_ecosystem, package_name, package_version, source, request_id, created_by)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, 'approved', $12, $13) RETURNING `+approvalColumns, args...))
			}
			if err != nil {
				return err
			}
			approval = &a
			approvalID = &a.ID
		}
		out, err = scanRequest(tx.QueryRow(ctx, `UPDATE access_requests SET state = $3, decided_by = $4, decided_at = now(), decision_reason = $5, approval_id = $6
WHERE workspace_uid = $1 AND id = $2 RETURNING `+requestColumns, d.WorkspaceUID, d.RequestID, state, d.Actor, d.Reason, approvalID))
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, insertEvent, eventArgs(decisionEvent(out, approval, d))...)
		return err
	})
	if err != nil {
		if errors.Is(err, ErrConflict) {
			return out, nil, err
		}
		return AccessRequest{}, nil, err
	}
	return out, approval, nil
}

// Approvals implements ApprovalStore.
func (p *Postgres) Approvals(ctx context.Context, uid string, f ApprovalFilter) ([]Approval, error) {
	if err := p.ready(); err != nil {
		return nil, err
	}
	q := "SELECT " + approvalColumns + " FROM approvals WHERE workspace_uid = $1"
	args := []any{uid}
	if !f.IncludeRevoked {
		q += " AND revoked_at IS NULL"
	}
	if f.Host != "" {
		args = append(args, f.Host)
		q += fmt.Sprintf(" AND host = $%d", len(args))
	}
	if f.BeforeID > 0 {
		args = append(args, f.BeforeID)
		q += fmt.Sprintf(" AND id < $%d", len(args))
	}
	args = append(args, limit(f.Limit))
	q += fmt.Sprintf(" ORDER BY id DESC LIMIT $%d", len(args))
	return p.queryApprovals(ctx, q, args...)
}

func (p *Postgres) queryApprovals(ctx context.Context, q string, args ...any) ([]Approval, error) {
	rows, err := p.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, classify(err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Approval, error) { return scanApproval(row) })
	return out, classify(err)
}

// ActiveApprovals implements ApprovalStore.
func (p *Postgres) ActiveApprovals(ctx context.Context, uid string) ([]Approval, error) {
	if err := p.ready(); err != nil {
		return nil, err
	}
	return p.queryApprovals(ctx, "SELECT "+approvalColumns+" FROM approvals WHERE workspace_uid = $1 AND revoked_at IS NULL ORDER BY id LIMIT $2", uid, MaxActiveApprovals)
}

// RevokeApproval implements ApprovalStore.
func (p *Postgres) RevokeApproval(ctx context.Context, r Revocation) (Approval, error) {
	if r.WorkspaceUID == "" || r.ApprovalID <= 0 || r.Actor == "" {
		return Approval{}, fmt.Errorf("%w: revocation needs workspace, approval and actor", ErrInvalid)
	}
	var out Approval
	err := p.inTx(ctx, func(tx pgx.Tx) error {
		if err := workspaceLock(ctx, tx, r.WorkspaceUID); err != nil {
			return err
		}
		a, err := scanApproval(tx.QueryRow(ctx, "SELECT "+approvalColumns+" FROM approvals WHERE workspace_uid = $1 AND id = $2 FOR UPDATE", r.WorkspaceUID, r.ApprovalID))
		if err != nil {
			return err
		}
		if a.RevokedAt != nil {
			out = a
			return fmt.Errorf("%w: approval already revoked", ErrConflict)
		}
		out, err = scanApproval(tx.QueryRow(ctx, "UPDATE approvals SET revoked_by = $3, revoked_at = now(), revoke_reason = $4 WHERE workspace_uid = $1 AND id = $2 RETURNING "+approvalColumns,
			r.WorkspaceUID, r.ApprovalID, r.Actor, r.Reason))
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, insertEvent, eventArgs(revocationEvent(out, r))...)
		return err
	})
	return out, err
}

// RecordProxySummaries implements ProxySummaryStore.
func (p *Postgres) RecordProxySummaries(ctx context.Context, s []ProxySummary) (int, error) {
	if len(s) > MaxPageSize {
		return 0, fmt.Errorf("%w: at most %d summaries per call", ErrInvalid, MaxPageSize)
	}
	for _, x := range s {
		if err := validateSummary(x); err != nil {
			return 0, err
		}
	}
	if err := p.ready(); err != nil {
		return 0, err
	}
	batch := &pgx.Batch{}
	for _, x := range s {
		batch.Queue(`INSERT INTO proxy_requests (workspace_uid, request_id, occurred_at, scheme, host, port, method, path, fingerprint, decision, matched, egress_window, status, bytes_sent, bytes_received, duration_ms, credential_profile)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17) ON CONFLICT (workspace_uid, request_id) DO NOTHING`,
			x.WorkspaceUID, x.RequestID, x.Time.UTC(), x.Target.Scheme, x.Target.Host, x.Target.Port, x.Target.Method, x.Target.Path, x.Target.Fingerprint,
			x.Decision, x.Matched, x.Window, x.Status, x.BytesSent, x.BytesReceived, x.DurationMillis, x.CredentialProfile)
	}
	n := 0
	err := p.inTx(ctx, func(tx pgx.Tx) error {
		results := tx.SendBatch(ctx, batch)
		defer results.Close()
		for range s {
			tag, err := results.Exec()
			if err != nil {
				return err
			}
			n += int(tag.RowsAffected())
		}
		return results.Close()
	})
	if err != nil {
		return 0, err
	}
	return n, nil
}

// ProxySummaries implements ProxySummaryStore.
func (p *Postgres) ProxySummaries(ctx context.Context, uid string, f ProxyFilter) ([]ProxySummary, error) {
	if err := p.ready(); err != nil {
		return nil, err
	}
	q := `SELECT id, workspace_uid, request_id, occurred_at, scheme, host, port, method, path, fingerprint, decision, matched, egress_window, status, bytes_sent, bytes_received, duration_ms, credential_profile
FROM proxy_requests WHERE workspace_uid = $1`
	args := []any{uid}
	if f.Decision != "" {
		args = append(args, f.Decision)
		q += fmt.Sprintf(" AND decision = $%d", len(args))
	}
	if f.Host != "" {
		args = append(args, f.Host)
		q += fmt.Sprintf(" AND host = $%d", len(args))
	}
	if !f.Since.IsZero() {
		args = append(args, f.Since)
		q += fmt.Sprintf(" AND occurred_at >= $%d", len(args))
	}
	if !f.Before.IsZero() {
		args = append(args, f.Before)
		q += fmt.Sprintf(" AND occurred_at < $%d", len(args))
	}
	args = append(args, limit(f.Limit))
	q += fmt.Sprintf(" ORDER BY occurred_at DESC, id DESC LIMIT $%d", len(args))
	rows, err := p.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, classify(err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (ProxySummary, error) {
		var x ProxySummary
		err := row.Scan(&x.ID, &x.WorkspaceUID, &x.RequestID, &x.Time, &x.Target.Scheme, &x.Target.Host, &x.Target.Port, &x.Target.Method, &x.Target.Path, &x.Target.Fingerprint,
			&x.Decision, &x.Matched, &x.Window, &x.Status, &x.BytesSent, &x.BytesReceived, &x.DurationMillis, &x.CredentialProfile)
		x.Time = x.Time.UTC()
		return x, err
	})
	return out, classify(err)
}

// ImportDone implements ImportStore.
func (p *Postgres) ImportDone(ctx context.Context, name string) (bool, error) {
	if err := p.ready(); err != nil {
		return false, err
	}
	var ok bool
	err := p.pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM imports WHERE name = $1)", name).Scan(&ok)
	return ok, classify(err)
}

// MarkImported implements ImportStore.
func (p *Postgres) MarkImported(ctx context.Context, name string, records int) error {
	if err := p.ready(); err != nil {
		return err
	}
	_, err := p.pool.Exec(ctx, "INSERT INTO imports (name, records) VALUES ($1, $2) ON CONFLICT (name) DO NOTHING", name, records)
	return classify(err)
}

// Prune implements Store. It deletes in batches so a long backlog never
// holds one big transaction.
func (p *Postgres) Prune(ctx context.Context, r Retention, now time.Time) (Pruned, error) {
	var out Pruned
	if err := p.ready(); err != nil {
		return out, err
	}
	loop := func(q string, cutoff time.Time, args ...any) (int64, error) {
		var total int64
		for {
			tag, err := p.pool.Exec(ctx, q, append([]any{cutoff}, args...)...)
			if err != nil {
				return total, classify(err)
			}
			total += tag.RowsAffected()
			if tag.RowsAffected() < 5000 {
				return total, nil
			}
		}
	}
	var err error
	if r.History > 0 {
		if out.History, err = loop(`DELETE FROM audit_events WHERE id IN (SELECT id FROM audit_events WHERE occurred_at < $1 AND action <> ALL($2) LIMIT 5000)`, now.Add(-r.History), LedgerActions); err != nil {
			return out, err
		}
	}
	if r.DecidedRequests > 0 {
		if out.Requests, err = loop(`DELETE FROM access_requests WHERE id IN (SELECT r.id FROM access_requests r WHERE r.state <> 'pending' AND r.decided_at < $1
AND NOT EXISTS (SELECT 1 FROM approvals a WHERE a.request_id = r.id) LIMIT 5000)`, now.Add(-r.DecidedRequests)); err != nil {
			return out, err
		}
	}
	if r.ProxySummaries > 0 {
		if out.ProxySummaries, err = loop(`DELETE FROM proxy_requests WHERE id IN (SELECT id FROM proxy_requests WHERE occurred_at < $1 LIMIT 5000)`, now.Add(-r.ProxySummaries)); err != nil {
			return out, err
		}
	}
	return out, nil
}
