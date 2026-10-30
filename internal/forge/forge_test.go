package forge

import (
	"context"
	"strings"
	"testing"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/forgejo"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const adminToken = "synthetic-forge-admin-credential"

func class(name, driver string, mutate ...func(*api.ForgeClass)) *api.ForgeClass {
	fc := &api.ForgeClass{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: api.ForgeClassSpec{Driver: driver, BaseURL: "https://git.example.com", CredentialsSecretRef: api.SecretKeyReference{Name: "controller-secrets", Key: "forgejo-token"}, Parameters: map[string]string{"owner": "operator"}}}
	for _, m := range mutate {
		m(fc)
	}
	return fc
}

func resolver(t *testing.T, objects ...client.Object) *Resolver {
	t.Helper()
	s := runtime.NewScheme()
	if err := api.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	_ = corev1.AddToScheme(s)
	objects = append(objects,
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "controller-secrets", Namespace: "control"}, Data: map[string][]byte{"forgejo-token": []byte(adminToken + "\n"), "empty": nil}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "elsewhere", Namespace: "other"}, Data: map[string][]byte{"forgejo-token": []byte("other-namespace-credential")}},
	)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(objects...).Build()
	return &Resolver{Client: c, Namespace: "control", Default: &Resolved{Driver: "forgejo", Origin: "https://default.example.com", Owner: "steve"}}
}

func TestEmptyNameKeepsConfiguredForge(t *testing.T) {
	r := resolver(t)
	f, err := r.Resolve(context.Background(), "")
	if err != nil || f != r.Default {
		t.Fatal("empty class did not resolve to the configured forge", f, err)
	}
	r.Default = nil
	if _, err := r.Resolve(context.Background(), ""); err == nil {
		t.Fatal("no configured forge accepted")
	}
}

func TestForgejoClassResolves(t *testing.T) {
	r := resolver(t, class("second", DriverForgejo))
	f, err := r.Resolve(context.Background(), "second")
	if err != nil {
		t.Fatal(err)
	}
	c, ok := f.Forge.(*forgejo.Client)
	if !ok || f.Class != "second" || f.Driver != "forgejo" || f.Origin != "https://git.example.com" || f.Owner != "operator" || c.AdminToken != adminToken {
		t.Fatalf("resolved %+v", f)
	}
}

func TestUnsupportedOrInvalidClassesFailClosed(t *testing.T) {
	cases := map[string]*api.ForgeClass{
		"github":     class("c", DriverGitHub),
		"gitlab":     class("c", DriverGitLab),
		"unknown":    class("c", "bitbucket"),
		"http":       class("c", DriverForgejo, func(fc *api.ForgeClass) { fc.Spec.BaseURL = "http://git.example.com" }),
		"path":       class("c", DriverForgejo, func(fc *api.ForgeClass) { fc.Spec.BaseURL = "https://git.example.com/sub" }),
		"userinfo":   class("c", DriverForgejo, func(fc *api.ForgeClass) { fc.Spec.BaseURL = "https://user@git.example.com" }),
		"github.com": class("c", DriverForgejo, func(fc *api.ForgeClass) { fc.Spec.BaseURL = "https://github.com" }),
		"namespace": class("c", DriverForgejo, func(fc *api.ForgeClass) {
			fc.Spec.CredentialsSecretRef = api.SecretKeyReference{Name: "elsewhere", Namespace: "other", Key: "forgejo-token"}
		}),
		"no key":      class("c", DriverForgejo, func(fc *api.ForgeClass) { fc.Spec.CredentialsSecretRef.Key = "" }),
		"empty key":   class("c", DriverForgejo, func(fc *api.ForgeClass) { fc.Spec.CredentialsSecretRef.Key = "empty" }),
		"absent key":  class("c", DriverForgejo, func(fc *api.ForgeClass) { fc.Spec.CredentialsSecretRef.Key = "absent" }),
		"no secret":   class("c", DriverForgejo, func(fc *api.ForgeClass) { fc.Spec.CredentialsSecretRef.Name = "missing" }),
		"no owner":    class("c", DriverForgejo, func(fc *api.ForgeClass) { fc.Spec.Parameters = nil }),
		"extra param": class("c", DriverForgejo, func(fc *api.ForgeClass) { fc.Spec.Parameters["insecure"] = "true" }),
	}
	for name, fc := range cases {
		r := resolver(t, fc)
		f, err := r.Resolve(context.Background(), "c")
		if err == nil || f != nil {
			t.Errorf("%s: accepted %+v", name, f)
			continue
		}
		if strings.Contains(err.Error(), adminToken) || strings.Contains(err.Error(), "other-namespace-credential") {
			t.Errorf("%s: error exposes a credential: %v", name, err)
		}
	}
	if _, err := resolver(t).Resolve(context.Background(), "missing"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatal("missing class", err)
	}
}

// The resolver never reads a credential outside the controller's namespace,
// even for a class that names one.
func TestCredentialNamespaceIsTheControllers(t *testing.T) {
	fc := class("c", DriverForgejo, func(fc *api.ForgeClass) { fc.Spec.CredentialsSecretRef.Namespace = "control" })
	r := resolver(t, fc)
	if _, err := r.Resolve(context.Background(), "c"); err != nil {
		t.Fatal("explicit controller namespace refused", err)
	}
	if err := ValidateSpec(class("c", DriverForgejo, func(fc *api.ForgeClass) { fc.Spec.CredentialsSecretRef.Namespace = "other" }).Spec, "control"); err == nil {
		t.Fatal("foreign credential namespace accepted")
	}
}

func TestNamedClassWithoutAPIFailsClosed(t *testing.T) {
	s := runtime.NewScheme()
	_ = corev1.AddToScheme(s)
	r := &Resolver{Client: fake.NewClientBuilder().WithScheme(s).Build(), Namespace: "control", Default: &Resolved{Driver: "forgejo", Origin: "https://default.example.com", Owner: "steve"}}
	if _, err := r.Resolve(context.Background(), "second"); err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatal("named class resolved without the ForgeClass API", err)
	}
	if f, err := r.Resolve(context.Background(), ""); err != nil || f != r.Default {
		t.Fatal("configured forge needs the ForgeClass API", err)
	}
}
