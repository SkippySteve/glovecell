package installation

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/yaml"
)

// TestAccessAdmissionPoliciesOnAPIServer runs the AccessGrant and
// AccessProfile policies on a local API server: grant lifetime and owner,
// LAN ranges, who may write grants, and read-only profiles.
func TestAccessAdmissionPoliciesOnAPIServer(t *testing.T) {
	if os.Getenv("APC_HELM_TEST") != "true" || os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("set APC_HELM_TEST=true with Helm installed and KUBEBUILDER_ASSETS for a local API server")
	}
	objs := renderAdmission(t, admissionValues())
	env := &envtest.Environment{CRDDirectoryPaths: []string{filepath.Join("..", "..", "config", "crd")}, ErrorIfCRDPathMissing: true}
	cfg, err := env.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = env.Stop() }()
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = api.AddToScheme(scheme)
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	for _, ns := range []string{admissionNamespace, "apc-agent"} {
		if err = c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}); err != nil {
			t.Fatal(err)
		}
	}
	for _, o := range objs {
		if o.GetKind() == "ConfigMap" {
			o.SetNamespace(admissionNamespace)
		}
		if err = c.Create(ctx, o); err != nil {
			t.Fatalf("install %s %s: %v", o.GetKind(), o.GetName(), err)
		}
	}
	cell := &api.ProjectWorkspace{ObjectMeta: metav1.ObjectMeta{Name: "project", Namespace: admissionNamespace}, Spec: api.WorkspaceSpec{Repository: "operator/project", DesiredState: "Running"}}
	if err = c.Create(ctx, cell); err != nil {
		t.Fatal(err)
	}
	seq := 0
	grant := func(mutate func(*api.AccessGrant)) *api.AccessGrant {
		seq++
		g := &api.AccessGrant{ObjectMeta: metav1.ObjectMeta{Name: "grant-" + string(rune('a'+seq%26)) + strings.Repeat("0", seq/26), Namespace: admissionNamespace,
			Labels:          map[string]string{api.CellLabel: "project"},
			OwnerReferences: []metav1.OwnerReference{{APIVersion: api.GroupVersion.String(), Kind: "ProjectWorkspace", Name: "project", UID: cell.UID}}},
			Spec: api.AccessGrantSpec{Cell: "project", Type: api.AccessGrantLAN, LAN: &api.LANGrant{Address: "192.168.1.20", Ports: []int32{22}}, ExpiresAt: metav1.NewTime(time.Now().Add(time.Hour))}}
		if mutate != nil {
			mutate(g)
		}
		return g
	}
	expires := func(d time.Duration) func(*api.AccessGrant) {
		return func(g *api.AccessGrant) { g.Spec.ExpiresAt = metav1.NewTime(time.Now().Add(d)) }
	}
	// Wait for the bindings to take effect.
	eventually(ctx, t, "access policies", func() bool { return c.Create(ctx, grant(expires(48*time.Hour))) != nil })

	cases := []struct {
		name   string
		mutate func(*api.AccessGrant)
		denied string
	}{
		{"one-hour LAN grant", nil, ""},
		{"eight-hour internet window", func(g *api.AccessGrant) {
			g.Spec.Type, g.Spec.LAN, g.Spec.Internet = api.AccessGrantInternet, nil, &api.InternetGrant{}
			g.Spec.ExpiresAt = metav1.NewTime(time.Now().Add(7*time.Hour + 50*time.Minute))
		}, ""},
		{"internet window beyond 8h", func(g *api.AccessGrant) {
			g.Spec.Type, g.Spec.LAN, g.Spec.Internet = api.AccessGrantInternet, nil, &api.InternetGrant{}
			g.Spec.ExpiresAt = metav1.NewTime(time.Now().Add(9 * time.Hour))
		}, "at most"},
		{"LAN grant beyond 24h", expires(25 * time.Hour), "at most"},
		{"already expired", expires(-time.Minute), "after creation"},
		{"no cell label", func(g *api.AccessGrant) { g.Labels = nil }, "cell label"},
		{"label names another cell", func(g *api.AccessGrant) { g.Labels[api.CellLabel] = "other" }, "cell label"},
		{"not owned by the cell", func(g *api.AccessGrant) { g.OwnerReferences = nil }, "owned by"},
		{"outside the LAN allowlist", func(g *api.AccessGrant) { g.Spec.LAN.Address = "192.168.2.5" }, "allowed LAN ranges"},
		{"denylisted address", func(g *api.AccessGrant) { g.Spec.LAN.Address = "192.168.1.1" }, "denylist"},
		{"metadata address", func(g *api.AccessGrant) { g.Spec.LAN.Address = "169.254.169.254" }, "allowed LAN ranges"},
	}
	for _, tc := range cases {
		err := c.Create(ctx, grant(tc.mutate))
		switch {
		case tc.denied == "" && err != nil:
			t.Errorf("%s: refused: %v", tc.name, err)
		case tc.denied != "" && err == nil:
			t.Errorf("%s: accepted", tc.name)
		case tc.denied != "" && !strings.Contains(err.Error(), tc.denied):
			t.Errorf("%s: refused for another reason: %v", tc.name, err)
		}
	}

	// A grant can end earlier, never later.
	g := grant(nil)
	if err = c.Create(ctx, g); err != nil {
		t.Fatal(err)
	}
	g.Spec.ExpiresAt = metav1.NewTime(g.Spec.ExpiresAt.Add(time.Hour))
	if err = c.Update(ctx, g); err == nil || !strings.Contains(err.Error(), "never later") {
		t.Fatalf("extension accepted: %v", err)
	}
	if err = c.Get(ctx, client.ObjectKeyFromObject(g), g); err != nil {
		t.Fatal(err)
	}
	g.Spec.ExpiresAt = metav1.NewTime(g.Spec.ExpiresAt.Add(-30 * time.Minute))
	if err = c.Update(ctx, g); err != nil {
		t.Fatalf("ending earlier refused: %v", err)
	}

	// Only the controller writes grants: an agent's ServiceAccount is refused
	// even when RBAC would let it.
	role := &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "grant-writer"}, Rules: []rbacv1.PolicyRule{{APIGroups: []string{"projects.agentwork.dev"}, Resources: []string{"accessgrants"}, Verbs: []string{"create"}}}}
	if err = c.Create(ctx, role); err != nil {
		t.Fatal(err)
	}
	controller, agent := "system:serviceaccount:"+admissionNamespace+":controller", "system:serviceaccount:apc-agent:default"
	binding := &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "grant-writer"}, RoleRef: rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "grant-writer"},
		Subjects: []rbacv1.Subject{{Kind: "ServiceAccount", Name: "controller", Namespace: admissionNamespace}, {Kind: "ServiceAccount", Name: "default", Namespace: "apc-agent"}}}
	if err = c.Create(ctx, binding); err != nil {
		t.Fatal(err)
	}
	as := func(user string) client.Client {
		ic := rest.CopyConfig(cfg)
		ic.Impersonate = rest.ImpersonationConfig{UserName: user, Groups: []string{"system:serviceaccounts", "system:authenticated"}}
		cl, err := client.New(ic, client.Options{Scheme: scheme})
		if err != nil {
			t.Fatal(err)
		}
		return cl
	}
	eventually(ctx, t, "RBAC", func() bool { return as(controller).Create(ctx, grant(nil)) == nil })
	// Same RBAC as the controller, so the refusal is the policy's.
	if err = as(agent).Create(ctx, grant(nil)); err == nil || !strings.Contains(err.Error(), "Only the project controller") {
		t.Fatalf("an agent ServiceAccount created a grant: %v", err)
	}

	// Profiles: the samples pass; writes, unbounded open and stray internet grants do not.
	raw, err := os.ReadFile("../../config/samples/accessprofiles.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, doc := range strings.Split(string(raw), "\n---\n") {
		p := &api.AccessProfile{}
		if err = yaml.UnmarshalStrict([]byte(doc), p); err != nil {
			t.Fatal(err)
		}
		if err = c.Create(ctx, p); err != nil {
			t.Fatalf("sample profile %s refused: %v", p.Name, err)
		}
	}
	for name, spec := range map[string]api.AccessProfileSpec{
		"post-anywhere": {Level: api.AccessLevelCustom, HTTPS: []api.HTTPSRule{{Host: "api.example.com", Methods: []string{"POST"}, Paths: []string{"/"}}}},
		"push":          {Level: api.AccessLevelCustom, HTTPS: []api.HTTPSRule{{Host: "github.com", Methods: []string{"POST"}, Paths: []string{"/o/r/git-receive-pack"}}}},
		"open-forever":  {Level: api.AccessLevelOpen, AllowedGrants: []api.AccessGrantType{api.AccessGrantInternet}},
		"custom-window": {Level: api.AccessLevelCustom, AllowedGrants: []api.AccessGrantType{api.AccessGrantInternet}, MaxGrantDuration: map[api.AccessGrantType]metav1.Duration{api.AccessGrantInternet: {Duration: time.Hour}}},
	} {
		if err = c.Create(ctx, &api.AccessProfile{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: spec}); err == nil {
			t.Errorf("profile %s accepted", name)
		}
	}
}
