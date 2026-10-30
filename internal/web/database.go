package web

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
	"k8s.io/apimachinery/pkg/types"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/store"
)

// Database is the console's view of the controller database (D13 item 2):
// access requests, exact approvals and proxy summaries. Nil turns the
// endpoints into refusals that explain no database is configured.
type Database struct {
	Store   store.Store
	Monitor *store.Monitor

	mu sync.Mutex
	// cache holds the last active-approval snapshot read per workspace UID,
	// served (marked stale) while the database is down.
	cache map[string]approvalSnapshot
}

type approvalSnapshot struct {
	items []store.Approval
	at    time.Time
}

// Messages the console shows. Writes fail closed: nothing is granted
// without a stored decision.
const (
	noDatabaseMessage = "No database is configured, so approvals and access requests are turned off. See docs/database.md."
	dbDownMessage     = "The database is unavailable, so new approvals and access requests can't be saved and are refused. Proxies keep enforcing the approvals they already have."
)

var decisions, _ = otel.Meter("agent-project-controller").Int64Counter("apc.access.decisions", metric.WithDescription("Access request decisions and approval revocations"))

func (d *Database) status() store.Status {
	if d == nil {
		return (*store.Monitor)(nil).Status()
	}
	return d.Monitor.Status()
}

// storeError maps store errors to responses. ErrUnavailable explains the
// fail-closed state.
func storeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrUnavailable):
		http.Error(w, dbDownMessage, http.StatusServiceUnavailable)
	case errors.Is(err, store.ErrNotFound):
		http.Error(w, "not found", http.StatusNotFound)
	case errors.Is(err, store.ErrConflict):
		http.Error(w, "already decided or revoked; refresh", http.StatusConflict)
	case errors.Is(err, store.ErrLimit):
		http.Error(w, "limit reached: "+strings.TrimPrefix(err.Error(), store.ErrLimit.Error()+": "), http.StatusTooManyRequests)
	case errors.Is(err, store.ErrInvalid):
		http.Error(w, strings.TrimPrefix(err.Error(), store.ErrInvalid.Error()+": "), http.StatusBadRequest)
	default:
		slog.Error("database request failed", "error", err.Error())
		http.Error(w, "database error", http.StatusInternalServerError)
	}
}

// databaseWorkspace resolves {name} and refuses when no database is set.
func (s *Server) databaseWorkspace(w http.ResponseWriter, r *http.Request) (*api.ProjectWorkspace, bool) {
	if s.Database == nil || s.Database.Store == nil {
		http.Error(w, noDatabaseMessage, http.StatusServiceUnavailable)
		return nil, false
	}
	obj := &api.ProjectWorkspace{}
	if e := s.Client.Get(r.Context(), types.NamespacedName{Namespace: s.Namespace, Name: r.PathValue("name")}, obj); e != nil || obj.UID == "" {
		http.Error(w, "project unavailable", http.StatusNotFound)
		return nil, false
	}
	return obj, true
}

// requireApprover allows the request only from an OIDC session in an
// approver group, and returns the actor and group to record.
func (s *Server) requireApprover(w http.ResponseWriter, r *http.Request) (actor, group string, ok bool) {
	actor, _ = r.Context().Value(actorKey{}).(string)
	group, _ = r.Context().Value(approverKey{}).(string)
	method, _ := r.Context().Value(authKey{}).(string)
	if method != "oidc" || group == "" || actor == "" {
		reason := "approving needs an OIDC sign-in in an approver group"
		if !s.OIDC.Approvers() {
			reason = "no approver group is configured (OIDC_APPROVER_GROUPS), so nobody can approve"
		}
		slog.WarnContext(r.Context(), "access decision refused", "actor", actor, "auth", method, "path", r.URL.Path, "reason", reason)
		http.Error(w, reason, http.StatusForbidden)
		return "", "", false
	}
	return actor, group, true
}

func pathID(r *http.Request, key string) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue(key), 10, 64)
	return id, err == nil && id > 0
}

