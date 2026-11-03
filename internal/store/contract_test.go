package store_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/store"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/store/storetest"
)

func TestMain(m *testing.M) { os.Exit(storetest.Main(m)) }

// backends runs one contract against the fake and against PostgreSQL.
func backends(t *testing.T, fn func(t *testing.T, s store.Store)) {
	t.Run("memory", func(t *testing.T) { fn(t, store.NewMemory()) })
	t.Run("postgres", func(t *testing.T) { fn(t, storetest.New(t)) })
}

var ctx = context.Background()

func target(path string) store.Target {
	return store.Target{Scheme: "https", Host: "registry.npmjs.org", Port: 443, Method: "GET", Path: path, Fingerprint: "sha256:00"}
}

func newRequest(uid, path string) store.NewRequest {
	return store.NewRequest{WorkspaceUID: uid, Workspace: "demo", Target: target(path), Reason: "install", RequestedBy: "agent"}
}

func event(uid, key, action string, at time.Time) store.Event {
	return store.Event{Key: key, WorkspaceUID: uid, Workspace: "demo", Generation: 1, Action: action, Actor: "operator", Message: "m " + key, Time: at, Details: map[string]string{"k": key}}
}

func TestContractHistory(t *testing.T) {
	backends(t, func(t *testing.T, s store.Store) {
		base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
		for i, a := range []string{"grant-requested", "access-granted", "phase-ready", "lan-host-added"} {
			ok, err := s.AppendEvent(ctx, event("uid-a", fmt.Sprintf("k%d", i), a, base.Add(time.Duration(i)*time.Minute)))
			if err != nil || !ok {
				t.Fatalf("append %d: %v %v", i, ok, err)
			}
		}
		// Same key again: idempotent, nothing inserted.
		if ok, err := s.AppendEvent(ctx, event("uid-a", "k0", "grant-requested", base.Add(time.Hour))); err != nil || ok {
			t.Fatalf("duplicate key inserted: %v %v", ok, err)
		}
		if _, err := s.AppendEvent(ctx, event("uid-b", "other", "phase-ready", base)); err != nil {
			t.Fatal(err)
		}
		if _, err := s.AppendEvent(ctx, store.Event{Key: "bad"}); !errors.Is(err, store.ErrInvalid) {
			t.Fatalf("invalid event accepted: %v", err)
		}
		all, err := s.Events(ctx, "uid-a", store.HistoryFilter{})
		if err != nil || len(all) != 4 || all[0].Action != "lan-host-added" || all[3].Action != "grant-requested" {
			t.Fatalf("events newest first: %+v %v", all, err)
		}
		if all[0].Details["k"] != "k3" || all[0].Workspace != "demo" || all[0].Generation != 1 || !all[0].Time.Equal(base.Add(3*time.Minute)) || all[0].Source != "controller" {
			t.Fatalf("event fields lost: %+v", all[0])
		}
		for name, c := range map[string]struct {
			f    store.HistoryFilter
			want int
		}{
			"prefix":          {store.HistoryFilter{ActionPrefix: "access-"}, 1},
			"like-is-literal": {store.HistoryFilter{ActionPrefix: "access_"}, 0},
			"actor":           {store.HistoryFilter{Actor: "operator"}, 4},
			"since":           {store.HistoryFilter{Since: base.Add(2 * time.Minute)}, 2},
			"before":          {store.HistoryFilter{Before: base.Add(2 * time.Minute)}, 2},
			"limit":           {store.HistoryFilter{Limit: 3}, 3},
			"nobody":          {store.HistoryFilter{Actor: "someone"}, 0},
		} {
			got, err := s.Events(ctx, "uid-a", c.f)
			if err != nil || len(got) != c.want {
				t.Fatalf("%s: got %d (%v), want %d", name, len(got), err, c.want)
			}
		}
		if ok, err := s.HasAction(ctx, "uid-a", "access-granted", "nothing"); err != nil || !ok {
			t.Fatalf("HasAction: %v %v", ok, err)
		}
		if ok, err := s.HasAction(ctx, "uid-b", "access-granted"); err != nil || ok {
			t.Fatalf("HasAction crossed workspaces: %v %v", ok, err)
		}
	})
}

