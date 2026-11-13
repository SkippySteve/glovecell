package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/store"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/store/storetest"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestMain(m *testing.M) { os.Exit(storetest.Main(m)) }

const (
	dbOrigin  = "https://projects.example.com"
	dbToken   = "automation-token-0123456789abcdef0123"
	projectA  = types.UID("aaaaaaaa-1111-2222-3333-444444444444")
	projectB  = types.UID("bbbbbbbb-1111-2222-3333-444444444444")
	approverG = "agent-project-approvers"
)

type dbFixture struct {
	t       *testing.T
	h       http.Handler
	s       *Server
	store   store.Store
	monitor *store.Monitor
}

// newDBFixture serves two projects with OIDC sessions for an approver
// ("alice", in the approver group) and a signed-in operator without it
// ("victor"), plus the automation token and static login.
func newDBFixture(t *testing.T, st store.Store, approverGroups ...string) *dbFixture {
	t.Helper()
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = api.AddToScheme(scheme)
	ws := func(name string, uid types.UID) *api.ProjectWorkspace {
		return &api.ProjectWorkspace{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "control", UID: uid}, Spec: api.WorkspaceSpec{Repository: "steve/" + name, DesiredState: "Running"}}
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(ws("project-a", projectA), ws("project-b", projectB)).Build()
	expires := time.Now().Add(time.Hour)
	group := ""
	if len(approverGroups) > 0 {
		group = approverGroups[0]
	}
	o := &OIDC{config: OIDCConfig{Issuer: "https://id.example.com", ApproverGroups: approverGroups}, transactions: map[string]transaction{}, sessions: map[string]session{
		"sid-alice":  {Subject: "https://id.example.com#alice", Expires: expires, ApproverGroup: group},
		"sid-victor": {Subject: "https://id.example.com#victor", Expires: expires},
	}}
	f := &dbFixture{t: t, store: st}
	if st != nil {
		f.monitor = store.NewMonitor(st, nil)
		f.monitor.Step(context.Background())
	}
	f.s = &Server{Client: c, Namespace: "control", Owner: "steve", Host: host{}, Origin: dbOrigin, Password: dbToken, AllowBasic: true, OIDC: o}
	if st != nil {
		f.s.Database = &Database{Store: st, Monitor: f.monitor}
	}
	h, err := f.s.Handler()
	if err != nil {
		t.Fatal(err)
	}
	f.h = h
	return f
}

// do sends a request as "alice", "victor", "bearer" or "basic".
func (f *dbFixture) do(as, method, path, body string) *httptest.ResponseRecorder {
	f.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if method != http.MethodGet {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", dbOrigin)
	}
	switch as {
	case "alice", "victor":
		req.AddCookie(&http.Cookie{Name: "__Host-apc-session", Value: "sid-" + as})
	case "bearer":
		req.Header.Set("Authorization", "Bearer "+dbToken)
	case "basic":
		req.SetBasicAuth("operator", dbToken)
	}
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	return rec
}

func (f *dbFixture) createRequest(as, project, path string) store.AccessRequest {
	f.t.Helper()
	rec := f.do(as, "POST", "/api/v1/workspaces/"+project+"/access-requests", `{"scheme":"https","host":"Registry.NPMJS.org","method":"get","path":"`+path+`","reason":"install deps"}`)
	if rec.Code != http.StatusCreated {
		f.t.Fatalf("create request: %d %s", rec.Code, rec.Body.String())
	}
	var r store.AccessRequest
	if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil {
		f.t.Fatal(err)
	}
	return r
}

