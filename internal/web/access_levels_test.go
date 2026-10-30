package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/controller"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

func installProfiles(t *testing.T, c client.Client) {
	t.Helper()
	raw, err := os.ReadFile("../../config/samples/accessprofiles.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, doc := range strings.Split(string(raw), "\n---\n") {
		p := &api.AccessProfile{}
		if err := yaml.UnmarshalStrict([]byte(doc), p); err != nil {
			t.Fatal(err)
		}
		if err := c.Create(context.Background(), p); err != nil {
			t.Fatal(err)
		}
	}
}

func levelEnv(t *testing.T) *lanEnv {
	t.Helper()
	e := lanFixture(t, true)
	installProfiles(t, e.s.Client)
	return e
}

func (e *lanEnv) level(t *testing.T, profile string) *httptest.ResponseRecorder {
	t.Helper()
	e.refresh(t)
	return e.do("PATCH", "/api/v1/workspaces/project/access-level", `{"resourceVersion":"`+e.obj.ResourceVersion+`","profile":"`+profile+`"}`)
}

func (e *lanEnv) setSpec(t *testing.T, change func(*api.ProjectWorkspace)) {
	t.Helper()
	e.refresh(t)
	change(e.obj)
	if err := e.s.Client.Update(context.Background(), e.obj); err != nil {
		t.Fatal(err)
	}
	e.refresh(t)
}

func TestAccessProfilesListSaysWhatProjectsSupport(t *testing.T) {
	e := levelEnv(t)
	rec := e.do("GET", "/api/v1/access-profiles", "")
	if rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	var out []profileView
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	byName := map[string]profileView{}
	for _, p := range out {
		byName[p.Name] = p
	}
	if len(byName) != 5 || !byName["locked"].Default || !byName["standard"].ProjectSupported || byName["ask"].ProjectSupported || byName["open"].ProjectSupported || byName["open"].Reason == "" {
		t.Fatalf("profiles = %#v", out)
	}
	if len(byName["standard"].Rules) != 12 || byName["standard"].Rules[0].Source != "profile:standard" || byName["open"].MaxGrantMinutes["internet"] != 480 {
		t.Fatalf("standard = %#v", byName["standard"])
	}
}

func TestLevelChangeAppliesLiveAndIsRecordedFirst(t *testing.T) {
	e := levelEnv(t)
	rec := e.level(t, "standard")
	if rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	e.refresh(t)
	if e.obj.Spec.AccessProfileName != "standard" || e.obj.Spec.DesiredState != "Running" {
		t.Fatalf("spec = %#v", e.obj.Spec)
	}
	if records := e.audits(t, "access-level-requested"); len(records) != 1 || records[0]["profile"] != "standard" || records[0]["actor"] != "https://auth.example.com#steve" {
		t.Fatalf("records = %#v", records)
	}
}

func TestLevelChangeRefusesUnsupportedLevelsAndKeepsOwnRulesWhileRunning(t *testing.T) {
	e := levelEnv(t)
	for _, profile := range []string{"ask", "open", "absent"} {
		if rec := e.level(t, profile); rec.Code != 400 {
			t.Fatal(profile, rec.Code, rec.Body.String())
		}
	}
	// Own HTTPS rules can only be dropped while paused.
	e.setSpec(t, func(w *api.ProjectWorkspace) {
		w.Spec.HTTPS = []api.HTTPSRule{{Host: "api.example.com", Methods: []string{"POST"}, Paths: []string{"/"}}}
	})
	if rec := e.level(t, "locked"); rec.Code != 409 {
		t.Fatal("own rules dropped from a running sandbox", rec.Code)
	}
	e.refresh(t)
	if e.obj.Spec.AccessProfileName != "" || len(e.obj.Spec.HTTPS) != 1 {
		t.Fatal("a refused level changed the sandbox", e.obj.Spec)
	}
	if len(e.audits(t, "access-level-requested")) != 0 {
		t.Fatal("refused requests were recorded")
	}
	// Custom keeps them, so it applies while running.
	if rec := e.level(t, "custom"); rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	e.refresh(t)
	if len(e.obj.Spec.HTTPS) != 1 || e.obj.Spec.AccessProfileName != "custom" {
		t.Fatal("custom dropped own rules", e.obj.Spec)
	}
}