func queryLimit(r *http.Request) (int, bool) {
	v := r.URL.Query().Get("limit")
	if v == "" {
		return 0, true
	}
	n, err := strconv.Atoi(v)
	return n, err == nil && n > 0 && n <= store.MaxPageSize
}

func queryBefore(r *http.Request) (int64, bool) {
	v := r.URL.Query().Get("before")
	if v == "" {
		return 0, true
	}
	n, err := strconv.ParseInt(v, 10, 64)
	return n, err == nil && n > 0
}

// databaseStatus serves GET /api/v1/database.
func (s *Server) databaseStatus(w http.ResponseWriter, _ *http.Request) {
	st := s.Database.status()
	out := map[string]any{"configured": st.Configured, "ready": st.Ready, "reason": st.Reason, "approvers": s.OIDC.Approvers()}
	if !st.Since.IsZero() {
		out["since"] = st.Since.Format(time.RFC3339)
	}
	if st.Configured && !st.Ready {
		out["message"] = dbDownMessage
	} else if !st.Configured {
		out["message"] = noDatabaseMessage
	}
	writeJSON(w, out)
}

// listRequests serves GET /api/v1/workspaces/{name}/access-requests
// (?state=pending|approved|denied&host=&before=<id>&limit=).
func (s *Server) listRequests(w http.ResponseWriter, r *http.Request) {
	obj, ok := s.databaseWorkspace(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := store.RequestFilter{State: q.Get("state"), Host: q.Get("host")}
	var okLimit, okBefore bool
	f.Limit, okLimit = queryLimit(r)
	f.BeforeID, okBefore = queryBefore(r)
	if !okLimit || !okBefore || f.State != "" && f.State != store.StatePending && f.State != store.StateApproved && f.State != store.StateDenied {
		http.Error(w, "invalid filter", http.StatusBadRequest)
		return
	}
	items, err := s.Database.Store.Requests(r.Context(), string(obj.UID), f)
	if err != nil {
		storeError(w, err)
		return
	}
	writeJSON(w, map[string]any{"requests": items})
}

// requestBody is one exact request as the console or a proxy reports it.
type requestBody struct {
	Scheme      string         `json:"scheme"`
	Host        string         `json:"host"`
	Port        int            `json:"port"`
	Method      string         `json:"method"`
	Path        string         `json:"path"`
	Fingerprint string         `json:"fingerprint,omitempty"`
	Package     *store.Package `json:"package,omitempty"`
	Reason      string         `json:"reason,omitempty"`
}

// createRequest serves POST /api/v1/workspaces/{name}/access-requests. A
// pending request grants nothing; while the database is down it cannot be
// recorded, so it is refused.
func (s *Server) createRequest(w http.ResponseWriter, r *http.Request) {
	obj, ok := s.databaseWorkspace(w, r)
	if !ok {
		return
	}
	var body requestBody
	if e := decode(w, r, &body); e != nil {
		http.Error(w, "invalid access request", http.StatusBadRequest)
		return
	}
	if !obj.DeletionTimestamp.IsZero() || obj.Spec.DesiredState == "Removed" {
		http.Error(w, "project is being removed", http.StatusConflict)
		return
	}
	actor, _ := r.Context().Value(actorKey{}).(string)
	target := store.Target{Scheme: strings.ToLower(body.Scheme), Host: strings.ToLower(body.Host), Port: body.Port, Method: strings.ToUpper(body.Method), Path: body.Path, Fingerprint: body.Fingerprint, Package: body.Package}
	if target.Port == 0 {
		target.Port = map[string]int{"https": 443, "http": 80}[target.Scheme]
	}
	req, created, err := s.Database.Store.CreateRequest(r.Context(), store.NewRequest{WorkspaceUID: string(obj.UID), Workspace: obj.Name, Target: target, Reason: body.Reason, RequestedBy: actor})
	if err != nil {
		storeError(w, err)
		return
	}
	if created {
		w.WriteHeader(http.StatusCreated)
	}
	writeJSON(w, req)
}

// decide serves POST /api/v1/workspaces/{name}/access-requests/{id}/decision
// with {"decision": "approve"|"deny", "reason": ""}. Only an approver may
// decide; the decision, the approver and their group are recorded in the
// same transaction, and sent to OTel.
func (s *Server) decide(w http.ResponseWriter, r *http.Request) {
	obj, ok := s.databaseWorkspace(w, r)
	if !ok {
		return
	}
	actor, group, ok := s.requireApprover(w, r)
	if !ok {
		return
	}
	id, ok := pathID(r, "id")
	var body struct {
		Decision string `json:"decision"`
		Reason   string `json:"reason,omitempty"`
	}
	if e := decode(w, r, &body); e != nil || !ok || body.Decision != "approve" && body.Decision != "deny" {
		http.Error(w, `decision must be "approve" or "deny"`, http.StatusBadRequest)
		return
	}
	approve := body.Decision == "approve"
	if approve && (!obj.DeletionTimestamp.IsZero() || obj.Spec.DesiredState == "Removed") {
		http.Error(w, "project is being removed", http.StatusConflict)
		return
	}
	ctx, span := otel.Tracer("agent-project-controller").Start(r.Context(), "access.decision", trace.WithAttributes(
		attribute.String("workspace_uid", string(obj.UID)), attribute.Int64("request_id", id), attribute.String("decision", body.Decision),
		attribute.String("actor", actor), attribute.String("approver_group", group)))
	defer span.End()
	req, approval, err := s.Database.Store.Decide(ctx, store.Decision{WorkspaceUID: string(obj.UID), RequestID: id, Approve: approve, Actor: actor, Group: group, Reason: body.Reason})
	outcome := "recorded"
	if err != nil {
		outcome = "refused"
	}
	decisions.Add(ctx, 1, metric.WithAttributes(attribute.String("decision", body.Decision), attribute.String("outcome", outcome)))
	if err != nil {
		slog.WarnContext(ctx, "access decision not recorded", "workspace_uid", string(obj.UID), "request", id, "decision", body.Decision, "actor", actor, "error", err.Error())
		storeError(w, err)
		return
	}
	slog.InfoContext(ctx, "access decision recorded", "workspace", obj.Name, "workspace_uid", string(obj.UID), "request", id, "decision", body.Decision,
		"actor", actor, "approver_group", group, "host", req.Target.Host, "method", req.Target.Method, "path", req.Target.Path, "trace_id", span.SpanContext().TraceID().String())
	s.Database.refresh(ctx, string(obj.UID))
	writeJSON(w, map[string]any{"request": req, "approval": approval})
}

// listApprovals serves GET /api/v1/workspaces/{name}/approvals
// (?include=revoked&host=&before=<id>&limit=). The plain call returns the
// active set; while the database is down it returns the last snapshot this
// controller read, with "stale": true, so operators see what proxies are
// still enforcing.
func (s *Server) listApprovals(w http.ResponseWriter, r *http.Request) {
	obj, ok := s.databaseWorkspace(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	limit, okLimit := queryLimit(r)
	before, okBefore := queryBefore(r)
	include := q.Get("include")
	if !okLimit || !okBefore || include != "" && include != "revoked" {
		http.Error(w, "invalid filter", http.StatusBadRequest)
		return
	}
	uid := string(obj.UID)
	if include == "" && q.Get("host") == "" && limit == 0 && before == 0 {
		items, err := s.Database.Store.ActiveApprovals(r.Context(), uid)
		if err == nil {
			s.Database.remember(uid, items)
			writeJSON(w, map[string]any{"approvals": items, "stale": false})
			return
		}
		if errors.Is(err, store.ErrUnavailable) {
			if snap, ok := s.Database.cached(uid); ok {
				writeJSON(w, map[string]any{"approvals": snap.items, "stale": true, "cachedAt": snap.at.Format(time.RFC3339), "message": dbDownMessage})
				return
			}
		}
		storeError(w, err)
		return
	}
	items, err := s.Database.Store.Approvals(r.Context(), uid, store.ApprovalFilter{IncludeRevoked: include == "revoked", Host: q.Get("host"), BeforeID: before, Limit: limit})
	if err != nil {
		storeError(w, err)
		return
	}
	writeJSON(w, map[string]any{"approvals": items, "stale": false})
}

// revokeApproval serves DELETE /api/v1/workspaces/{name}/approvals/{id},
// with an optional {"reason": ""} body. Approvers only; recorded.
func (s *Server) revokeApproval(w http.ResponseWriter, r *http.Request) {
	obj, ok := s.databaseWorkspace(w, r)
	if !ok {
		return
	}
	actor, group, ok := s.requireApprover(w, r)
	if !ok {
		return
	}
	id, ok := pathID(r, "id")
	var body struct {
		Reason string `json:"reason,omitempty"`
	}
	if e := decode(w, r, &body); e != nil && !errors.Is(e, io.EOF) || !ok {
		http.Error(w, "invalid revocation", http.StatusBadRequest)
		return
	}
	ctx, span := otel.Tracer("agent-project-controller").Start(r.Context(), "access.revoke", trace.WithAttributes(
		attribute.String("workspace_uid", string(obj.UID)), attribute.Int64("approval_id", id), attribute.String("actor", actor), attribute.String("approver_group", group)))
	defer span.End()
	a, err := s.Database.Store.RevokeApproval(ctx, store.Revocation{WorkspaceUID: string(obj.UID), ApprovalID: id, Actor: actor, Group: group, Reason: body.Reason})
	outcome := "recorded"
	if err != nil {
		outcome = "refused"
	}
	decisions.Add(ctx, 1, metric.WithAttributes(attribute.String("decision", "revoke"), attribute.String("outcome", outcome)))
	if err != nil {
		storeError(w, err)
		return
	}
	slog.InfoContext(ctx, "approval revoked", "workspace", obj.Name, "workspace_uid", string(obj.UID), "approval", id, "actor", actor, "approver_group", group, "host", a.Target.Host)
	s.Database.refresh(ctx, string(obj.UID))
	writeJSON(w, a)
}

// listProxyRequests serves GET /api/v1/workspaces/{name}/proxy-requests
// (?decision=allow|deny&host=&since=&before=&limit=).
func (s *Server) listProxyRequests(w http.ResponseWriter, r *http.Request) {
	obj, ok := s.databaseWorkspace(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := store.ProxyFilter{Decision: q.Get("decision"), Host: q.Get("host")}
	var okLimit bool
	f.Limit, okLimit = queryLimit(r)
	valid := okLimit && (f.Decision == "" || f.Decision == store.ProxyAllow || f.Decision == store.ProxyDeny)
	for key, dst := range map[string]*time.Time{"since": &f.Since, "before": &f.Before} {
		if v := q.Get(key); v != "" {
			t, err := time.Parse(time.RFC3339Nano, v)
			valid = valid && err == nil
			*dst = t
		}
	}
	if !valid {
		http.Error(w, "invalid filter", http.StatusBadRequest)
		return
	}
	items, err := s.Database.Store.ProxySummaries(r.Context(), string(obj.UID), f)
	if err != nil {
		storeError(w, err)
		return
	}
	writeJSON(w, map[string]any{"requests": items})
}

func (d *Database) remember(uid string, items []store.Approval) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cache == nil {
		d.cache = map[string]approvalSnapshot{}
	}
	d.cache[uid] = approvalSnapshot{items: items, at: time.Now().UTC()}
}

func (d *Database) cached(uid string) (approvalSnapshot, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	snap, ok := d.cache[uid]
	return snap, ok
}

// refresh re-reads the active set after a write, best effort.
func (d *Database) refresh(ctx context.Context, uid string) {
	if items, err := d.Store.ActiveApprovals(ctx, uid); err == nil {
		d.remember(uid, items)
	}
}
