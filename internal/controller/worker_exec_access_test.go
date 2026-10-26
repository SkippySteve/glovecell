package controller

import (
	"context"
	"testing"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

const testExecRole = "agent-project-controller-worker-exec"

func workerExecBinding(t *testing.T, r *Reconciler, w *api.ProjectWorkspace) *rbacv1.RoleBinding {
	t.Helper()
	b := &rbacv1.RoleBinding{}
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: namespace(w), Name: workerExecAccessName}, b); err != nil {
		t.Fatal(err)
	}
	return b
}

func assertControllerOnly(t *testing.T, b *rbacv1.RoleBinding, w *api.ProjectWorkspace) {
	t.Helper()
	if b.RoleRef != (rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: testExecRole}) {
		t.Fatalf("roleRef %+v", b.RoleRef)
	}
	if len(b.Subjects) != 1 || b.Subjects[0] != (rbacv1.Subject{Kind: rbacv1.ServiceAccountKind, Name: "controller", Namespace: "control"}) {
		t.Fatalf("subjects %+v", b.Subjects)
	}
	if b.Labels[api.OwnerLabel] != string(w.UID) {
		t.Fatal("binding missing owner label")
	}
}

func TestWorkerExecAccessIsBoundPerProjectNamespace(t *testing.T) {
	ctx := context.Background()
	r, w, _ := fixture(t)
	r.Config.WorkerExecClusterRole = testExecRole
	if err := r.ensure(ctx, w); err != nil {
		t.Fatal(err)
	}
	assertControllerOnly(t, workerExecBinding(t, r, w), w)
	var all rbacv1.RoleBindingList
	if err := r.List(ctx, &all); err != nil {
		t.Fatal(err)
	}
	for _, b := range all.Items {
		if b.Namespace != namespace(w) {
			t.Fatalf("binding %s/%s outside the project namespace", b.Namespace, b.Name)
		}
	}
	var clusterBindings rbacv1.ClusterRoleBindingList
	if err := r.List(ctx, &clusterBindings); err != nil {
		t.Fatal(err)
	}
	if len(clusterBindings.Items) != 0 {
		t.Fatal("controller created a ClusterRoleBinding")
	}
}

func TestWorkerExecAccessUnsetLeavesRBACUnchanged(t *testing.T) {
	ctx := context.Background()
	r, w, _ := fixture(t)
	if err := r.ensure(ctx, w); err != nil {
		t.Fatal(err)
	}
	b := &rbacv1.RoleBinding{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace(w), Name: workerExecAccessName}, b); !apierrors.IsNotFound(err) {
		t.Fatal("binding created without WORKER_EXEC_CLUSTER_ROLE", err)
	}
}

func TestWorkerExecAccessRejectsUnownedBinding(t *testing.T) {
	ctx := context.Background()
	r, w, _ := fixture(t)
	r.Config.WorkerExecClusterRole = testExecRole
	foreign := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: workerExecAccessName, Namespace: namespace(w)},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "cluster-admin"},
		Subjects:   []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: "default", Namespace: namespace(w)}},
	}
	if err := r.Create(ctx, foreign); err != nil {
		t.Fatal(err)
	}
	if err := r.ensureWorkerExecAccess(ctx, w); err == nil {
		t.Fatal("unowned binding was adopted")
	}
	if b := workerExecBinding(t, r, w); b.RoleRef.Name != "cluster-admin" {
		t.Fatal("unowned binding was changed")
	}
}

// A binding widened by hand, or pointing at another role, is reset; and a
// reconcile interrupted between deleting and recreating it recovers.
func TestWorkerExecAccessResetsDriftAndRecovers(t *testing.T) {
	ctx := context.Background()
	r, w, _ := fixture(t)
	r.Config.WorkerExecClusterRole = testExecRole
	if err := r.ensure(ctx, w); err != nil {
		t.Fatal(err)
	}
	b := workerExecBinding(t, r, w)
	b.Subjects = append(b.Subjects, rbacv1.Subject{Kind: rbacv1.ServiceAccountKind, Name: "default", Namespace: namespace(w)})
	if err := r.Update(ctx, b); err != nil {
		t.Fatal(err)
	}
	if err := r.ensureWorkerExecAccess(ctx, w); err != nil {
		t.Fatal(err)
	}
	assertControllerOnly(t, workerExecBinding(t, r, w), w)

	b = workerExecBinding(t, r, w)
	if err := r.Delete(ctx, b); err != nil {
		t.Fatal(err)
	}
	b = &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: workerExecAccessName, Namespace: namespace(w), Labels: map[string]string{api.OwnerLabel: string(w.UID)}},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "edit"},
		Subjects:   []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: "controller", Namespace: "control"}},
	}
	if err := r.Create(ctx, b); err != nil {
		t.Fatal(err)
	}
	if err := r.ensureWorkerExecAccess(ctx, w); err != nil {
		t.Fatal(err)
	}
	assertControllerOnly(t, workerExecBinding(t, r, w), w)

	if err := r.Delete(ctx, workerExecBinding(t, r, w)); err != nil {
		t.Fatal(err)
	}
	if err := r.ensure(ctx, w); err != nil {
		t.Fatal(err)
	}
	assertControllerOnly(t, workerExecBinding(t, r, w), w)
}

func TestWorkerExecAccessComesBeforeRegistrarAccess(t *testing.T) {
	ctx := context.Background()
	r, w, _ := fixture(t)
	r.Config.WorkerExecClusterRole = testExecRole
	r.Config.RegistrarNamespace = "control-t3"
	var order []string
	r.Client = interceptor.NewClient(r.Client.(client.WithWatch), interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
		switch obj.(type) {
		case *rbacv1.Role, *rbacv1.RoleBinding:
			order = append(order, obj.GetName())
		}
		return c.Create(ctx, obj, opts...)
	}})
	if err := r.ensure(ctx, w); err != nil {
		t.Fatal(err)
	}
	if len(order) != 3 || order[0] != workerExecAccessName || order[1] != registrarAccessName {
		t.Fatalf("RBAC created in order %v; the controller's exec binding must come first", order)
	}
}

func TestWorkerExecConfigValidation(t *testing.T) {
	base := Config{Namespace: "control", Owner: "steve", Image: "example.com/worker@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", RuntimeClass: "gvisor", StorageClass: "delete", StorageSize: "1Gi"}
	for _, bad := range []Config{
		func() Config { c := base; c.WorkerExecClusterRole = "Not A Role"; return c }(),
		func() Config { c := base; c.WorkerExecClusterRole = testExecRole; c.ControllerServiceAccount = "bad/account"; return c }(),
	} {
		if err := bad.Validate(); err == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
	good := base
	good.WorkerExecClusterRole = testExecRole
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
}
