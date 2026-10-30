package installation

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/yaml"
)

const admissionNamespace = "test-control"

// renderAdmission renders the chart and returns its admission policies,
// bindings and params ConfigMap.
func renderAdmission(t *testing.T, values map[string]any) []*unstructured.Unstructured {
	t.Helper()
	path := filepath.Join(t.TempDir(), "values.json")
	raw, _ := json.Marshal(values)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("helm", "template", "qualification", "../../charts/agent-project-controller", "-n", admissionNamespace, "-f", path).CombinedOutput()
	if err != nil {
		t.Fatalf("render: %v: %s", err, out)
	}
	var objs []*unstructured.Unstructured
	for _, doc := range bytes.Split(out, []byte("\n---")) {
		m := map[string]any{}
		if err := yaml.Unmarshal(doc, &m); err != nil {
			t.Fatal(err)
		}
		o := &unstructured.Unstructured{Object: m}
		if o.GetKind() == "ValidatingAdmissionPolicy" || o.GetKind() == "ValidatingAdmissionPolicyBinding" || o.GetKind() == "ConfigMap" && o.GetName() == "admission-params" {
			objs = append(objs, o)
		}
	}
	return objs
}

func admissionValues() map[string]any {
	return map[string]any{
		"controllerImage":  "registry.example.com/controller@sha256:" + strings.Repeat("a", 64),
		"workspaceImage":   "registry.example.com/workspace@sha256:" + strings.Repeat("b", 64),
		"additionalGitHub": map[string]any{"enabled": true, "owner": "ghorg", "existingSecret": "github-token"},
		"config": map[string]any{
			"FORGEJO_ORIGIN": "https://git.example.com", "FORGEJO_OWNER": "operator", "PUBLIC_ORIGIN": "https://projects.example.com",
			"OIDC_ISSUER": "https://identity.example.com/realm", "OIDC_CLIENT_ID": "projects",
			"LAN_GRANT_ALLOWED_CIDRS": "192.168.1.0/24, 10.30.1.128/26",
			"LAN_GRANT_DENIED_CIDRS":  "192.168.1.1 192.168.1.64/27",
			"PROVIDER_LEASES_SECRET":  "provider-broker/leases",
		},
	}
}

func TestAdmissionPolicyParamsRender(t *testing.T) {
	if os.Getenv("APC_HELM_TEST") != "true" {
		t.Skip("set APC_HELM_TEST=true with Helm installed")
	}
	values := admissionValues()
	var params *unstructured.Unstructured
	policies, bindings := 0, 0
	for _, o := range renderAdmission(t, values) {
		switch o.GetKind() {
		case "ConfigMap":
			params = o
		case "ValidatingAdmissionPolicy":
			policies++
			if fp, _, _ := unstructured.NestedString(o.Object, "spec", "failurePolicy"); fp != "Fail" {
				t.Fatalf("%s failurePolicy %q", o.GetName(), fp)
			}
		case "ValidatingAdmissionPolicyBinding":
			bindings++
			if ns, _, _ := unstructured.NestedString(o.Object, "spec", "matchResources", "namespaceSelector", "matchLabels", "kubernetes.io/metadata.name"); ns != admissionNamespace {
				t.Fatalf("%s binds outside the release namespace", o.GetName())
			}
		}
	}
	if policies != 5 || bindings != 5 || params == nil {
		t.Fatalf("policies %d, bindings %d, params %v", policies, bindings, params != nil)
	}
	data, _, _ := unstructured.NestedStringMap(params.Object, "data")
	want := map[string]string{"gitHost": "forgejo", "repositoryOwner": "operator", "additionalGitHubOwner": "ghorg", "reservedHTTPSHosts": "git.example.com",
		"lanAllowedCIDRs": "192.168.1.0/24,10.30.1.128/26", "lanDeniedCIDRs": "192.168.1.1,192.168.1.64/27", "providersEnabled": "true"}
	for k, v := range want {
		if data[k] != v {
			t.Errorf("params %s = %q, want %q", k, data[k], v)
		}
	}
	values["admissionPolicies"] = map[string]any{"enabled": false}
	if objs := renderAdmission(t, values); len(objs) != 0 {
		t.Fatalf("disabled policies rendered %d objects", len(objs))
	}
}

