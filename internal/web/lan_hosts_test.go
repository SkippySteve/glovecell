package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/network"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const lanOrigin = "https://console.example.com"

type lanEnv struct {
	s   *Server
	h   http.Handler
	obj *api.ProjectWorkspace
}

func lanFixture(t *testing.T, enabled bool) *lanEnv {
	t.Helper()
	scheme := runtime.NewScheme()
	_ = api.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	obj := &api.ProjectWorkspace{ObjectMeta: metav1.ObjectMeta{Name: "project", Namespace: "control", UID: types.UID("fixture-uid"), Generation: 1}, Spec: api.WorkspaceSpec{Repository: "steve/repo", DesiredState: "Running"}, Status: api.WorkspaceStatus{Phase: "Ready", Namespace: "apc-fixtureuid"}}
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node"}, Status: corev1.NodeStatus{Addresses: []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "192.168.120.9"}}}}
	oidc := &OIDC{sessions: map[string]session{"operator-session": {Subject: "https://auth.example.com#steve", Expires: time.Now().Add(time.Hour)}}}
	s := &Server{Client: fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(obj).WithObjects(obj, node).Build(), Host: host{}, Namespace: "control", Owner: "steve", Password: strings.Repeat("a", 32), Origin: lanOrigin, GitOrigin: "https://git.example.com", OIDC: oidc}
	if enabled {
		lan, err := network.ParseLANConfig("192.168.0.0/16", "192.168.105.0/24,192.168.50.0/28,192.168.110.1")
		if err != nil {
			t.Fatal(err)
		}
		s.LAN = &LANGrants{Config: lan, Nodes: network.NodeAddresses}
	}
	h, err := s.Handler()
	if err != nil {
		t.Fatal(err)
	}
	env := &lanEnv{s: s, h: h, obj: obj}
	env.refresh(t)
	return env
}

func (e *lanEnv) refresh(t *testing.T) {
	t.Helper()
	if err := e.s.Client.Get(context.Background(), client.ObjectKeyFromObject(e.obj), e.obj); err != nil {
		t.Fatal(err)
	}
}

