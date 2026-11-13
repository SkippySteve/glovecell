package v1alpha1

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

// The console and the controller edit a project by reading it into the Go
// types and writing it back. RepositoryProject.Public is omitempty, so that
// round trip drops an explicit "public: false". The CRD rules compare whole
// repository entries, so without a schema default the write is refused as a
// repository change and every edit of that project fails while it runs.
func TestTypedRoundTripKeepsRepositoryEntries(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("set KUBEBUILDER_ASSETS to run a local Kubernetes API server")
	}
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
	s := runtime.NewScheme()
	_ = AddToScheme(s)
	_ = corev1.AddToScheme(s)
	c, err := client.New(cfg, client.Options{Scheme: s})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err = c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "control"}}); err != nil {
		t.Fatal(err)
	}
	gvk := schema.GroupVersionKind{Group: "projects.agentwork.dev", Version: "v1alpha1", Kind: "ProjectWorkspace"}
	for name, entry := range map[string]map[string]any{
		// What kubectl, GitOps or another client may store.
		"explicit-false": {"name": "extra", "host": "forgejo", "repository": "operator/extra", "public": false},
		// What the Go types store.
		"omitted": {"name": "extra", "host": "forgejo", "repository": "operator/extra"},
	} {
		t.Run(name, func(t *testing.T) {
			u := &unstructured.Unstructured{Object: map[string]any{
				"metadata": map[string]any{"name": name, "namespace": "control"},
				"spec": map[string]any{
					"repository":   "operator/repo",
					"desiredState": "Running",
					"repositories": []any{entry},
				},
			}}
			u.SetGroupVersionKind(gvk)
			if err := c.Create(ctx, u); err != nil {
				t.Fatal("create", err)
			}
			w := &ProjectWorkspace{}
			if err := c.Get(ctx, types.NamespacedName{Namespace: "control", Name: name}, w); err != nil {
				t.Fatal(err)
			}
			w.Spec.Providers = []ProviderGrant{{Name: "codex", AddedBy: "operator"}}
			if err := c.Update(ctx, w); err != nil {
				t.Fatal("provider opt-in on a running project with an enrolled repository refused:", err)
			}
			if len(w.Spec.Repositories) != 1 || w.Spec.Repositories[0].Public {
				t.Fatalf("repository entry changed: %+v", w.Spec.Repositories)
			}
		})
	}
}