func TestLevelChangeWithoutAuditRecordChangesNothing(t *testing.T) {
	e := levelEnv(t)
	e.s.Client = failConfigMaps{Client: e.s.Client}
	if rec := e.level(t, "standard"); rec.Code != 503 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	e.refresh(t)
	if e.obj.Spec.AccessProfileName != "" {
		t.Fatal("unrecorded level change was stored")
	}
}

type failConfigMaps struct{ client.Client }

func (f failConfigMaps) Create(ctx context.Context, o client.Object, opts ...client.CreateOption) error {
	if _, ok := o.(*corev1.ConfigMap); ok {
		return errors.New("audit store unavailable")
	}
	return f.Client.Create(ctx, o, opts...)
}

type failGrantDeletes struct {
	client.Client
	left int
}

func (f *failGrantDeletes) Delete(ctx context.Context, o client.Object, opts ...client.DeleteOption) error {
	if _, ok := o.(*api.AccessGrant); ok {
		if f.left == 0 {
			return errors.New("api server unavailable")
		}
		f.left--
	}
	return f.Client.Delete(ctx, o, opts...)
}

func (e *lanEnv) grant(t *testing.T, body string, modify ...func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	return e.do("POST", "/api/v1/workspaces/project/grants", body, modify...)
}

func TestLANGrantNeedsOIDCAndAProfileThatAllowsIt(t *testing.T) {
	e := levelEnv(t)
	body := `{"type":"lan","minutes":60,"lan":{"address":"192.168.1.20","ports":[22]}}`
	if rec := e.grant(t, body); rec.Code != 409 {
		t.Fatal("grant without a level", rec.Code, rec.Body.String())
	}
	e.setSpec(t, func(w *api.ProjectWorkspace) { w.Spec.AccessProfileName = "locked" })
	if rec := e.grant(t, body); rec.Code != 409 {
		t.Fatal("locked allowed a grant", rec.Code, rec.Body.String())
	}
	e.setSpec(t, func(w *api.ProjectWorkspace) { w.Spec.AccessProfileName = "custom" })
	bearer := func(r *http.Request) {
		r.Header.Del("Cookie")
		r.Header.Del("Origin")
		r.Header.Set("Authorization", "Bearer "+strings.Repeat("a", 32))
	}
	if rec := e.grant(t, body, bearer); rec.Code != 403 {
		t.Fatal("automation bearer created a grant", rec.Code)
	}
	for name, refused := range map[string]string{
		"internet":     `{"type":"internet","minutes":60,"internet":{}}`,
		"package":      `{"type":"package","minutes":60,"package":{"ecosystem":"npm","name":"left-pad"}}`,
		"too long":     `{"type":"lan","minutes":2000,"lan":{"address":"192.168.1.20","ports":[22]}}`,
		"denied range": `{"type":"lan","minutes":60,"lan":{"address":"192.168.105.7","ports":[22]}}`,
		"node":         `{"type":"lan","minutes":60,"lan":{"address":"192.168.120.9","ports":[22]}}`,
		"no ports":     `{"type":"lan","minutes":60,"lan":{"address":"192.168.1.20","ports":[]}}`,
		"no minutes":   `{"type":"lan","lan":{"address":"192.168.1.20","ports":[22]}}`,
	} {
		if rec := e.grant(t, refused); rec.Code != 400 {
			t.Fatal(name, rec.Code, rec.Body.String())
		}
	}
	var list api.AccessGrantList
	if err := e.s.Client.List(context.Background(), &list); err != nil || len(list.Items) != 0 {
		t.Fatal("a refused request created a grant", len(list.Items), err)
	}

	rec := e.grant(t, body)
	if rec.Code != 201 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	var g api.AccessGrant
	if err := json.Unmarshal(rec.Body.Bytes(), &g); err != nil {
		t.Fatal(err)
	}
	if g.Spec.Cell != "project" || g.Labels[api.CellLabel] != "project" || len(g.OwnerReferences) != 1 || g.OwnerReferences[0].UID != e.obj.UID || g.Spec.RequestedBy != "https://auth.example.com#steve" {
		t.Fatalf("grant = %#v", g)
	}
	if left := time.Until(g.Spec.ExpiresAt.Time); left < 59*time.Minute || left > 61*time.Minute {
		t.Fatal("expiry", left)
	}
	// The controller would apply exactly this grant.
	_, decisions := controller.ResolveGrants(e.obj, mustProfile(t, e, "custom"), []api.AccessGrant{withCreation(g)}, e.s.LAN.Config, nil, time.Now())
	if decisions[0].State != api.AccessGrantActive {
		t.Fatal(decisions[0].Message)
	}
	if len(e.audits(t, "grant-requested")) != 1 {
		t.Fatal("grant not recorded")
	}
	if rec := e.do("GET", "/api/v1/workspaces/project/grants", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), g.Name) {
		t.Fatal(rec.Code, rec.Body.String())
	}
	if rec := e.do("DELETE", "/api/v1/workspaces/other/grants/"+g.Name, ""); rec.Code != 404 {
		t.Fatal("removed through another sandbox", rec.Code)
	}
	if rec := e.do("DELETE", "/api/v1/workspaces/project/grants/"+g.Name, ""); rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body.String())
	}
}