func TestContractRequestsAndDecisions(t *testing.T) {
	backends(t, func(t *testing.T, s store.Store) {
		r1, created, err := s.CreateRequest(ctx, newRequest("uid-a", "/left-pad"))
		if err != nil || !created || r1.State != store.StatePending || r1.Observations != 1 {
			t.Fatalf("create: %+v %v %v", r1, created, err)
		}
		again, created, err := s.CreateRequest(ctx, newRequest("uid-a", "/left-pad"))
		if err != nil || created || again.ID != r1.ID || again.Observations != 2 {
			t.Fatalf("repeat observation: %+v %v %v", again, created, err)
		}
		r2, _, err := s.CreateRequest(ctx, newRequest("uid-a", "/right-pad"))
		if err != nil {
			t.Fatal(err)
		}
		bad := newRequest("uid-a", "/x?q=1")
		if _, _, err := s.CreateRequest(ctx, bad); !errors.Is(err, store.ErrInvalid) {
			t.Fatalf("query in path accepted: %v", err)
		}
		bad = newRequest("uid-a", "/x")
		bad.Target.Host = "Registry.npmjs.org"
		if _, _, err := s.CreateRequest(ctx, bad); !errors.Is(err, store.ErrInvalid) {
			t.Fatalf("upper-case host accepted: %v", err)
		}
		pending, err := s.Requests(ctx, "uid-a", store.RequestFilter{State: store.StatePending})
		if err != nil || len(pending) != 2 || pending[0].ID != r2.ID {
			t.Fatalf("pending newest first: %+v %v", pending, err)
		}
		if got, _ := s.Requests(ctx, "uid-a", store.RequestFilter{BeforeID: r2.ID}); len(got) != 1 || got[0].ID != r1.ID {
			t.Fatalf("paging: %+v", got)
		}
		if got, _ := s.Requests(ctx, "uid-b", store.RequestFilter{}); len(got) != 0 {
			t.Fatalf("requests crossed workspaces: %+v", got)
		}
		// Another workspace cannot decide this request.
		if _, _, err := s.Decide(ctx, store.Decision{WorkspaceUID: "uid-b", RequestID: r1.ID, Approve: true, Actor: "alice"}); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("cross-workspace decision: %v", err)
		}
		if _, _, err := s.Decide(ctx, store.Decision{WorkspaceUID: "uid-a", RequestID: r1.ID, Approve: true}); !errors.Is(err, store.ErrInvalid) {
			t.Fatalf("decision without actor: %v", err)
		}
		decided, approval, err := s.Decide(ctx, store.Decision{WorkspaceUID: "uid-a", RequestID: r1.ID, Approve: true, Actor: "alice", Group: "approvers", Reason: "needed"})
		if err != nil || decided.State != store.StateApproved || approval == nil || decided.ApprovalID == nil || *decided.ApprovalID != approval.ID || decided.DecidedBy != "alice" || decided.DecidedAt == nil {
			t.Fatalf("approve: %+v %+v %v", decided, approval, err)
		}
		if approval.Source != store.SourceApproved || approval.CreatedBy != "alice" || approval.RequestID == nil || *approval.RequestID != r1.ID || approval.Target.Path != "/left-pad" {
			t.Fatalf("approval fields: %+v", approval)
		}
		if _, _, err := s.Decide(ctx, store.Decision{WorkspaceUID: "uid-a", RequestID: r1.ID, Approve: false, Actor: "bob"}); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("second decision accepted: %v", err)
		}
		denied, none, err := s.Decide(ctx, store.Decision{WorkspaceUID: "uid-a", RequestID: r2.ID, Approve: false, Actor: "bob", Group: "approvers"})
		if err != nil || denied.State != store.StateDenied || none != nil || denied.ApprovalID != nil {
			t.Fatalf("deny: %+v %+v %v", denied, none, err)
		}
		// The same target asked again after approval: a new pending request,
		// and approving it reuses the active approval.
		r3, created, err := s.CreateRequest(ctx, newRequest("uid-a", "/left-pad"))
		if err != nil || !created || r3.ID == r1.ID {
			t.Fatalf("new request after decision: %+v %v %v", r3, created, err)
		}
		_, reused, err := s.Decide(ctx, store.Decision{WorkspaceUID: "uid-a", RequestID: r3.ID, Approve: true, Actor: "alice"})
		if err != nil || reused.ID != approval.ID {
			t.Fatalf("approval not reused: %+v %v", reused, err)
		}
		active, err := s.ActiveApprovals(ctx, "uid-a")
		if err != nil || len(active) != 1 {
			t.Fatalf("active approvals: %+v %v", active, err)
		}
		// Every decision is in history, with the approver group.
		hist, err := s.Events(ctx, "uid-a", store.HistoryFilter{ActionPrefix: "request-"})
		if err != nil || len(hist) != 3 {
			t.Fatalf("decision history: %+v %v", hist, err)
		}
		seen := map[string]bool{}
		for _, e := range hist {
			seen[e.Action+"/"+e.Actor] = true
			if e.Details["request"] == "" || e.Details["host"] != "registry.npmjs.org" {
				t.Fatalf("decision details: %+v", e)
			}
		}
		if !seen["request-approved/alice"] || !seen["request-denied/bob"] {
			t.Fatalf("decisions missing: %v", seen)
		}
		if created, _ := s.Events(ctx, "uid-a", store.HistoryFilter{ActionPrefix: store.ActionRequestCreated}); len(created) != 3 {
			t.Fatalf("request creation history: %+v", created)
		}
	})
}