// do sends a request as the signed-in OIDC operator from the console origin,
// unless a modifier changes the credential or headers.
func (e *lanEnv) do(method, path, body string, modify ...func(*http.Request)) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.AddCookie(&http.Cookie{Name: "__Host-apc-session", Value: "operator-session"})
	req.Header.Set("Origin", lanOrigin)
	req.Header.Set("Content-Type", "application/json")
	for _, m := range modify {
		m(req)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func (e *lanEnv) add(t *testing.T, address, ports string, modify ...func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	e.refresh(t)
	return e.do("POST", "/api/v1/workspaces/project/lan-hosts", `{"resourceVersion":"`+e.obj.ResourceVersion+`","address":"`+address+`","ports":`+ports+`,"note":"dev box"}`, modify...)
}

func (e *lanEnv) remove(t *testing.T, address string, modify ...func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	e.refresh(t)
	return e.do("DELETE", "/api/v1/workspaces/project/lan-hosts/"+address, `{"resourceVersion":"`+e.obj.ResourceVersion+`"}`, modify...)
}

func (e *lanEnv) audits(t *testing.T, action string) []map[string]string {
	t.Helper()
	var list corev1.ConfigMapList
	if err := e.s.Client.List(context.Background(), &list, client.InNamespace("control"), client.MatchingLabels{api.OwnerLabel: "fixture-uid"}); err != nil {
		t.Fatal(err)
	}
	var out []map[string]string
	for _, cm := range list.Items {
		if cm.Data["action"] == action {
			if cm.Immutable == nil || !*cm.Immutable || !strings.HasPrefix(cm.Name, "audit-") {
				t.Fatalf("audit record %s is not an immutable audit ConfigMap", cm.Name)
			}
			out = append(out, cm.Data)
		}
	}
	return out
}

func TestLANHostAddAndRemoveByOIDCOperator(t *testing.T) {
	e := lanFixture(t, true)
	rec := e.add(t, "192.168.110.2", "[8080,22]")
	if rec.Code != 201 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	e.refresh(t)
	if len(e.obj.Spec.LANHosts) != 1 {
		t.Fatalf("grants = %#v", e.obj.Spec.LANHosts)
	}
	g := e.obj.Spec.LANHosts[0]
	if g.Address != "192.168.110.2" || len(g.Ports) != 2 || g.Ports[0] != 22 || g.Note != "dev box" || g.AddedBy != "https://auth.example.com#steve" || g.AddedAt == nil {
		t.Fatalf("grant = %#v", g)
	}
	// Running projects are changed in place; no pause was needed.
	if e.obj.Spec.DesiredState != "Running" || e.obj.Annotations["projects.agentwork.dev/requested-by"] != "https://auth.example.com#steve" {
		t.Fatal("grant changed lifecycle or attribution", e.obj.Spec.DesiredState, e.obj.Annotations)
	}
	added := e.audits(t, "lan-host-added")
	if len(added) != 1 || added[0]["actor"] != "https://auth.example.com#steve" || added[0]["address"] != "192.168.110.2" || added[0]["ports"] != "22,8080" || added[0]["workspace"] != "project" || added[0]["time"] == "" || !strings.Contains(added[0]["message"], "added by") {
		t.Fatalf("audit = %#v", added)
	}
	if rec := e.add(t, "192.168.110.2", "[443]"); rec.Code != 409 {
		t.Fatal("duplicate address accepted", rec.Code)
	}
	if rec := e.remove(t, "192.168.110.9"); rec.Code != 404 {
		t.Fatal("unknown grant removal", rec.Code)
	}
	if rec := e.remove(t, "192.168.110.2"); rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	e.refresh(t)
	if len(e.obj.Spec.LANHosts) != 0 {
		t.Fatal("grant not removed")
	}
	removed := e.audits(t, "lan-host-removed")
	if len(removed) != 1 || removed[0]["address"] != "192.168.110.2" || removed[0]["ports"] != "22,8080" {
		t.Fatalf("removal audit = %#v", removed)
	}
}

func TestLANHostRequiresOIDCOperatorAndSameOriginJSON(t *testing.T) {
	e := lanFixture(t, true)
	bearer := func(r *http.Request) {
		r.Header.Del("Cookie")
		r.Header.Del("Origin")
		r.Header.Set("Authorization", "Bearer "+strings.Repeat("a", 32))
	}
	cases := map[string]struct {
		modify func(*http.Request)
		code   int
	}{
		"anonymous":              {func(r *http.Request) { r.Header.Del("Cookie") }, 401},
		"expired session":        {func(r *http.Request) { r.Header.Set("Cookie", "__Host-apc-session=unknown") }, 401},
		"automation bearer":      {bearer, 403},
		"cross-site origin":      {func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") }, 403},
		"missing origin":         {func(r *http.Request) { r.Header.Del("Origin") }, 403},
		"form post":              {func(r *http.Request) { r.Header.Set("Content-Type", "application/x-www-form-urlencoded") }, 415},
		"text/plain simple post": {func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }, 415},
	}
	for name, c := range cases {
		if rec := e.add(t, "192.168.110.2", "[22]", c.modify); rec.Code != c.code {
			t.Errorf("add %s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	e.refresh(t)
	if len(e.obj.Spec.LANHosts) != 0 {
		t.Fatal("refused request saved a grant")
	}
	if rec := e.add(t, "192.168.110.2", "[22]"); rec.Code != 201 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	for name, c := range cases {
		if rec := e.remove(t, "192.168.110.2", c.modify); rec.Code != c.code {
			t.Errorf("remove %s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	// Static login is refused too, even where it is enabled.
	e.s.AllowBasic = true
	h, err := e.s.Handler()
	if err != nil {
		t.Fatal(err)
	}
	e.h = h
	basic := func(r *http.Request) { r.Header.Del("Cookie"); r.SetBasicAuth("operator", strings.Repeat("a", 32)) }
	if rec := e.remove(t, "192.168.110.2", basic); rec.Code != 403 {
		t.Fatal("static login removed a grant", rec.Code)
	}
	e.refresh(t)
	if len(e.obj.Spec.LANHosts) != 1 {
		t.Fatal("refused removal changed grants")
	}
}

func TestLANHostRejectsBadInput(t *testing.T) {
	e := lanFixture(t, true)
	for _, c := range []struct{ address, ports, want string }{
		{"192.168.110.0/24", "[22]", "not a range"},
		{"192.168.110.2-192.168.110.4", "[22]", "not a range"},
		{"devbox.lan", "[22]", "hostnames"},
		{"fd00::2", "[22]", "only IPv4"},
		{"::ffff:192.168.110.2", "[22]", "only IPv4"},
		{"127.0.0.1", "[22]", "loopback"},
		{"169.254.169.254", "[80]", "metadata"},
		{"224.0.0.251", "[22]", "multicast"},
		{"255.255.255.255", "[22]", "broadcast"},
		{"192.168.110.255", "[22]", "broadcast"},
		{"8.8.8.8", "[443]", "outside the allowed LAN ranges"},
		{"192.168.105.4", "[22]", "denylist"},
		{"192.168.105.250", "[6443]", "denylist"},
		{"192.168.50.3", "[443]", "denylist"},
		{"192.168.110.1", "[443]", "denylist"},
		{"192.168.120.9", "[22]", "node address"},
		{"192.168.110.2", "[]", "TCP ports"},
		{"192.168.110.2", "[0]", "between 1 and 65535"},
		{"192.168.110.2", "[65536]", "between 1 and 65535"},
		{"192.168.110.2", "[22,22]", "twice"},
		{"192.168.110.2", "[1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17]", "TCP ports"},
	} {
		rec := e.add(t, c.address, c.ports)
		if rec.Code != 400 || !strings.Contains(rec.Body.String(), c.want) {
			t.Errorf("%s %s: %d %s", c.address, c.ports, rec.Code, rec.Body.String())
		}
	}
	e.refresh(t)
	for _, body := range []string{
		`{"resourceVersion":"` + e.obj.ResourceVersion + `","address":"192.168.110.2","ports":["22"]}`,
		`{"resourceVersion":"` + e.obj.ResourceVersion + `","address":"192.168.110.2","ports":[22],"cidr":"10.0.0.0/8"}`,
		`{"address":"192.168.110.2","ports":[22]}`,
		`{"resourceVersion":"` + e.obj.ResourceVersion + `","address":"192.168.110.2","ports":[22],"note":"line\nbreak"}`,
		`{"resourceVersion":"` + e.obj.ResourceVersion + `","address":"192.168.110.2","ports":[22]} {}`,
	} {
		if rec := e.do("POST", "/api/v1/workspaces/project/lan-hosts", body); rec.Code != 400 {
			t.Errorf("%s: %d", body, rec.Code)
		}
	}
	if rec := e.do("POST", "/api/v1/workspaces/project/lan-hosts", `{"resourceVersion":"stale","address":"192.168.110.2","ports":[22]}`); rec.Code != 409 {
		t.Error("stale resourceVersion accepted", rec.Code)
	}
	if rec := e.do("POST", "/api/v1/workspaces/missing/lan-hosts", `{"resourceVersion":"1","address":"192.168.110.2","ports":[22]}`); rec.Code != 404 {
		t.Error("missing project", rec.Code)
	}
	e.refresh(t)
	if len(e.obj.Spec.LANHosts) != 0 {
		t.Fatal("bad input saved a grant")
	}
	for i := 2; i < 2+network.MaxLANHosts; i++ {
		if rec := e.add(t, "192.168.111."+string(rune('0'+i)), "[22]"); rec.Code != 201 {
			t.Fatal(i, rec.Code, rec.Body.String())
		}
	}
	if rec := e.add(t, "192.168.112.2", "[22]"); rec.Code != 409 || !strings.Contains(rec.Body.String(), "at most") {
		t.Fatal("per-project cap not enforced", rec.Code)
	}
	e.refresh(t)
	e.obj.Spec.DesiredState = "Removed"
	if err := e.s.Client.Update(context.Background(), e.obj); err != nil {
		t.Fatal(err)
	}
	if rec := e.add(t, "192.168.112.2", "[22]"); rec.Code != 409 {
		t.Fatal("grant added to a removed project", rec.Code)
	}
}

func TestLANHostDisabledStillAllowsRemoval(t *testing.T) {
	e := lanFixture(t, false)
	if rec := e.add(t, "192.168.110.2", "[22]"); rec.Code != 403 || !strings.Contains(rec.Body.String(), "disabled") {
		t.Fatal("disabled feature accepted a grant", rec.Code)
	}
	var session map[string]any
	rec := e.do("GET", "/api/v1/session", "")
	if json.Unmarshal(rec.Body.Bytes(), &session) != nil || session["lanHosts"] != nil {
		t.Fatalf("session advertises LAN grants: %v", session)
	}
	// A grant left over from before the feature was turned off can still be withdrawn.
	e.obj.Spec.LANHosts = []api.LANHost{{Address: "192.168.110.2", Ports: []int32{22}}}
	if err := e.s.Client.Update(context.Background(), e.obj); err != nil {
		t.Fatal(err)
	}
	if rec := e.remove(t, "192.168.110.2"); rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	enabled := lanFixture(t, true)
	rec = enabled.do("GET", "/api/v1/session", "")
	if json.Unmarshal(rec.Body.Bytes(), &session) != nil || session["lanHosts"] != true || session["lanAllowed"].([]any)[0] != "192.168.0.0/16" {
		t.Fatalf("session = %v", session)
	}
}

func TestLANHostNodeListFailureFailsClosed(t *testing.T) {
	e := lanFixture(t, true)
	e.s.LAN.Nodes = func(context.Context, client.Client) ([]netip.Addr, error) { return nil, context.DeadlineExceeded }
	if rec := e.add(t, "192.168.110.2", "[22]"); rec.Code != 503 {
		t.Fatal("grant accepted without node addresses", rec.Code)
	}
}
