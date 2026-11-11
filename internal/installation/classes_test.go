package installation

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/forge"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/providerbroker"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/yaml"
)

var classResources = []string{"providerclasses", "forgeclasses", "providerclasses/status", "forgeclasses/status", "*"}

func classValues(t *testing.T, config map[string]any, extra map[string]any) string {
	t.Helper()
	base := map[string]any{"FORGEJO_ORIGIN": "https://git.example.com", "FORGEJO_OWNER": "operator", "PUBLIC_ORIGIN": "https://projects.example.com", "OIDC_ISSUER": "https://identity.example.com/realm", "OIDC_CLIENT_ID": "projects"}
	for k, v := range config {
		base[k] = v
	}
	values := map[string]any{"controllerImage": "registry.example.com/controller@sha256:" + strings.Repeat("a", 64), "workspaceImage": "registry.example.com/workspace@sha256:" + strings.Repeat("b", 64), "config": base, "controlClient": map[string]any{"enabled": true}}
	for k, v := range extra {
		values[k] = v
	}
	path := filepath.Join(t.TempDir(), "values.json")
	raw, _ := json.Marshal(values)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func renderDocs(t *testing.T, values string) []map[string]any {
	t.Helper()
	out, err := exec.Command("helm", "template", "qualification", "../../charts/agent-project-controller", "-n", "test-control", "-f", values).CombinedOutput()
	if err != nil {
		t.Fatalf("render: %v: %s", err, out)
	}
	var docs []map[string]any
	for _, doc := range strings.Split(string(out), "\n---\n") {
		var obj map[string]any
		if yaml.Unmarshal([]byte(doc), &obj) == nil && obj != nil && obj["kind"] != nil {
			docs = append(docs, obj)
		}
	}
	return docs
}

func ofKind(docs []map[string]any, kind string) []map[string]any {
	var out []map[string]any
	for _, d := range docs {
		if d["kind"] == kind {
			out = append(out, d)
		}
	}
	return out
}

// forgeClassPolicies returns the ForgeClass policy and binding (classes.yaml),
// leaving out the chart's ProjectWorkspace and access-level policies.
func forgeClassPolicies(docs []map[string]any) []map[string]any {
	var out []map[string]any
	for _, kind := range []string{"ValidatingAdmissionPolicy", "ValidatingAdmissionPolicyBinding"} {
		for _, d := range ofKind(docs, kind) {
			if meta, _ := d["metadata"].(map[string]any); meta != nil && strings.HasSuffix(fmt.Sprint(meta["name"]), "-forgeclasses") {
				out = append(out, d)
			}
		}
	}
	return out
}

func convert(t *testing.T, in map[string]any, out any) {
	t.Helper()
	raw, _ := json.Marshal(in)
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatal(err)
	}
}