func requestPath(project string, id int64, suffix string) string {
	return "/api/v1/workspaces/" + project + "/access-requests/" + itoa(id) + suffix
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func dbBackends(t *testing.T, fn func(t *testing.T, st store.Store)) {
	t.Run("memory", func(t *testing.T) { fn(t, store.NewMemory()) })
	t.Run("postgres", func(t *testing.T) { fn(t, storetest.New(t)) })
}

func TestDecisionsNeedTheApproverGroup(t *testing.T) {
	dbBackends(t, func(t *testing.T, st store.Store) {
		f := newDBFixture(t, st, approverG)
		r := f.createRequest("victor", "project-a", "/left-pad")
		if r.Target.Host != "registry.npmjs.org" || r.Target.Method != "GET" || r.Target.Port != 443 || r.RequestedBy != "https://id.example.com#victor" {
			t.Fatalf("request normalised: %+v", r)
		}
		decision := `{"decision":"approve","reason":"needed"}`
		for _, as := range []string{"victor", "bearer", "basic", ""} {
			rec := f.do(as, "POST", requestPath("project-a", r.ID, "/decision"), decision)
			want := http.StatusForbidden
			if as == "" {
				want = http.StatusUnauthorized
			}
			if rec.Code != want {
				t.Fatalf("%q decided: %d %s", as, rec.Code, rec.Body.String())
			}
		}
		if got, _ := st.Request(context.Background(), string(projectA), r.ID); got.State != store.StatePending {
			t.Fatalf("refused decisions changed the request: %+v", got)
		}
		// The approver's decision through another project's path is not found.
		if rec := f.do("alice", "POST", requestPath("project-b", r.ID, "/decision"), decision); rec.Code != http.StatusNotFound {
			t.Fatalf("cross-project decision: %d", rec.Code)
		}
		// Same-origin is still required for a session.
		req := httptest.NewRequest("POST", requestPath("project-a", r.ID, "/decision"), strings.NewReader(decision))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(&http.Cookie{Name: "__Host-apc-session", Value: "sid-alice"})
		rec := httptest.NewRecorder()
		f.h.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("decision without Origin: %d", rec.Code)
		}
		rec = f.do("alice", "POST", requestPath("project-a", r.ID, "/decision"), decision)
		if rec.Code != http.StatusOK {
			t.Fatalf("approver decision: %d %s", rec.Code, rec.Body.String())
		}
		var out struct {
			Request  store.AccessRequest `json:"request"`
			Approval *store.Approval     `json:"approval"`
		}
		json.Unmarshal(rec.Body.Bytes(), &out)
		if out.Request.State != store.StateApproved || out.Approval == nil || out.Request.DecidedBy != "https://id.example.com#alice" {
			t.Fatalf("decision: %s", rec.Body.String())
		}
		if rec := f.do("alice", "POST", requestPath("project-a", r.ID, "/decision"), `{"decision":"deny"}`); rec.Code != http.StatusConflict {
			t.Fatalf("second decision: %d", rec.Code)
		}
		// Recorded with the approver and group, and visible in History.
		hist := f.do("victor", "GET", "/api/v1/workspaces/project-a/audit?action=request-", "")
		var items []map[string]string
		json.Unmarshal(hist.Body.Bytes(), &items)
		if hist.Code != 200 || len(items) != 1 || items[0]["action"] != store.ActionRequestApproved || items[0]["actor"] != "https://id.example.com#alice" || items[0]["approverGroup"] != approverG || items[0]["reason"] != "needed" {
			t.Fatalf("decision history: %d %s", hist.Code, hist.Body.String())
		}
		// Revocation is approver-only too.
		approvals := f.do("victor", "GET", "/api/v1/workspaces/project-a/approvals", "")
		if approvals.Code != 200 || !strings.Contains(approvals.Body.String(), `"stale":false`) || !strings.Contains(approvals.Body.String(), "/left-pad") {
			t.Fatalf("approvals: %d %s", approvals.Code, approvals.Body.String())
		}
		revoke := "/api/v1/workspaces/project-a/approvals/" + itoa(out.Approval.ID)
		for _, as := range []string{"victor", "bearer", "basic"} {
			if rec := f.do(as, "DELETE", revoke, ""); rec.Code != http.StatusForbidden {
				t.Fatalf("%s revoked: %d", as, rec.Code)
			}
		}
		if rec := f.do("alice", "DELETE", revoke, `{"reason":"done"}`); rec.Code != http.StatusOK {
			t.Fatalf("revoke: %d %s", rec.Code, rec.Body.String())
		}
		if rec := f.do("alice", "DELETE", revoke, ""); rec.Code != http.StatusConflict {
			t.Fatalf("second revoke: %d", rec.Code)
		}
		if rec := f.do("victor", "GET", "/api/v1/workspaces/project-a/approvals?include=revoked", ""); !strings.Contains(rec.Body.String(), `"revokedBy":"https://id.example.com#alice"`) {
			t.Fatalf("revoked list: %s", rec.Body.String())
		}
		// Denying works the same way and creates no approval.
		r2 := f.createRequest("bearer", "project-a", "/right-pad")
		if rec := f.do("alice", "POST", requestPath("project-a", r2.ID, "/decision"), `{"decision":"deny"}`); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"approval":null`) {
			t.Fatalf("deny: %d %s", rec.Code, rec.Body.String())
		}
		if rec := f.do("victor", "GET", "/api/v1/workspaces/project-a/access-requests?state=denied", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), "/right-pad") || strings.Contains(rec.Body.String(), "/left-pad") {
			t.Fatalf("denied list: %d %s", rec.Code, rec.Body.String())
		}
		// Session reports approver rights.
		if rec := f.do("alice", "GET", "/api/v1/session", ""); !strings.Contains(rec.Body.String(), `"approver":true`) {
			t.Fatalf("session: %s", rec.Body.String())
		}
		if rec := f.do("victor", "GET", "/api/v1/session", ""); !strings.Contains(rec.Body.String(), `"approver":false`) {
			t.Fatalf("session: %s", rec.Body.String())
		}
	})
}

func TestNoApproverGroupConfiguredRefusesEveryone(t *testing.T) {
	f := newDBFixture(t, store.NewMemory())
	r := f.createRequest("alice", "project-a", "/x")
	rec := f.do("alice", "POST", requestPath("project-a", r.ID, "/decision"), `{"decision":"approve"}`)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "OIDC_APPROVER_GROUPS") {
		t.Fatalf("approval without configured approvers: %d %s", rec.Code, rec.Body.String())
	}
}

func TestDatabaseDownFailsClosed(t *testing.T) {
	m := store.NewMemory()
	f := newDBFixture(t, m, approverG)
	r := f.createRequest("victor", "project-a", "/a")
	if rec := f.do("alice", "POST", requestPath("project-a", r.ID, "/decision"), `{"decision":"approve"}`); rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	pending := f.createRequest("victor", "project-a", "/b")
	// The console read the active approvals once (its snapshot).
	if rec := f.do("victor", "GET", "/api/v1/workspaces/project-a/approvals", ""); rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	m.SetDown(true)
	f.monitor.Step(context.Background())
	for name, rec := range map[string]*httptest.ResponseRecorder{
		"create":  f.do("victor", "POST", "/api/v1/workspaces/project-a/access-requests", `{"scheme":"https","host":"example.com","method":"GET","path":"/c"}`),
		"approve": f.do("alice", "POST", requestPath("project-a", pending.ID, "/decision"), `{"decision":"approve"}`),
		"deny":    f.do("alice", "POST", requestPath("project-a", pending.ID, "/decision"), `{"decision":"deny"}`),
		"revoke":  f.do("alice", "DELETE", "/api/v1/workspaces/project-a/approvals/1", ""),
		"list":    f.do("victor", "GET", "/api/v1/workspaces/project-a/access-requests", ""),
		"cold":    f.do("victor", "GET", "/api/v1/workspaces/project-b/approvals", ""),
	} {
		if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "can't be saved and are refused") {
			t.Errorf("%s with the database down: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	// Approvals already granted are still listed from the snapshot, marked
	// stale: that is what proxies keep enforcing.
	rec := f.do("victor", "GET", "/api/v1/workspaces/project-a/approvals", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"stale":true`) || !strings.Contains(rec.Body.String(), "/a") {
		t.Fatalf("cached approvals: %d %s", rec.Code, rec.Body.String())
	}
	status := f.do("victor", "GET", "/api/v1/database", "")
	if !strings.Contains(status.Body.String(), `"ready":false`) || !strings.Contains(status.Body.String(), "Proxies keep enforcing") {
		t.Fatalf("database status: %s", status.Body.String())
	}
	if rec := f.do("victor", "GET", "/api/v1/session", ""); !strings.Contains(rec.Body.String(), `"ready":false`) {
		t.Fatalf("session database state: %s", rec.Body.String())
	}
	// Nothing changed while refused; recovery accepts decisions again.
	m.SetDown(false)
	f.monitor.Step(context.Background())
	if got, _ := m.Request(context.Background(), string(projectA), pending.ID); got.State != store.StatePending {
		t.Fatalf("refused decision changed state: %+v", got)
	}
	if rec := f.do("alice", "POST", requestPath("project-a", pending.ID, "/decision"), `{"decision":"approve"}`); rec.Code != 200 {
		t.Fatalf("after recovery: %d %s", rec.Code, rec.Body.String())
	}
}

func TestNoDatabaseConfiguredExplains(t *testing.T) {
	f := newDBFixture(t, nil, approverG)
	for _, p := range []string{"/api/v1/workspaces/project-a/access-requests", "/api/v1/workspaces/project-a/approvals", "/api/v1/workspaces/project-a/proxy-requests"} {
		if rec := f.do("victor", "GET", p, ""); rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "No database is configured") {
			t.Fatalf("%s: %d %s", p, rec.Code, rec.Body.String())
		}
	}
	if rec := f.do("victor", "GET", "/api/v1/database", ""); !strings.Contains(rec.Body.String(), `"configured":false`) {
		t.Fatalf("status: %s", rec.Body.String())
	}
}

