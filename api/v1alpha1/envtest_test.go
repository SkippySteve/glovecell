package v1alpha1

import (
	"context"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"os"
	"path/filepath"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"testing"
)

func TestCRDValidation(t *testing.T) {
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
	if e = c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "control"}}); e != nil {
		t.Fatal(e)
	}
	w := &ProjectWorkspace{ObjectMeta: metav1.ObjectMeta{Name: "example", Namespace: "control"}, Spec: WorkspaceSpec{Repository: "operator/repo", DesiredState: "Running"}}
	if e = c.Create(ctx, w); e != nil {
		t.Fatal("create", e)
	}
	key := types.NamespacedName{Namespace: w.Namespace, Name: w.Name}
	w.Spec.Repository = "operator/other"
	if e = c.Update(ctx, w); !apierrors.IsInvalid(e) {
		t.Fatal("repository mutation accepted", e)
	}
	_ = c.Get(ctx, key, w)
	w.Spec.HTTPS = []HTTPSRule{{Host: "example.com", Methods: []string{"GET"}, Paths: []string{"/"}}}
	if e = c.Update(ctx, w); !apierrors.IsInvalid(e) {
		t.Fatal("HTTPS expansion accepted", e)
	}
	_ = c.Get(ctx, key, w)
	w.Spec.Repositories = []RepositoryProject{{Name: "extra", Host: "forgejo", Repository: "operator/extra"}}
	if e = c.Update(ctx, w); !apierrors.IsInvalid(e) {
		t.Fatal("repository enrollment while running accepted", e)
	}
	_ = c.Get(ctx, key, w)
	w.Spec.DesiredState = "Paused"
	if e = c.Update(ctx, w); e != nil {
		t.Fatal("pause", e)
	}
	w.Spec.Repositories = []RepositoryProject{{Name: "extra", Host: "forgejo", Repository: "operator/extra"}}
	if e = c.Update(ctx, w); e != nil {
		t.Fatal("paused enrollment rejected", e)
	}
	w.Spec.Repositories = nil
	if e = c.Update(ctx, w); !apierrors.IsInvalid(e) {
		t.Fatal("unverified individual removal accepted", e)
	}
	_ = c.Get(ctx, key, w)
	w.Spec.HTTPS = []HTTPSRule{{Host: "example.com", Methods: []string{"GET"}, Paths: []string{"/"}}}
	if e = c.Update(ctx, w); e != nil {
		t.Fatal("paused HTTPS change rejected", e)
	}
	w.Spec.DesiredState = "Running"
	w.Spec.HTTPS = nil
	if e = c.Update(ctx, w); !apierrors.IsInvalid(e) {
		t.Fatal("policy edit combined with resume accepted", e)
	}
	_ = c.Get(ctx, key, w)
	w.Spec.DesiredState = "Removed"
	if e = c.Update(ctx, w); e != nil {
		t.Fatal(e)
	}
	w.Spec.DesiredState = "Running"
	if e = c.Update(ctx, w); !apierrors.IsInvalid(e) {
		t.Fatal("removed workspace reopened", e)
	}
	_ = c.Get(ctx, key, w)
	w.Status.Phase = "Removed"
	if e = c.Status().Update(ctx, w); e != nil {
		t.Fatal("status subresource", e)
	}
}

// LAN host grants change while the project runs; the schema accepts only a
// canonical IPv4 address and an explicit, unique TCP port list.
func TestCRDLANHostValidation(t *testing.T) {
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
	if e = c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "control"}}); e != nil {
		t.Fatal(e)
	}
	w := &ProjectWorkspace{ObjectMeta: metav1.ObjectMeta{Name: "lan", Namespace: "control"}, Spec: WorkspaceSpec{Repository: "operator/repo", DesiredState: "Running"}}
	if e = c.Create(ctx, w); e != nil {
		t.Fatal("create", e)
	}
	key := types.NamespacedName{Namespace: w.Namespace, Name: w.Name}
	now := metav1.Now()
	w.Spec.LANHosts = []LANHost{{Address: "192.168.110.2", Ports: []int32{22, 8080}, Note: "dev box", AddedBy: "issuer#operator", AddedAt: &now}}
	if e = c.Update(ctx, w); e != nil {
		t.Fatal("grant on a running project rejected", e)
	}
	if e = c.Get(ctx, key, w); e != nil || len(w.Spec.LANHosts) != 1 || w.Spec.LANHosts[0].AddedAt == nil {
		t.Fatal("grant pruned by the schema", e, w.Spec.LANHosts)
	}
	for _, bad := range [][]LANHost{
		{{Address: "192.168.110.0/24", Ports: []int32{22}}},
		{{Address: "fd00::2", Ports: []int32{22}}},
		{{Address: "0192.168.110.2", Ports: []int32{22}}},
		{{Address: "devbox", Ports: []int32{22}}},
		{{Address: "192.168.110.2", Ports: []int32{}}},
		{{Address: "192.168.110.2", Ports: []int32{0}}},
		{{Address: "192.168.110.2", Ports: []int32{65536}}},
		{{Address: "192.168.110.2", Ports: []int32{22, 22}}},
		{{Address: "192.168.110.2", Ports: []int32{22}}, {Address: "192.168.110.2", Ports: []int32{80}}},
	} {
		_ = c.Get(ctx, key, w)
		w.Spec.LANHosts = bad
		if e = c.Update(ctx, w); !apierrors.IsInvalid(e) {
			t.Errorf("accepted %#v: %v", bad, e)
		}
	}
	_ = c.Get(ctx, key, w)
	w.Spec.LANHosts = nil
	if e = c.Update(ctx, w); e != nil {
		t.Fatal("removing grants from a running project rejected", e)
	}
}
