package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestBrowserDashboard(t *testing.T) {
	if os.Getenv("APC_BROWSER_TEST") != "true" {
		t.Skip("set APC_BROWSER_TEST=true with node, Playwright and Chromium installed")
	}
	scheme := runtime.NewScheme()
	_ = api.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	blocked := &api.ProjectWorkspace{ObjectMeta: metav1.ObjectMeta{Name: "blocked-project", Namespace: "control", UID: types.UID("blocked"), Generation: 1}, Spec: api.WorkspaceSpec{Repository: "steve/repo", DesiredState: "Running"}, Status: api.WorkspaceStatus{Phase: "Preparing", ObservedGeneration: 1, Conditions: []metav1.Condition{{Type: "Ready", Status: metav1.ConditionFalse, Reason: "PreparationPending", Message: "main branch protection needs operator review: require at least 1 approval (currently 0). Edit Settings > Branches; preparation retries automatically", ObservedGeneration: 1, LastTransitionTime: metav1.Now()}}}}
	paused := &api.ProjectWorkspace{ObjectMeta: metav1.ObjectMeta{Name: "paused-project", Namespace: "control", UID: types.UID("paused"), Generation: 1}, Spec: api.WorkspaceSpec{Repository: "steve/repo", DesiredState: "Paused"}, Status: api.WorkspaceStatus{Phase: "Paused", Namespace: "apc-paused", ObservedGeneration: 1}}
	ledger := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "apc-paused", Namespace: "control", Labels: map[string]string{api.OwnerLabel: "paused"}}, Data: map[string][]byte{"https-policy": []byte("null")}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(blocked, paused, ledger).Build()
	var legacyChanges, egressChanges atomic.Int32
	// Instant egress state for the fake sandbox-control workspaces.
	var mu sync.Mutex
	egress := map[string]string{"shared-pilot": "Locked", "orca": "Open"}
	minutes := map[string]string{}
	mode := map[string]string{"shared-pilot": "Running", "orca": "Running"}
	pendingObs := map[string]bool{"files.example.com": true, "tracker.example.com": true}
	approvedObs := map[string]bool{"registry.example.com": true}
	previewState := "Pending"
	var nativeActions []string
	observations := func(set map[string]bool) []map[string]any {
		out := []map[string]any{}
		for host := range set {
			raw, _ := json.Marshal(map[string]any{"Host": host, "Method": "GET", "Path": "/v1", "Scheme": "https", "Port": 443, "QueryHash": ""})
			out = append(out, map[string]any{"host": host, "method": "GET", "path": "/v1", "scheme": "https", "port": 443, "observation": string(raw)})
		}
		sort.Slice(out, func(i, j int) bool { return out[i]["host"].(string) < out[j]["host"].(string) })
		return out
	}
	summary := func(id string) map[string]any {
		out := map[string]any{"id": id, "namespace": "agent-workspace-" + id, "sandbox": "worktree", "mode": mode[id], "revision": 3, "status": "Active", "learning": egress[id], "egress": egress[id], "egressControl": true, "pending": 0, "approvals": 0}
		if id == "shared-pilot" {
			out["approvals"] = 2
		}
		if egress[id] != "Locked" {
			out["egressExpires"] = 1900000000
		}
		return out
	}
	legacy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-authentik-username") != "operator" {
			http.Error(w, "unauthenticated", 401)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "__Host-sandbox-csrf", Value: "fixture-csrf", Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode})
		if r.Method == "POST" {
			cookie, err := r.Cookie("__Host-sandbox-csrf")
			if err != nil || cookie.Value != "fixture-csrf" || r.ParseForm() != nil || r.Form.Get("csrf") != "fixture-csrf" || r.Header.Get("Origin") != "https://legacy.example.com" {
				http.Error(w, "CSRF denied", 403)
				return
			}
			if r.Header.Get("Accept") == "application/json" {
				mu.Lock()
				defer mu.Unlock()
				id, action := r.Form.Get("workspace"), r.Form.Get("action")
				fail := func(code int, message string) {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(code)
					json.NewEncoder(w).Encode(map[string]string{"error": message})
				}
				if _, ok := egress[id]; !ok {
					fail(404, "workspace not enrolled")
					return
				}
				switch r.URL.Path + " " + action {
				case "/egress learning-open", "/egress learning-lock", "/egress learning-resume":
					egress[id] = map[string]string{"learning-open": "Open", "learning-lock": "Locked", "learning-resume": "Learning"}[action]
					minutes[id] = r.Form.Get("minutes")
					egressChanges.Add(1)
				case "/egress approve-learning", "/egress revoke-learning":
					var o map[string]any
					if json.Unmarshal([]byte(r.Form.Get("observation")), &o) != nil || o["Host"] == nil {
						fail(400, "invalid observation")
						return
					}
					host := o["Host"].(string)
					if action == "approve-learning" {
						delete(pendingObs, host)
						approvedObs[host] = true
					} else {
						delete(approvedObs, host)
						pendingObs[host] = true
					}
				case "/egress clear-unapproved":
					pendingObs = map[string]bool{}
				case "/lifecycle ":
					mode[id] = r.Form.Get("mode")
				case "/decision ":
					if r.Form.Get("gaps") != "on" && r.Form.Get("state") == "Approved" {
						fail(400, "acknowledge observation gaps")
						return
					}
					previewState = r.Form.Get("state")
				case "/rollback ", "/services ", "/profile ":
				default:
					fail(400, "unknown learning action")
					return
				}
				nativeActions = append(nativeActions, strings.TrimSpace(r.URL.Path+" "+action))
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(summary(id))
				return
			}
			legacyChanges.Add(1)
			http.Redirect(w, r, "/?workspace=shared-pilot&tab=access", 303)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			mu.Lock()
			defer mu.Unlock()
			id := r.URL.Query().Get("workspace")
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Sandbox-CSRF", "fixture-csrf")
			var body any
			switch r.URL.Path {
			case "/api/workspace":
				body = map[string]any{"id": id, "namespace": "agent-workspace-" + id, "sandbox": "worktree", "mode": mode[id],
					"revision":       map[string]any{"id": 7, "status": "Active", "actor": "operator", "at": 1900000000},
					"learningEgress": true, "tailnetReady": true, "serviceOwner": id == "shared-pilot",
					"learning":      map[string]any{"phase": egress[id], "minutes": 60, "started": 1899990000, "expires": 1900000000},
					"providerHosts": []string{"api.provider.example"}, "previewPending": map[bool]int{true: 1}[previewState == "Pending"],
					"egressCounts": map[string]int{"pending": len(pendingObs), "approved": len(approvedObs)},
					"links":        map[string]string{"grafana": "https://grafana.example.com/"},
					"services":     []map[string]any{{"name": "openlit", "host": "openlit.example.com", "editable": id == "shared-pilot", "lanAvailable": true, "tailscaleAvailable": true, "grant": map[string]any{"lan": true, "tailscale": false, "rulesText": "GET,HEAD prefix:/"}}},
					"revisions":    []map[string]any{{"id": 7, "status": "Active", "actor": "operator", "at": 1900000000}, {"id": 6, "status": "Active", "actor": "operator", "at": 1899000000}}}
			case "/api/egress":
				body = map[string]any{"filter": r.URL.Query().Get("q"), "pending": map[string]any{"total": len(pendingObs), "items": observations(pendingObs)}, "approved": map[string]any{"total": len(approvedObs), "items": observations(approvedObs)}}
			case "/api/previews":
				body = map[string]any{"expiredCount": 0, "requests": []map[string]any{{"id": "p1", "state": previewState, "proposedName": "docs-site", "proposedRulesText": "GET,HEAD prefix:/", "rulesText": "", "lan": false, "tailscale": false, "gapAcknowledged": false, "seen": 1900000000.0, "first": 1899999000.0, "count": 3,
					"listener": map[string]any{"project": "workspace", "worktree": "main", "port": 8080, "pid": 41, "address": "0.0.0.0", "loopback": false}, "urls": map[string]string{"lan": "https://preview-p1.example.com", "tailnet": "https://preview-p1-ts.example.com"}}}}
			case "/api/activity":
				body = map[string]any{"window": map[string]any{"start": "2030-03-17T17:31:40Z", "end": "2030-03-17T17:46:40Z", "minutes": 15}, "links": map[string]string{},
					"sources": []map[string]any{{"source": "Web inspection", "scope": "agent-workspace-" + id, "at": 1900000000, "items": []map[string]any{{"title": "GET files.example.com", "target": "https://files.example.com/v1", "time": "17:40", "outcome": "Blocked", "blocked": map[string]any{"host": "files.example.com", "method": "GET", "path": "/v1", "timestamp": "x", "reason": "Approve web requests on the Access tab"}}}}},
					"health":  map[string]any{"cards": []map[string]string{{"title": "worker", "target": "212.0 MiB memory in use"}}}}
			default:
				http.NotFound(w, r)
				return
			}
			json.NewEncoder(w).Encode(body)
			return
		}
		if r.URL.Path == "/workspaces" {
			mu.Lock()
			defer mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Sandbox-CSRF", "fixture-csrf")
			json.NewEncoder(w).Encode([]map[string]any{summary("orca"), summary("shared-pilot")})
			return
		}
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<!doctype html><html><head><title>Existing controls</title></head><body><header><h1>Existing controls</h1></header><main><nav><a href="/?workspace=shared-pilot&amp;tab=access">shared-pilot</a><a href="/?workspace=codex-a&amp;tab=access">codex-a</a><a href="/?workspace=grok-a&amp;tab=access">grok-a</a><a href="/?workspace=cursor-a&amp;tab=access">cursor-a</a></nav><h2>Existing sandbox access</h2><form method="post" action="/access"><input name="csrf" type="hidden" value="fixture-csrf"><button>Save existing access</button></form></main></body></html>`))
	}))
	defer legacy.Close()
	password := strings.Repeat("test", 8)
	server := httptest.NewUnstartedServer(nil)
	origin := "https://" + server.Listener.Addr().String()
	s := &Server{Client: c, Host: host{}, Namespace: "control", Owner: "steve", Password: password, Origin: origin, AllowBasic: true}
	s.SandboxControl = &SandboxControl{URL: legacy.URL, Origin: "https://legacy.example.com", Operator: "operator"}
	handler, e := s.Handler()
	if e != nil {
		t.Fatal(e)
	}
	server.Config.Handler = handler
	server.StartTLS()
	defer server.Close()
	cmd := exec.Command("node", "../../scripts/qualify-dashboard.cjs")
	cmd.Env = append(os.Environ(), "APC_TEST_WEB_ORIGIN="+origin, "APC_TEST_WEB_PASSWORD="+password)
	out, e := cmd.CombinedOutput()
	if e != nil {
		t.Fatalf("browser qualification failed: %v\n%s", e, out)
	}
	w := &api.ProjectWorkspace{}
	if e = c.Get(context.Background(), types.NamespacedName{Namespace: "control", Name: "browser-project"}, w); e != nil {
		t.Fatal(e)
	}
	if w.Spec.DesiredState != "Removed" {
		t.Fatal("browser removal did not persist terminal state")
	}
	if legacyChanges.Load() != 1 {
		t.Fatal("legacy sandbox mutation did not use the combined app")
	}
	mu.Lock()
	if egressChanges.Load() != 7 || egress["shared-pilot"] != "Locked" || egress["orca"] != "Open" || minutes["orca"] != "240" {
		t.Fatalf("instant egress changes did not reach sandbox control: %d %v %v", egressChanges.Load(), egress, minutes)
	}
	want := []string{"/egress learning-open", "/egress learning-lock", "/egress approve-learning", "/egress clear-unapproved", "/decision", "/lifecycle", "/lifecycle", "/rollback"}
	if strings.Join(nativeActions[len(nativeActions)-len(want):], ",") != strings.Join(want, ",") {
		t.Fatalf("native sandbox actions: %v", nativeActions)
	}
	mu.Unlock()
	if e = c.Get(context.Background(), types.NamespacedName{Namespace: "control", Name: "paused-project"}, w); e != nil {
		t.Fatal(e)
	}
	if len(w.Spec.HTTPS) != 1 || w.Spec.HTTPS[0].Host != "api.example.com" || w.Spec.DesiredState != "Paused" {
		b, _ := json.Marshal(w.Spec)
		t.Fatal("browser access update failed", string(b))
	}
	t.Log(string(out))
}