func TestProxySummariesAndFilters(t *testing.T) {
	m := store.NewMemory()
	f := newDBFixture(t, m, approverG)
	now := time.Now().UTC()
	m.RecordProxySummaries(context.Background(), []store.ProxySummary{
		{WorkspaceUID: string(projectA), RequestID: "1", Time: now, Target: store.Target{Scheme: "https", Host: "a.example", Port: 443, Method: "GET", Path: "/"}, Decision: store.ProxyDeny},
		{WorkspaceUID: string(projectA), RequestID: "2", Time: now, Target: store.Target{Scheme: "https", Host: "b.example", Port: 443, Method: "GET", Path: "/"}, Decision: store.ProxyAllow},
	})
	rec := f.do("victor", "GET", "/api/v1/workspaces/project-a/proxy-requests?decision=deny", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "a.example") || strings.Contains(rec.Body.String(), "b.example") {
		t.Fatalf("proxy summaries: %d %s", rec.Code, rec.Body.String())
	}
	for _, bad := range []string{"?decision=maybe", "?limit=0", "?limit=5000", "?since=yesterday"} {
		if rec := f.do("victor", "GET", "/api/v1/workspaces/project-a/proxy-requests"+bad, ""); rec.Code != 400 {
			t.Fatalf("%s accepted: %d", bad, rec.Code)
		}
	}
	if rec := f.do("victor", "GET", "/api/v1/workspaces/project-a/audit?limit=nope", ""); rec.Code != 400 {
		t.Fatalf("bad history filter: %d", rec.Code)
	}
}

func TestApproverGroupFromClaims(t *testing.T) {
	o := &OIDC{config: OIDCConfig{GroupsClaim: "groups", ApproverGroupsClaim: "roles", ApproverGroups: []string{"approvers", "admins"}}}
	for _, c := range []struct {
		claims map[string]any
		want   string
	}{
		{map[string]any{"roles": []any{"viewers", "admins"}}, "admins"},
		{map[string]any{"roles": "approvers"}, "approvers"},
		{map[string]any{"groups": []any{"approvers"}}, ""},
		{map[string]any{"roles": []any{1, "", "Approvers"}}, ""},
		{map[string]any{}, ""},
	} {
		if got := o.approverGroup(c.claims); got != c.want {
			t.Errorf("%v: %q, want %q", c.claims, got, c.want)
		}
	}
	if (&OIDC{}).Approvers() || (*OIDC)(nil).Approvers() || !o.Approvers() {
		t.Fatal("Approvers()")
	}
}