func TestContractApprovals(t *testing.T) {
	backends(t, func(t *testing.T, s store.Store) {
		r, _, err := s.CreateRequest(ctx, newRequest("uid-a", "/a"))
		if err != nil {
			t.Fatal(err)
		}
		_, a, err := s.Decide(ctx, store.Decision{WorkspaceUID: "uid-a", RequestID: r.ID, Approve: true, Actor: "alice"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.RevokeApproval(ctx, store.Revocation{WorkspaceUID: "uid-b", ApprovalID: a.ID, Actor: "alice"}); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("cross-workspace revocation: %v", err)
		}
		revoked, err := s.RevokeApproval(ctx, store.Revocation{WorkspaceUID: "uid-a", ApprovalID: a.ID, Actor: "carol", Group: "approvers", Reason: "no longer needed"})
		if err != nil || revoked.RevokedAt == nil || revoked.RevokedBy != "carol" || revoked.RevokeReason != "no longer needed" {
			t.Fatalf("revoke: %+v %v", revoked, err)
		}
		if _, err := s.RevokeApproval(ctx, store.Revocation{WorkspaceUID: "uid-a", ApprovalID: a.ID, Actor: "carol"}); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("second revocation: %v", err)
		}
		if active, _ := s.ActiveApprovals(ctx, "uid-a"); len(active) != 0 {
			t.Fatalf("revoked approval still active: %+v", active)
		}
		if all, _ := s.Approvals(ctx, "uid-a", store.ApprovalFilter{IncludeRevoked: true}); len(all) != 1 {
			t.Fatalf("revoked approval not listed: %+v", all)
		}
		if ev, _ := s.Events(ctx, "uid-a", store.HistoryFilter{ActionPrefix: store.ActionApprovalRevoked}); len(ev) != 1 || ev[0].Actor != "carol" || ev[0].Details["approverGroup"] != "approvers" {
			t.Fatalf("revocation history: %+v", ev)
		}
		// Approving the same target again makes a new approval.
		r2, _, _ := s.CreateRequest(ctx, newRequest("uid-a", "/a"))
		_, a2, err := s.Decide(ctx, store.Decision{WorkspaceUID: "uid-a", RequestID: r2.ID, Approve: true, Actor: "alice"})
		if err != nil || a2.ID == a.ID {
			t.Fatalf("re-approval: %+v %v", a2, err)
		}
		pkg := newRequest("uid-a", "/b/-/b-1.0.0.tgz")
		pkg.Target.Package = &store.Package{Ecosystem: "npm", Name: "b", Version: "1.0.0"}
		r3, _, _ := s.CreateRequest(ctx, pkg)
		_, a3, err := s.Decide(ctx, store.Decision{WorkspaceUID: "uid-a", RequestID: r3.ID, Approve: true, Actor: "alice"})
		if err != nil || a3.Target.Package == nil || a3.Target.Package.Version != "1.0.0" {
			t.Fatalf("package approval: %+v %v", a3, err)
		}
		if got, _ := s.Approvals(ctx, "uid-a", store.ApprovalFilter{Limit: 1}); len(got) != 1 || got[0].ID != a3.ID {
			t.Fatalf("approval paging: %+v", got)
		}
	})
}

