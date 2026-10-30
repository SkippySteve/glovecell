package controller

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"strings"
	"testing"
	"time"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/network"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/proxy"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

// sampleProfiles reads the default profiles shipped in config/samples.
func sampleProfiles(t *testing.T) map[string]*api.AccessProfile {
	t.Helper()
	raw, err := os.ReadFile("../../config/samples/accessprofiles.yaml")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]*api.AccessProfile{}
	for _, doc := range strings.Split(string(raw), "\n---\n") {
		p := &api.AccessProfile{}
		if err := yaml.UnmarshalStrict([]byte(doc), p); err != nil {
			t.Fatal(err)
		}
		out[p.Name] = p
	}
	return out
}

func createProfile(t *testing.T, r *Reconciler, p *api.AccessProfile) {
	t.Helper()
	p = p.DeepCopyObject().(*api.AccessProfile)
	p.ResourceVersion = ""
	if err := r.Create(context.Background(), p); err != nil {
		t.Fatal(err)
	}
}

func setProfile(t *testing.T, r *Reconciler, w *api.ProjectWorkspace, name string) {
	t.Helper()
	w.Spec.AccessProfileName = name
	w.Generation++
	if err := r.Update(context.Background(), w); err != nil {
		t.Fatal(err)
	}
}

func TestSampleProfilesMatchTheLevels(t *testing.T) {
	profiles := sampleProfiles(t)
	for _, level := range []api.AccessLevel{api.AccessLevelLocked, api.AccessLevelAsk, api.AccessLevelStandard, api.AccessLevelOpen, api.AccessLevelCustom} {
		p := profiles[string(level)]
		if p == nil || p.Spec.Level != level {
			t.Fatalf("no sample profile for %s", level)
		}
		rules, err := ProfileRules(p)
		if err != nil {
			t.Fatal(err)
		}
		// Profile rules must pass the read-only reference validation.
		if err = proxy.Validate(proxy.Config{Rules: append([]api.HTTPSRule{{Host: "git.example.com", Methods: []string{"GET"}, Paths: []string{"/"}}}, rules...)}); err != nil {
			t.Fatal(level, err)
		}
	}
	if profiles["locked"].Annotations[api.DefaultAccessProfileAnnotation] != "true" || len(profiles["locked"].Spec.AllowedGrants) != 0 {
		t.Fatal("locked must be the default and allow no grants")
	}
	standard, _ := ProfileRules(profiles["standard"])
	want := proxy.StandardRules()
	if len(standard) != len(want) {
		t.Fatalf("standard profile has %d rules, proxy.StandardRules %d", len(standard), len(want))
	}
	for i := range want {
		if standard[i].Host != want[i].Host || strings.Join(standard[i].Methods, ",") != "GET,HEAD" {
			t.Fatalf("standard rule %d = %#v", i, standard[i])
		}
	}
	// Only the open profile allows internet windows, and only with a limit.
	for name, p := range profiles {
		if p.Allows(api.AccessGrantInternet) != (name == "open") {
			t.Fatal(name, "internet grants")
		}
	}
	if maxDuration(profiles["open"], api.AccessGrantInternet) != 8*time.Hour {
		t.Fatal("open window limit")
	}
}