func mustProfile(t *testing.T, e *lanEnv, name string) *api.AccessProfile {
	t.Helper()
	p := &api.AccessProfile{}
	if err := e.s.Client.Get(context.Background(), client.ObjectKey{Name: name}, p); err != nil {
		t.Fatal(err)
	}
	return p
}

func withCreation(g api.AccessGrant) api.AccessGrant {
	g.CreationTimestamp = metav1.Now()
	return g
}

func TestLockNowRemovesGrantsThenLocksAndRecoversFromInterruption(t *testing.T) {
	e := levelEnv(t)
	e.setSpec(t, func(w *api.ProjectWorkspace) { w.Spec.AccessProfileName = "custom" })
	for _, address := range []string{"192.168.1.20", "192.168.1.21"} {
		if rec := e.grant(t, `{"type":"lan","minutes":60,"lan":{"address":"`+address+`","ports":[22]}}`); rec.Code != 201 {
			t.Fatal(rec.Code, rec.Body.String())
		}
	}
	lock := func() *httptest.ResponseRecorder {
		e.refresh(t)
		return e.do("POST", "/api/v1/workspaces/project/lock", `{"resourceVersion":"`+e.obj.ResourceVersion+`"}`)
	}
	// Interrupted after the first grant: one is gone, the profile is unchanged, nothing is wider.
	real := e.s.Client
	e.s.Client = &failGrantDeletes{Client: real, left: 1}
	if rec := lock(); rec.Code != 500 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	e.s.Client = real
	var list api.AccessGrantList
	if err := real.List(context.Background(), &list); err != nil || len(list.Items) != 1 {
		t.Fatal("interrupted lock", len(list.Items), err)
	}
	// The retry finishes.
	if rec := lock(); rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	e.refresh(t)
	if err := real.List(context.Background(), &list); err != nil || len(list.Items) != 0 || e.obj.Spec.AccessProfileName != "locked" {
		t.Fatal("lock left access", len(list.Items), e.obj.Spec.AccessProfileName)
	}
	if len(e.audits(t, "access-locked")) == 0 {
		t.Fatal("lock not recorded")
	}
}

func TestCreateTakesTheClusterDefaultProfile(t *testing.T) {
	e := levelEnv(t)
	e.s.AllowBasic = true
	post := func(body string) *httptest.ResponseRecorder {
		return e.do("POST", "/api/v1/workspaces", body)
	}
	if rec := post(`{"name":"opened","repository":"steve/repo","accessProfile":"open"}`); rec.Code != 400 {
		t.Fatal("created with an unsupported level", rec.Code)
	}
	rec := post(`{"name":"fresh","repository":"steve/repo"}`)
	if rec.Code != 201 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	var created api.ProjectWorkspace
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil || created.Spec.AccessProfileName != "locked" {
		t.Fatal("default profile not applied", created.Spec.AccessProfileName, err)
	}
	rec = post(`{"name":"std","repository":"steve/repo","accessProfile":"standard"}`)
	if rec.Code != 201 || !strings.Contains(rec.Body.String(), `"accessProfileName":"standard"`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
}