// The chart's generated classes are equivalent to the settings they come
// from, so an upgrade changes nothing for existing workspaces.
func TestHelmDefaultClasses(t *testing.T) {
	if os.Getenv("APC_HELM_TEST") != "true" {
		t.Skip("set APC_HELM_TEST=true with Helm installed")
	}
	docs := renderDocs(t, classValues(t, nil, nil))
	if len(ofKind(docs, "ProviderClass")) != 0 {
		t.Fatal("ProviderClasses rendered without PROVIDER_LEASES_SECRET")
	}
	forges := ofKind(docs, "ForgeClass")
	if len(forges) != 1 {
		t.Fatalf("%d ForgeClasses", len(forges))
	}
	var fc api.ForgeClass
	convert(t, forges[0], &fc)
	if fc.Spec.BaseURL != "https://git.example.com" || fc.Spec.Parameters["owner"] != "operator" || fc.Spec.CredentialsSecretRef != (api.SecretKeyReference{Name: "controller-secrets", Namespace: "test-control", Key: "forgejo-token"}) {
		t.Fatalf("default ForgeClass %+v", fc.Spec)
	}
	if err := forge.ValidateSpec(fc.Spec, "test-control"); err != nil {
		t.Fatal("controller refuses the chart's ForgeClass", err)
	}

	docs = renderDocs(t, classValues(t, map[string]any{"PROVIDER_LEASES_SECRET": "provider-broker/provider-leases"}, nil))
	leases := types.NamespacedName{Namespace: "provider-broker", Name: "provider-leases"}
	var providers []string
	for _, doc := range ofKind(docs, "ProviderClass") {
		var pc api.ProviderClass
		convert(t, doc, &pc)
		if pc.Annotations[api.DefaultClassAnnotation] != "true" {
			t.Fatal("generated ProviderClass is not a default", pc.Name)
		}
		got, err := providerbroker.ValidateClass(&pc, &leases)
		want := providerbroker.Class{Name: pc.Name, Provider: pc.Spec.Provider, Driver: providerbroker.DriverProviderBroker, Leases: leases, MinDuration: providerbroker.DefaultMinLease}
		if err != nil || got != want {
			t.Fatalf("chart ProviderClass %s resolves to %+v (%v), want the built-in default", pc.Name, got, err)
		}
		providers = append(providers, pc.Spec.Provider)
	}
	slices.Sort(providers)
	if !slices.Equal(providers, []string{"claude", "codex", "cursor", "grok"}) {
		t.Fatal("default ProviderClasses", providers)
	}

	github := map[string]any{"GIT_HOST": "github", "GITHUB_OWNER": "owner", "GITHUB_APP_ID": "12345", "GITHUB_APP_INSTALLATION_ID": "99", "FORGEJO_ORIGIN": "", "FORGEJO_OWNER": ""}
	if docs = renderDocs(t, classValues(t, github, nil)); len(ofKind(docs, "ForgeClass")) != 0 {
		t.Fatal("GIT_HOST=github rendered a ForgeClass; github is not a supported class driver")
	}
	docs = renderDocs(t, classValues(t, map[string]any{"PROVIDER_LEASES_SECRET": "provider-broker/provider-leases"}, map[string]any{"classes": map[string]any{"createDefaults": false, "admissionPolicy": false}}))
	if len(ofKind(docs, "ForgeClass"))+len(ofKind(docs, "ProviderClass"))+len(forgeClassPolicies(docs)) != 0 {
		t.Fatal("classes rendered with createDefaults and admissionPolicy off")
	}
	if len(forgeClassPolicies(renderDocs(t, classValues(t, nil, nil)))) != 2 {
		t.Fatal("ForgeClass policy and binding not rendered by default")
	}
	// admissionPolicies.enabled=false (a cluster without the admission API)
	// turns the ForgeClass policy off with the chart's other policies.
	docs = renderDocs(t, classValues(t, nil, map[string]any{"admissionPolicies": map[string]any{"enabled": false}}))
	if len(forgeClassPolicies(docs)) != 0 || len(ofKind(docs, "ForgeClass")) != 1 {
		t.Fatal("ForgeClass policy rendered with admissionPolicies.enabled=false")
	}
}

// Classes are admin-only: the controller reads them and writes only their
// status; the class admin role is bound to nobody; no other role, including
// the control client's (an agent runtime), mentions them.
func TestHelmClassRBAC(t *testing.T) {
	if os.Getenv("APC_HELM_TEST") != "true" {
		t.Skip("set APC_HELM_TEST=true with Helm installed")
	}
	docs := renderDocs(t, classValues(t, map[string]any{"PROVIDER_LEASES_SECRET": "provider-broker/provider-leases"}, nil))
	checkClassRBAC(t, docs, "test-control-qualification", "test-control-qualification-class-admin")
}