func TestProfileRulesApplyAndNarrowWhileRunning(t *testing.T) {
	ctx := context.Background()
	r, w, _ := fixture(t)
	profiles := sampleProfiles(t)
	createProfile(t, r, profiles["standard"])
	createProfile(t, r, profiles["locked"])
	if err := r.ensure(ctx, w); err != nil {
		t.Fatal(err)
	}
	_, cfg := proxyPolicy(t, r, w)
	if proxy.Allowed(cfg, "registry.npmjs.org", "GET", "/left-pad") {
		t.Fatal("a cell without a profile gained registry access")
	}

	pod := runningProxyPod(t, r, w)
	setProfile(t, r, w, "standard")
	if err := r.ensure(ctx, w); err != nil {
		t.Fatal("standard refused for a running cell", err)
	}
	raw, cfg := proxyPolicy(t, r, w)
	if !proxy.Allowed(cfg, "registry.npmjs.org", "GET", "/left-pad") || proxy.Allowed(cfg, "registry.npmjs.org", "PUT", "/left-pad") || !strings.Contains(raw, `"source":"profile:standard"`) {
		t.Fatal("standard profile rules not in force", raw)
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(pod), pod); err == nil {
		t.Fatal("proxy not restarted for the new profile")
	}

	// Lock now: the narrower profile applies without a pause.
	pod = runningProxyPod(t, r, w)
	setProfile(t, r, w, "locked")
	if err := r.ensure(ctx, w); err != nil {
		t.Fatal(err)
	}
	if _, cfg = proxyPolicy(t, r, w); proxy.Allowed(cfg, "registry.npmjs.org", "GET", "/left-pad") {
		t.Fatal("locked kept registry access")
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(pod), pod); err == nil {
		t.Fatal("proxy kept connections opened under the wider profile")
	}
}

func TestMissingOrWritingProfileFailsClosed(t *testing.T) {
	ctx := context.Background()
	r, w, _ := fixture(t)
	setProfile(t, r, w, "absent")
	if err := r.validate(ctx, w); err == nil || !errors.Is(err, errProfileMissing) {
		t.Fatal("missing profile accepted", err)
	}
	writer := &api.AccessProfile{ObjectMeta: metav1.ObjectMeta{Name: "writer"}, Spec: api.AccessProfileSpec{Level: api.AccessLevelCustom, HTTPS: []api.HTTPSRule{{Host: "upload.example.com", Methods: []string{"POST"}, Paths: []string{"/"}}}}}
	createProfile(t, r, writer)
	setProfile(t, r, w, "writer")
	if err := r.validate(ctx, w); err == nil {
		t.Fatal("a profile with write rules was accepted")
	}
}

func grantFor(w *api.ProjectWorkspace, name string, t api.AccessGrantType, created time.Time, expires time.Duration) api.AccessGrant {
	g := api.AccessGrant{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: w.Namespace, CreationTimestamp: metav1.NewTime(created), Labels: map[string]string{api.CellLabel: w.Name},
		OwnerReferences: []metav1.OwnerReference{{APIVersion: api.GroupVersion.String(), Kind: "ProjectWorkspace", Name: w.Name, UID: w.UID}}},
		Spec: api.AccessGrantSpec{Cell: w.Name, Type: t, ExpiresAt: metav1.NewTime(created.Add(expires))}}
	switch t {
	case api.AccessGrantLAN:
		g.Spec.LAN = &api.LANGrant{Address: "192.168.1.20", Ports: []int32{22}}
	case api.AccessGrantInternet:
		g.Spec.Internet = &api.InternetGrant{}
	case api.AccessGrantPackage:
		g.Spec.Package = &api.PackageGrant{Ecosystem: "npm", Name: "left-pad"}
	}
	return g
}