// TestAdmissionPoliciesOnAPIServer installs the rendered policies on a local
// API server and runs every policy's CEL against allowed and denied fixtures.
func TestAdmissionPoliciesOnAPIServer(t *testing.T) {
	if os.Getenv("APC_HELM_TEST") != "true" || os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("set APC_HELM_TEST=true with Helm installed and KUBEBUILDER_ASSETS for a local API server")
	}
	objs := renderAdmission(t, admissionValues())
	env := &envtest.Environment{CRDDirectoryPaths: []string{filepath.Join("..", "..", "config", "crd")}, ErrorIfCRDPathMissing: true}
	cfg, err := env.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := env.Stop(); err != nil {
			t.Error(err)
		}
	}()
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = api.AddToScheme(scheme)
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	for _, ns := range []string{admissionNamespace, "elsewhere"} {
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
	// Type-check warnings (status.typeChecking) come from kube-controller-manager,
	// which envtest does not run; the fixtures below exercise every expression.
	seq := 0
	workspace := func(mutate func(*api.ProjectWorkspace)) *api.ProjectWorkspace {
		seq++
		w := &api.ProjectWorkspace{ObjectMeta: metav1.ObjectMeta{Name: "project-" + strings.Repeat("x", seq%3) + string(rune('a'+seq%26)) + strings.Repeat("0", seq/26), Namespace: admissionNamespace},
			Spec: api.WorkspaceSpec{Repository: "operator/project", DesiredState: "Running"}}
		if mutate != nil {
			mutate(w)
		}
		return w
	}
	https := func(host string, paths ...string) func(*api.ProjectWorkspace) {
		return func(w *api.ProjectWorkspace) {
			w.Spec.HTTPS = []api.HTTPSRule{{Host: host, Methods: []string{"GET"}, Paths: paths}}
		}
	}
	lan := func(address string, note string) func(*api.ProjectWorkspace) {
		return func(w *api.ProjectWorkspace) {
			w.Spec.LANHosts = []api.LANHost{{Address: address, Ports: []int32{22}, Note: note}}
		}
	}
	repos := func(rs ...api.RepositoryProject) func(*api.ProjectWorkspace) {
		return func(w *api.ProjectWorkspace) { w.Spec.Repositories = rs }
	}
	// The first create waits for the bindings to take effect.
	for {
		err = c.Create(ctx, workspace(https("1.2.3.4", "/")))
		if apierrors.IsInvalid(err) || apierrors.IsForbidden(err) {
			break
		}
		if err == nil && ctx.Err() != nil {
			t.Fatal("policies never took effect")
		}
		time.Sleep(200 * time.Millisecond)
	}

	cases := []struct {
		name   string
		mutate func(*api.ProjectWorkspace)
		denied string // "" allows; otherwise a substring of the refusal
	}{
		{"minimal project", nil, ""},
		{"exact host and literal prefix", https("api.example.com", "/v1/", "/healthz"), ""},
		{"LAN host in allowlist", lan("192.168.1.20", "NAS"), ""},
		{"LAN host inside a /26 range", lan("10.30.1.130", ""), ""},
		{"repositories", repos(api.RepositoryProject{Name: "other", Host: "forgejo", Repository: "operator/other"}, api.RepositoryProject{Name: "lib", Host: "github", Repository: "someone/lib", Public: true}, api.RepositoryProject{Name: "private", Host: "github", Repository: "ghorg/private"}), ""},
		{"providers when configured", func(w *api.ProjectWorkspace) { w.Spec.Providers = []api.ProviderGrant{{Name: "grok"}} }, ""},

		{"name starts with a digit", func(w *api.ProjectWorkspace) { w.Name = "1project" }, "Project names"},
		{"name longer than 40", func(w *api.ProjectWorkspace) { w.Name = "p" + strings.Repeat("a", 40) }, "Project names"},
		{"repository of another owner", func(w *api.ProjectWorkspace) { w.Spec.Repository = "someone/project" }, "configured owner operator"},
		{"IP literal host", https("10.0.0.1", "/"), "exact lowercase DNS hosts"},
		{"empty label", https("api..example.com", "/"), "exact lowercase DNS hosts"},
		{"overlong label", https(strings.Repeat("a", 64)+".example.com", "/"), "exact lowercase DNS hosts"},
		{"wildcard host", https("*.example.com", "/"), "spec.https[0].host"},
		{"wildcard path", https("api.example.com", "/v1/*"), "literal prefixes"},
		{"regex path", https("api.example.com", "/v1/.*#"), "literal prefixes"},
		{"query path", https("api.example.com", "/search?q=x"), "literal prefixes"},
		{"percent-encoded path", https("api.example.com", "/a%2fb"), "literal prefixes"},
		{"dot-dot path", https("api.example.com", "/a/../b"), "literal prefixes"},
		{"double slash path", https("api.example.com", "//evil.example"), "literal prefixes"},
		{"backslash path", https("api.example.com", `/a\b`), "literal prefixes"},
		{"Git host override", https("git.example.com", "/"), "controller-managed"},
		{"GitHub override with GitHub enrollment", func(w *api.ProjectWorkspace) {
			https("github.com", "/")(w)
			repos(api.RepositoryProject{Name: "lib", Host: "github", Repository: "someone/lib", Public: true})(w)
		}, "controller-managed"},
		{"denylisted address", lan("192.168.1.1", ""), "denylist"},
		{"denylisted range", lan("192.168.1.70", ""), "denylist"},
		{"outside allowlist", lan("192.168.2.5", ""), "allowed LAN ranges 192.168.1.0/24, 10.30.1.128/26"},
		{"loopback", lan("127.0.0.1", ""), "Refused"},
		{"metadata address", lan("169.254.169.254", ""), "Refused"},
		{"network address", lan("192.168.1.0", ""), "network or broadcast"},
		{"broadcast address", lan("192.168.1.255", ""), "network or broadcast"},
		{"network of a /26", lan("10.30.1.128", ""), "network or broadcast"},
		{"broadcast of a /26", lan("10.30.1.191", ""), "network or broadcast"},
		{"multi-line note", lan("192.168.1.20", "line\nbreak"), "printable"},
		{"Forgejo repository of another owner", repos(api.RepositoryProject{Name: "x", Host: "forgejo", Repository: "someone/x"}), "Forgejo enrollment"},
		{"public Forgejo enrollment", repos(api.RepositoryProject{Name: "x", Host: "forgejo", Repository: "operator/x", Public: true}), "Forgejo enrollment"},
		{"private GitHub of another owner", repos(api.RepositoryProject{Name: "x", Host: "github", Repository: "otherorg/x"}), "trusted GitHub authorization"},
		{"duplicate enrollment", repos(api.RepositoryProject{Name: "a", Host: "forgejo", Repository: "operator/x"}, api.RepositoryProject{Name: "b", Host: "forgejo", Repository: "operator/x"}), "Duplicate"},
		{"primary enrolled again", repos(api.RepositoryProject{Name: "a", Host: "forgejo", Repository: "operator/project"}), "Duplicate"},
	}
	for _, tc := range cases {
		err := c.Create(ctx, workspace(tc.mutate))
		switch {
		case tc.denied == "" && err != nil:
			t.Errorf("%s: refused: %v", tc.name, err)
		case tc.denied != "" && err == nil:
			t.Errorf("%s: accepted", tc.name)
		case tc.denied != "" && !strings.Contains(err.Error(), tc.denied):
			t.Errorf("%s: refused for another reason: %v", tc.name, err)
		}
	}

	// Policies are bound to the release namespace only.
	stray := workspace(https("1.2.3.4", "/"))
	stray.Namespace = "elsewhere"
	if err = c.Create(ctx, stray); err != nil {
		t.Fatalf("policy applied outside its release namespace: %v", err)
	}

	// A narrower setting never blocks unrelated edits or finalizer removal of
	// an existing object; changing the refused field is checked again.
	granted := workspace(lan("192.168.1.20", ""))
	granted.Finalizers = []string{api.Finalizer}
	if err = c.Create(ctx, granted); err != nil {
		t.Fatal(err)
	}
	params := &corev1.ConfigMap{}
	if err = c.Get(ctx, types.NamespacedName{Namespace: admissionNamespace, Name: "admission-params"}, params); err != nil {
		t.Fatal(err)
	}
	params.Data["lanAllowedCIDRs"] = "10.30.1.128/26"
	params.Data["providersEnabled"] = "false"
	if err = c.Update(ctx, params); err != nil {
		t.Fatal(err)
	}
	eventually(ctx, t, "narrowed params", func() bool {
		return c.Create(ctx, workspace(lan("192.168.1.21", ""))) != nil
	})
	if err = c.Create(ctx, workspace(func(w *api.ProjectWorkspace) { w.Spec.Providers = []api.ProviderGrant{{Name: "grok"}} })); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("providers accepted after they were turned off: %v", err)
	}
	update := func(mutate func(*api.ProjectWorkspace)) error {
		w := &api.ProjectWorkspace{}
		if err := c.Get(ctx, client.ObjectKeyFromObject(granted), w); err != nil {
			t.Fatal(err)
		}
		mutate(w)
		return c.Update(ctx, w)
	}
	if err = update(func(w *api.ProjectWorkspace) {
		w.Annotations = map[string]string{"projects.agentwork.dev/requested-by": "operator"}
	}); err != nil {
		t.Fatalf("unrelated edit blocked: %v", err)
	}
	if err = update(func(w *api.ProjectWorkspace) {
		w.Spec.LANHosts = append(w.Spec.LANHosts, api.LANHost{Address: "10.30.1.140", Ports: []int32{443}})
	}); err == nil || !strings.Contains(err.Error(), "allowed LAN ranges") {
		t.Fatalf("grant added beside a now-refused grant: %v", err)
	}
	if err = update(func(w *api.ProjectWorkspace) { w.Spec.LANHosts = nil }); err != nil {
		t.Fatalf("withdrawing a refused grant blocked: %v", err)
	}
	if err = c.Delete(ctx, granted); err != nil {
		t.Fatal(err)
	}
	if err = update(func(w *api.ProjectWorkspace) { w.Finalizers = nil }); err != nil {
		t.Fatalf("finalizer removal blocked: %v", err)
	}

	// Without its params the policies fail closed.
	if err = c.Delete(ctx, params); err != nil {
		t.Fatal(err)
	}
	eventually(ctx, t, "missing params", func() bool {
		return c.Create(ctx, workspace(nil)) != nil
	})
}

func eventually(ctx context.Context, t *testing.T, what string, ok func() bool) {
	t.Helper()
	for !ok() {
		if ctx.Err() != nil {
			t.Fatalf("%s: timed out", what)
		}
		time.Sleep(200 * time.Millisecond)
	}
}