func TestDeployClassRBAC(t *testing.T) {
	raw, err := os.ReadFile("../../config/deploy/rbac.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var docs []map[string]any
	for _, doc := range strings.Split(string(raw), "\n---\n") {
		var obj map[string]any
		if err := yaml.Unmarshal([]byte(doc), &obj); err != nil {
			t.Fatal(err)
		}
		docs = append(docs, obj)
	}
	checkClassRBAC(t, docs, "agent-project-controller", "")
}

func checkClassRBAC(t *testing.T, docs []map[string]any, controllerRole, adminRole string) {
	t.Helper()
	sawController, sawAdmin := false, adminRole == ""
	for _, doc := range docs {
		switch doc["kind"] {
		case "Role", "ClusterRole":
			var role rbacv1.ClusterRole
			convert(t, doc, &role)
			for _, rule := range role.Rules {
				if !slices.Contains(rule.APIGroups, api.GroupVersion.Group) && !slices.Contains(rule.APIGroups, "*") {
					continue
				}
				for _, resource := range rule.Resources {
					if !slices.Contains(classResources, resource) {
						continue
					}
					switch {
					case doc["kind"] == "ClusterRole" && role.Name == controllerRole:
						sawController = true
						allowed := []string{"get", "list", "watch"}
						if strings.HasSuffix(resource, "/status") {
							allowed = []string{"get", "update", "patch"}
						}
						for _, verb := range rule.Verbs {
							if !slices.Contains(allowed, verb) {
								t.Fatalf("controller may %s %s", verb, resource)
							}
						}
					case doc["kind"] == "ClusterRole" && role.Name == adminRole:
						sawAdmin = true
					default:
						t.Fatalf("%s %s grants %v on %s", doc["kind"], role.Name, rule.Verbs, resource)
					}
				}
			}
		case "RoleBinding", "ClusterRoleBinding":
			var binding rbacv1.ClusterRoleBinding
			convert(t, doc, &binding)
			if adminRole != "" && binding.RoleRef.Name == adminRole {
				t.Fatalf("%s binds the class admin role", binding.Name)
			}
		}
	}
	if !sawController || !sawAdmin {
		t.Fatal("class rules not found; the test checks nothing", sawController, sawAdmin)
	}
}

// The admission policy rejects what the controller would refuse anyway, and
// admits the chart's own defaults. Needs Helm and a local API server.
func TestHelmClassAdmissionPolicy(t *testing.T) {
	if os.Getenv("APC_HELM_TEST") != "true" || os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("set APC_HELM_TEST=true with Helm installed and KUBEBUILDER_ASSETS")
	}
	env := &envtest.Environment{CRDDirectoryPaths: []string{filepath.Join("..", "..", "config", "crd")}, ErrorIfCRDPathMissing: true}
	cfg, err := env.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = env.Stop() }()
	s := runtime.NewScheme()
	_ = api.AddToScheme(s)
	_ = corev1.AddToScheme(s)
	c, err := client.New(cfg, client.Options{Scheme: s})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	docs := renderDocs(t, classValues(t, map[string]any{"PROVIDER_LEASES_SECRET": "provider-broker/provider-leases"}, nil))
	for _, kind := range []string{"ValidatingAdmissionPolicy", "ValidatingAdmissionPolicyBinding"} {
		for _, doc := range ofKind(docs, kind) {
			if err := c.Create(ctx, &unstructured.Unstructured{Object: doc}); err != nil {
				t.Fatal(kind, err)
			}
		}
	}
	foreign := &api.ForgeClass{ObjectMeta: metav1.ObjectMeta{Name: "foreign"}, Spec: api.ForgeClassSpec{Driver: "forgejo", BaseURL: "https://git.example.com", CredentialsSecretRef: api.SecretKeyReference{Name: "token", Namespace: "elsewhere", Key: "token"}, Parameters: map[string]string{"owner": "operator"}}}
	// The policy takes effect asynchronously.
	deadline := time.Now().Add(30 * time.Second)
	for {
		err = c.Create(ctx, foreign.DeepCopyObject().(client.Object))
		if err != nil || time.Now().After(deadline) {
			break
		}
		_ = c.Delete(ctx, foreign)
		time.Sleep(200 * time.Millisecond)
	}
	if err == nil || !strings.Contains(err.Error(), "controller's namespace") {
		t.Fatal("foreign credential namespace admitted", err)
	}
	github := foreign.DeepCopyObject().(*api.ForgeClass)
	github.Name, github.Spec.Driver, github.Spec.CredentialsSecretRef.Namespace = "github", "github", ""
	if err = c.Create(ctx, github); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatal("reserved forge driver admitted", err)
	}
	local := foreign.DeepCopyObject().(*api.ForgeClass)
	local.Name, local.Spec.CredentialsSecretRef.Namespace = "local", "test-control"
	if err = c.Create(ctx, local); err != nil {
		t.Fatal("class in the controller namespace refused", err)
	}
	for _, kind := range []string{"ForgeClass", "ProviderClass"} {
		for _, doc := range ofKind(docs, kind) {
			if err := c.Create(ctx, &unstructured.Unstructured{Object: doc}); err != nil {
				t.Fatal("chart default refused", kind, err)
			}
		}
	}
}