func TestContractPendingCap(t *testing.T) {
	backends(t, func(t *testing.T, s store.Store) {
		for i := 0; i < store.MaxPendingRequests; i++ {
			if _, _, err := s.CreateRequest(ctx, newRequest("uid-cap", fmt.Sprintf("/p%d", i))); err != nil {
				t.Fatalf("request %d: %v", i, err)
			}
		}
		if _, _, err := s.CreateRequest(ctx, newRequest("uid-cap", "/one-more")); !errors.Is(err, store.ErrLimit) {
			t.Fatalf("cap not enforced: %v", err)
		}
		// A repeat of a pending request is still counted, not refused.
		if r, created, err := s.CreateRequest(ctx, newRequest("uid-cap", "/p0")); err != nil || created || r.Observations != 2 {
			t.Fatalf("repeat at cap: %+v %v %v", r, created, err)
		}
		// Other workspaces are unaffected.
		if _, _, err := s.CreateRequest(ctx, newRequest("uid-other", "/one-more")); err != nil {
			t.Fatal(err)
		}
	})
}

func TestContractProxySummaries(t *testing.T) {
	backends(t, func(t *testing.T, s store.Store) {
		base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
		sum := func(id string, at time.Time, decision, host string) store.ProxySummary {
			tg := target("/x")
			tg.Host = host
			return store.ProxySummary{WorkspaceUID: "uid-a", RequestID: id, Time: at, Target: tg, Decision: decision, Matched: "approval:1", BytesSent: 10, BytesReceived: 20, DurationMillis: 5, Status: 200}
		}
		batch := []store.ProxySummary{sum("r1", base, store.ProxyAllow, "a.example"), sum("r2", base.Add(time.Minute), store.ProxyDeny, "b.example"), sum("r3", base.Add(2*time.Minute), store.ProxyAllow, "b.example")}
		if n, err := s.RecordProxySummaries(ctx, batch); err != nil || n != 3 {
			t.Fatalf("record: %d %v", n, err)
		}
		// Redelivery from a proxy outbox is idempotent.
		if n, err := s.RecordProxySummaries(ctx, batch[:2]); err != nil || n != 0 {
			t.Fatalf("redelivery: %d %v", n, err)
		}
		if _, err := s.RecordProxySummaries(ctx, []store.ProxySummary{sum("r4", base, "maybe", "a.example")}); !errors.Is(err, store.ErrInvalid) {
			t.Fatalf("invalid decision accepted: %v", err)
		}
		all, err := s.ProxySummaries(ctx, "uid-a", store.ProxyFilter{})
		if err != nil || len(all) != 3 || all[0].RequestID != "r3" || all[0].BytesReceived != 20 || all[0].Matched != "approval:1" {
			t.Fatalf("summaries: %+v %v", all, err)
		}
		if got, _ := s.ProxySummaries(ctx, "uid-a", store.ProxyFilter{Decision: store.ProxyDeny}); len(got) != 1 || got[0].RequestID != "r2" {
			t.Fatalf("decision filter: %+v", got)
		}
		if got, _ := s.ProxySummaries(ctx, "uid-a", store.ProxyFilter{Host: "b.example", Since: base.Add(90 * time.Second)}); len(got) != 1 || got[0].RequestID != "r3" {
			t.Fatalf("host and since filter: %+v", got)
		}
	})
}

