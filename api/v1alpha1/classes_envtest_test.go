package v1alpha1

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

// The class schemas refuse what the controller would refuse anyway, so a bad
// class is rejected at admission rather than only reported in status.
func TestCRDClassValidation(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("set KUBEBUILDER_ASSETS to run a local Kubernetes API server")
	}
	env := &envtest.Environment{CRDDirectoryPaths: []string{filepath.Join("..", "..", "config", "crd")}, ErrorIfCRDPathMissing: true}
	cfg, e := env.Start()
	if e != nil {
		t.Fatal(e)
	}
	defer func() {
		if e := env.Stop(); e != nil {
			t.Error(e)
		}
	}()
	s := runtime.NewScheme()
	_ = AddToScheme(s)
	_ = corev1.AddToScheme(s)
	c, e := client.New(cfg, client.Options{Scheme: s})
	if e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()

	forge := func(name string) *ForgeClass {
		return &ForgeClass{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: ForgeClassSpec{Driver: "forgejo", BaseURL: "https://git.example.com", CredentialsSecretRef: SecretKeyReference{Name: "controller-secrets", Key: "forgejo-token"}, Parameters: map[string]string{"owner": "operator"}}}
	}
	fc := forge("forgejo")
	if e = c.Create(ctx, fc); e != nil {
		t.Fatal("valid ForgeClass", e)
	}
	for name, mutate := range map[string]func(*ForgeClass){
		"driver":    func(f *ForgeClass) { f.Spec.Driver = "bitbucket" },
		"http":      func(f *ForgeClass) { f.Spec.BaseURL = "http://git.example.com" },
		"path":      func(f *ForgeClass) { f.Spec.BaseURL = "https://git.example.com/forge" },
		"no key":    func(f *ForgeClass) { f.Spec.CredentialsSecretRef.Key = "" },
		"bad ns":    func(f *ForgeClass) { f.Spec.CredentialsSecretRef.Namespace = "Not_A_Namespace" },
		"no secret": func(f *ForgeClass) { f.Spec.CredentialsSecretRef = SecretKeyReference{} },
	} {
		bad := forge("bad")
		mutate(bad)
		if e = c.Create(ctx, bad); !apierrors.IsInvalid(e) {
			t.Errorf("%s: accepted: %v", name, e)
			_ = c.Delete(ctx, bad)
		}
	}
	// Reserved drivers are in the schema; the controller refuses them.
	reserved := forge("github")
	reserved.Spec.Driver = "github"
	if e = c.Create(ctx, reserved); e != nil {
		t.Fatal("reserved driver name refused by the schema", e)
	}
	for name, mutate := range map[string]func(*ForgeClass){
		"driver":  func(f *ForgeClass) { f.Spec.Driver = "gitlab" },
		"baseURL": func(f *ForgeClass) { f.Spec.BaseURL = "https://other.example.com" },
		"owner":   func(f *ForgeClass) { f.Spec.Parameters["owner"] = "someone-else" },
	} {
		var current ForgeClass
		if e = c.Get(ctx, types.NamespacedName{Name: "forgejo"}, &current); e != nil {
			t.Fatal(e)
		}
		mutate(&current)
		if e = c.Update(ctx, &current); !apierrors.IsInvalid(e) {
			t.Errorf("ForgeClass %s change accepted: %v", name, e)
		}
	}
	var current ForgeClass
	_ = c.Get(ctx, types.NamespacedName{Name: "forgejo"}, &current)
	current.Spec.CredentialsSecretRef.Key = "rotated"
	if e = c.Update(ctx, &current); e != nil {
		t.Fatal("credential rotation refused", e)
	}

	provider := func(name string) *ProviderClass {
		return &ProviderClass{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: ProviderClassSpec{Driver: "provider-broker", Provider: "grok"}}
	}
	pc := provider("grok")
	pc.Spec.Lease = &ProviderLeaseBounds{MinDuration: &metav1.Duration{Duration: 2 * time.Minute}, MaxDuration: &metav1.Duration{Duration: 10 * time.Minute}}
	if e = c.Create(ctx, pc); e != nil {
		t.Fatal("valid ProviderClass", e)
	}
	for name, mutate := range map[string]func(*ProviderClass){
		"min short": func(p *ProviderClass) {
			p.Spec.Lease = &ProviderLeaseBounds{MinDuration: &metav1.Duration{Duration: 30 * time.Second}}
		},
		"max low": func(p *ProviderClass) {
			p.Spec.Lease = &ProviderLeaseBounds{MaxDuration: &metav1.Duration{Duration: 30 * time.Second}}
		},
		"max<min": func(p *ProviderClass) {
			p.Spec.Lease = &ProviderLeaseBounds{MinDuration: &metav1.Duration{Duration: 5 * time.Minute}, MaxDuration: &metav1.Duration{Duration: 4 * time.Minute}}
		},
		"max long": func(p *ProviderClass) {
			p.Spec.Lease = &ProviderLeaseBounds{MaxDuration: &metav1.Duration{Duration: 25 * time.Hour}}
		},
		"driver case": func(p *ProviderClass) { p.Spec.Driver = "Provider-Broker" },
		"no provider": func(p *ProviderClass) { p.Spec.Provider = "" },
	} {
		bad := provider("bad")
		mutate(bad)
		if e = c.Create(ctx, bad); !apierrors.IsInvalid(e) {
			t.Errorf("ProviderClass %s accepted: %v", name, e)
			_ = c.Delete(ctx, bad)
		}
	}
	var currentProvider ProviderClass
	_ = c.Get(ctx, types.NamespacedName{Name: "grok"}, &currentProvider)
	currentProvider.Spec.Provider = "cursor"
	if e = c.Update(ctx, &currentProvider); !apierrors.IsInvalid(e) {
		t.Error("ProviderClass provider change accepted", e)
	}

	// Workspace references: forgeClassName is immutable, providerClassName is stored.
	if e = c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "control"}}); e != nil {
		t.Fatal(e)
	}
	w := &ProjectWorkspace{ObjectMeta: metav1.ObjectMeta{Name: "classes", Namespace: "control"}, Spec: WorkspaceSpec{Repository: "operator/repo", DesiredState: "Paused", ForgeClassName: "forgejo", Providers: []ProviderGrant{{Name: "grok", ProviderClassName: "grok"}}}}
	if e = c.Create(ctx, w); e != nil {
		t.Fatal(e)
	}
	key := types.NamespacedName{Namespace: "control", Name: "classes"}
	if e = c.Get(ctx, key, w); e != nil || w.Spec.ForgeClassName != "forgejo" || w.Spec.Providers[0].ProviderClassName != "grok" {
		t.Fatal("class references pruned", e, w.Spec)
	}
	for _, name := range []string{"other", ""} {
		_ = c.Get(ctx, key, w)
		w.Spec.ForgeClassName = name
		if e = c.Update(ctx, w); !apierrors.IsInvalid(e) {
			t.Errorf("forgeClassName change to %q accepted: %v", name, e)
		}
	}
	plain := &ProjectWorkspace{ObjectMeta: metav1.ObjectMeta{Name: "plain", Namespace: "control"}, Spec: WorkspaceSpec{Repository: "operator/repo", DesiredState: "Paused"}}
	if e = c.Create(ctx, plain); e != nil {
		t.Fatal(e)
	}
	plain.Spec.ForgeClassName = "forgejo"
	if e = c.Update(ctx, plain); !apierrors.IsInvalid(e) {
		t.Error("forgeClassName added to an existing workspace", e)
	}
	_ = c.Get(ctx, types.NamespacedName{Namespace: "control", Name: "plain"}, plain)
	plain.Spec.Providers = []ProviderGrant{{Name: "grok", ProviderClassName: "Bad_Name"}}
	if e = c.Update(ctx, plain); !apierrors.IsInvalid(e) {
		t.Error("invalid providerClassName accepted", e)
	}
}