func TestResolveGrantsOnlyAppliesWhatEachCheckAllows(t *testing.T) {
	lan, err := network.ParseLANConfig("192.168.0.0/16", "192.168.105.0/24")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	w := &api.ProjectWorkspace{ObjectMeta: metav1.ObjectMeta{Name: "project", Namespace: "control", UID: "cell-uid"}, Spec: api.WorkspaceSpec{LANHosts: []api.LANHost{{Address: "192.168.1.20", Ports: []int32{8080}}}}}
	custom := &api.AccessProfile{ObjectMeta: metav1.ObjectMeta{Name: "custom"}, Spec: api.AccessProfileSpec{Level: api.AccessLevelCustom, AllowedGrants: []api.AccessGrantType{api.AccessGrantLAN, api.AccessGrantPackage, api.AccessGrantInternet},
		MaxGrantDuration: map[api.AccessGrantType]metav1.Duration{api.AccessGrantLAN: {Duration: 24 * time.Hour}, api.AccessGrantPackage: {Duration: 24 * time.Hour}, api.AccessGrantInternet: {Duration: 8 * time.Hour}}}}
	locked := &api.AccessProfile{ObjectMeta: metav1.ObjectMeta{Name: "locked"}, Spec: api.AccessProfileSpec{Level: api.AccessLevelLocked}}
	created := now.Add(-time.Hour)

	ok := grantFor(w, "ok", api.AccessGrantLAN, created, 2*time.Hour)
	hosts, decisions := ResolveGrants(w, custom, []api.AccessGrant{ok}, lan, nil, now)
	if len(hosts) != 1 || len(hosts[0].Ports) != 2 || decisions[0].State != api.AccessGrantActive {
		t.Fatalf("LAN grant not merged with the cell's host: %#v %#v", hosts, decisions)
	}
	if len(w.Spec.LANHosts[0].Ports) != 1 {
		t.Fatal("resolution changed the cell's own spec")
	}

	cases := map[string]struct {
		grant   func() api.AccessGrant
		profile *api.AccessProfile
		state   api.AccessGrantState
		nodes   []netip.Addr
	}{
		"expired":        {func() api.AccessGrant { return grantFor(w, "g", api.AccessGrantLAN, now.Add(-3*time.Hour), time.Hour) }, custom, api.AccessGrantExpired, nil},
		"no profile":     {func() api.AccessGrant { return ok }, nil, api.AccessGrantRefused, nil},
		"locked profile": {func() api.AccessGrant { return ok }, locked, api.AccessGrantRefused, nil},
		"beyond limit":   {func() api.AccessGrant { return grantFor(w, "g", api.AccessGrantLAN, created, 48*time.Hour) }, custom, api.AccessGrantRefused, nil},
		"internet":       {func() api.AccessGrant { return grantFor(w, "g", api.AccessGrantInternet, created, 2*time.Hour) }, custom, api.AccessGrantRefused, nil},
		"package":        {func() api.AccessGrant { return grantFor(w, "g", api.AccessGrantPackage, created, 2*time.Hour) }, custom, api.AccessGrantRefused, nil},
		"other cell": {func() api.AccessGrant {
			g := ok
			g.Spec.Cell = "other"
			return g
		}, custom, api.AccessGrantRefused, nil},
		"not owned": {func() api.AccessGrant {
			g := ok
			g.OwnerReferences = nil
			return g
		}, custom, api.AccessGrantRefused, nil},
		"denied range": {func() api.AccessGrant {
			g := grantFor(w, "g", api.AccessGrantLAN, created, 2*time.Hour)
			g.Spec.LAN.Address = "192.168.105.7"
			return g
		}, custom, api.AccessGrantRefused, nil},
		"node address": {func() api.AccessGrant {
			g := grantFor(w, "g", api.AccessGrantLAN, created, 2*time.Hour)
			g.Spec.LAN.Address = "192.168.7.9"
			return g
		}, custom, api.AccessGrantRefused, []netip.Addr{netip.MustParseAddr("192.168.7.9")}},
		"too many ports": {func() api.AccessGrant {
			g := grantFor(w, "g", api.AccessGrantLAN, created, 2*time.Hour)
			g.Spec.LAN.Ports = []int32{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
			return g
		}, custom, api.AccessGrantRefused, nil},
	}
	for name, c := range cases {
		hosts, decisions := ResolveGrants(w, c.profile, []api.AccessGrant{c.grant()}, lan, c.nodes, now)
		if len(decisions) != 1 || decisions[0].State != c.state {
			t.Fatalf("%s: %#v", name, decisions)
		}
		if len(hosts) != 1 || len(hosts[0].Ports) != 1 {
			t.Fatalf("%s widened the LAN policy: %#v", name, hosts)
		}
	}
	// LAN grants off on this controller: nothing applies.
	if hosts, d := ResolveGrants(w, custom, []api.AccessGrant{ok}, network.LANConfig{}, nil, now); len(hosts) != 1 || len(hosts[0].Ports) != 1 || d[0].State != api.AccessGrantRefused {
		t.Fatal("LAN grant applied with LAN grants disabled")
	}
}

func TestLANGrantAppliesAndExpiresWhileRunning(t *testing.T) {
	ctx := context.Background()
	r, w := lanFixture(t)
	createProfile(t, r, sampleProfiles(t)["custom"])
	setProfile(t, r, w, "custom")
	g := grantFor(w, "desk-ssh", api.AccessGrantLAN, time.Now(), time.Hour)
	g.CreationTimestamp = metav1.Time{}
	if err := r.Create(ctx, &g); err != nil {
		t.Fatal(err)
	}
	if err := r.ensure(ctx, w); err != nil {
		t.Fatal(err)
	}
	p := lanPolicy(t, r, w)
	if p == nil || !strings.Contains(strings.Join(mustJSON(t, p.Object["spec"]), ""), "192.168.1.20/32") {
		t.Fatal("live LAN grant not in the lan-hosts policy")
	}

	// It expires: the policy narrows first, then the grant is deleted and recorded.
	if err := r.Get(ctx, client.ObjectKeyFromObject(&g), &g); err != nil {
		t.Fatal(err)
	}
	g.Spec.ExpiresAt = metav1.NewTime(time.Now().Add(-time.Second))
	if err := r.Update(ctx, &g); err != nil {
		t.Fatal(err)
	}
	if err := r.ensure(ctx, w); err != nil {
		t.Fatal(err)
	}
	if lanPolicy(t, r, w) != nil {
		t.Fatal("expired grant still in the LAN policy")
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(&g), &g); !apierrors.IsNotFound(err) {
		t.Fatal("expired grant not deleted", err)
	}
	if len(auditActions(t, r, "grant-expired-")) != 1 {
		t.Fatal("expiry not recorded")
	}
}

// Deleting an expired grant can fail; the policy must already be narrower.
func TestGrantExpiryInterruptedAfterNarrowing(t *testing.T) {
	ctx := context.Background()
	r, w := lanFixture(t)
	createProfile(t, r, sampleProfiles(t)["custom"])
	setProfile(t, r, w, "custom")
	g := grantFor(w, "desk-ssh", api.AccessGrantLAN, time.Now(), time.Hour)
	g.CreationTimestamp = metav1.Time{}
	if err := r.Create(ctx, &g); err != nil {
		t.Fatal(err)
	}
	if err := r.ensure(ctx, w); err != nil {
		t.Fatal(err)
	}
	_ = r.Get(ctx, client.ObjectKeyFromObject(&g), &g)
	g.Spec.ExpiresAt = metav1.NewTime(time.Now().Add(-time.Second))
	if err := r.Update(ctx, &g); err != nil {
		t.Fatal(err)
	}
	failing := *r
	failing.Client = failDeletes{Client: r.Client}
	if err := failing.ensure(ctx, w); err == nil {
		t.Fatal("interrupted expiry reported success")
	}
	if lanPolicy(t, r, w) != nil {
		t.Fatal("expired grant still enforced after an interrupted expiry")
	}
	// The retry finishes the job.
	if err := r.ensure(ctx, w); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(&g), &g); !apierrors.IsNotFound(err) {
		t.Fatal("retry left the expired grant", err)
	}
}

type failDeletes struct{ client.Client }

func (f failDeletes) Delete(ctx context.Context, o client.Object, opts ...client.DeleteOption) error {
	if _, ok := o.(*api.AccessGrant); ok {
		return errors.New("api server unavailable")
	}
	return f.Client.Delete(ctx, o, opts...)
}

func mustJSON(t *testing.T, v any) []string {
	t.Helper()
	b, err := yaml.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return []string{string(b)}
}