func TestContractImportsAndRetention(t *testing.T) {
	backends(t, func(t *testing.T, s store.Store) {
		if done, err := s.ImportDone(ctx, "x"); err != nil || done {
			t.Fatalf("import done before mark: %v %v", done, err)
		}
		for i := 0; i < 2; i++ {
			if err := s.MarkImported(ctx, "x", 3); err != nil {
				t.Fatal(err)
			}
		}
		if done, err := s.ImportDone(ctx, "x"); err != nil || !done {
			t.Fatalf("import not done: %v %v", done, err)
		}
		now := time.Now().UTC()
		old := now.Add(-400 * 24 * time.Hour)
		s.AppendEvent(ctx, event("uid-a", "old-phase", "phase-ready", old))
		s.AppendEvent(ctx, event("uid-a", "old-grant", "access-granted", old))
		s.AppendEvent(ctx, event("uid-a", "new-phase", "phase-ready", now))
		s.RecordProxySummaries(ctx, []store.ProxySummary{
			{WorkspaceUID: "uid-a", RequestID: "old", Time: now.Add(-40 * 24 * time.Hour), Target: target("/x"), Decision: store.ProxyAllow},
			{WorkspaceUID: "uid-a", RequestID: "new", Time: now, Target: target("/x"), Decision: store.ProxyAllow},
		})
		denied, _, _ := s.CreateRequest(ctx, newRequest("uid-a", "/denied"))
		s.Decide(ctx, store.Decision{WorkspaceUID: "uid-a", RequestID: denied.ID, Actor: "bob"})
		approved, _, _ := s.CreateRequest(ctx, newRequest("uid-a", "/approved"))
		s.Decide(ctx, store.Decision{WorkspaceUID: "uid-a", RequestID: approved.ID, Approve: true, Actor: "bob"})
		pending, _, _ := s.CreateRequest(ctx, newRequest("uid-a", "/pending"))

		// Decided requests age from their decision time, so prune "later".
		later := now.Add(366 * 24 * time.Hour)
		n, err := s.Prune(ctx, store.Retention{History: 365 * 24 * time.Hour, DecidedRequests: 365 * 24 * time.Hour, ProxySummaries: 30 * 24 * time.Hour}, now)
		if err != nil || n.History != 1 || n.ProxySummaries != 1 {
			t.Fatalf("prune now: %+v %v", n, err)
		}
		if ok, _ := s.HasAction(ctx, "uid-a", "access-granted"); !ok {
			t.Fatal("retention removed a ledger record")
		}
		n, err = s.Prune(ctx, store.Retention{DecidedRequests: 365 * 24 * time.Hour}, later)
		if err != nil || n.Requests != 1 {
			t.Fatalf("prune decided requests: %+v %v", n, err)
		}
		if _, err := s.Request(ctx, "uid-a", denied.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("old denied request kept: %v", err)
		}
		for _, id := range []int64{approved.ID, pending.ID} {
			if _, err := s.Request(ctx, "uid-a", id); err != nil {
				t.Fatalf("request %d pruned (pending, or the source of an approval): %v", id, err)
			}
		}
		if n, err := s.Prune(ctx, store.Retention{}, later); err != nil || n != (store.Pruned{}) {
			t.Fatalf("zero retention pruned: %+v %v", n, err)
		}
	})
}
