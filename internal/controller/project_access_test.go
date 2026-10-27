package controller

import (
	"context"
	"fmt"
	"strings"
	"testing"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

const testAccessRole = "agent-project-controller-project-access"

// scopedRBAC simulates the installed RBAC once the cluster-wide Secret and
// namespace-delete verbs are gone: Secrets only in the control namespace
// (static Role) or in a namespace holding the controller's access binding,
// and namespace delete only for a namespace holding that binding. Every
// refusal is recorded so a test fails on any out-of-scope access, even one
// the controller would retry.
type scopedRBAC struct {
	control string
	denied  []string
}

func (s *scopedRBAC) bound(ctx context.Context, c client.WithWatch, ns string) bool {
	b := &rbacv1.RoleBinding{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: projectAccessName}, b); err != nil {
		return false
	}
	return b.RoleRef.Kind == "ClusterRole" && b.RoleRef.Name == testAccessRole && len(b.Subjects) == 1 &&
		b.Subjects[0] == rbacv1.Subject{Kind: rbacv1.ServiceAccountKind, Name: "controller", Namespace: s.control}
}

func (s *scopedRBAC) secret(ctx context.Context, c client.WithWatch, verb, ns string) error {
	if ns == s.control || (ns != "" && s.bound(ctx, c, ns)) {
		return nil
	}
	s.denied = append(s.denied, fmt.Sprintf("%s secrets in %q", verb, ns))
	return apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, "", fmt.Errorf("not in scope"))
}

func (s *scopedRBAC) client(c client.WithWatch) client.WithWatch {
	return interceptor.NewClient(c, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*corev1.Secret); ok {
				if err := s.secret(ctx, c, "get", key.Namespace); err != nil {
					return err
				}
			}
			return c.Get(ctx, key, obj, opts...)
		},
		List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if _, ok := list.(*corev1.SecretList); ok {
				o := &client.ListOptions{}
				o.ApplyOptions(opts)
				if err := s.secret(ctx, c, "list", o.Namespace); err != nil {
					return err
				}
			}
			return c.List(ctx, list, opts...)
		},
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if _, ok := obj.(*corev1.Secret); ok {
				if err := s.secret(ctx, c, "create", obj.GetNamespace()); err != nil {
					return err
				}
			}
			return c.Create(ctx, obj, opts...)
		},
		Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			if _, ok := obj.(*corev1.Secret); ok {
				if err := s.secret(ctx, c, "update", obj.GetNamespace()); err != nil {
					return err
				}
			}
			if _, ok := obj.(*corev1.Namespace); ok {
				s.denied = append(s.denied, "update namespace "+obj.GetName())
			}
			return c.Update(ctx, obj, opts...)
		},
		Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
			if _, ok := obj.(*corev1.Secret); ok {
				if err := s.secret(ctx, c, "patch", obj.GetNamespace()); err != nil {
					return err
				}
			}
			if _, ok := obj.(*corev1.Namespace); ok {
				s.denied = append(s.denied, "patch namespace "+obj.GetName())
			}
			return c.Patch(ctx, obj, patch, opts...)
		},
		Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			switch obj.(type) {
			case *corev1.Secret:
				if err := s.secret(ctx, c, "delete", obj.GetNamespace()); err != nil {
					return err
				}
			case *corev1.Namespace:
				// The API server authorizes DELETE /api/v1/namespaces/<n> in namespace <n>.
				if !s.bound(ctx, c, obj.GetName()) {
					s.denied = append(s.denied, "delete namespace "+obj.GetName())
					return apierrors.NewForbidden(schema.GroupResource{Resource: "namespaces"}, obj.GetName(), fmt.Errorf("not in scope"))
				}
			}
			return c.Delete(ctx, obj, opts...)
		},
	})
}

func projectAccessBinding(t *testing.T, r *Reconciler, w *api.ProjectWorkspace) *rbacv1.RoleBinding {
	t.Helper()
	b := &rbacv1.RoleBinding{}
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: namespace(w), Name: projectAccessName}, b); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestProjectAccessIsBoundPerProjectNamespace(t *testing.T) {
	ctx := context.Background()
	r, w, _ := fixture(t)
	r.Config.ProjectAccessClusterRole = testAccessRole
	if err := r.ensure(ctx, w); err != nil {
		t.Fatal(err)
	}
	b := projectAccessBinding(t, r, w)
	if b.RoleRef != (rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: testAccessRole}) {
		t.Fatalf("roleRef %+v", b.RoleRef)
	}
	if len(b.Subjects) != 1 || b.Subjects[0] != (rbacv1.Subject{Kind: rbacv1.ServiceAccountKind, Name: "controller", Namespace: "control"}) {
		t.Fatalf("subjects %+v", b.Subjects)
	}
	if b.Labels[api.OwnerLabel] != string(w.UID) {
		t.Fatal("binding missing owner label")
	}
	var clusterBindings rbacv1.ClusterRoleBindingList
	if err := r.List(ctx, &clusterBindings); err != nil {
		t.Fatal(err)
	}
	if len(clusterBindings.Items) != 0 {
		t.Fatal("controller created a ClusterRoleBinding")
	}
}

func TestProjectAccessUnsetLeavesRBACUnchanged(t *testing.T) {
	ctx := context.Background()
	r, w, _ := fixture(t)
	if err := r.ensure(ctx, w); err != nil {
		t.Fatal(err)
	}
	b := &rbacv1.RoleBinding{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace(w), Name: projectAccessName}, b); !apierrors.IsNotFound(err) {
		t.Fatal("binding created without PROJECT_ACCESS_CLUSTER_ROLE", err)
	}
}

func TestProjectAccessRejectsUnownedBinding(t *testing.T) {
	ctx := context.Background()
	r, w, _ := fixture(t)
	r.Config.ProjectAccessClusterRole = testAccessRole
	foreign := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: projectAccessName, Namespace: namespace(w)},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "cluster-admin"},
		Subjects:   []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: "default", Namespace: namespace(w)}},
	}
	if err := r.Create(ctx, foreign); err != nil {
		t.Fatal(err)
	}
	if err := r.ensureProjectAccess(ctx, w); err == nil || !strings.Contains(err.Error(), "not owned") {
		t.Fatal("unowned binding was adopted", err)
	}
	if b := projectAccessBinding(t, r, w); b.RoleRef.Name != "cluster-admin" || len(b.Subjects) != 1 || b.Subjects[0].Name != "default" {
		t.Fatal("unowned binding was changed")
	}
}

func TestProjectAccessResetsDrift(t *testing.T) {
	ctx := context.Background()
	r, w, _ := fixture(t)
	r.Config.ProjectAccessClusterRole = testAccessRole
	if err := r.ensure(ctx, w); err != nil {
		t.Fatal(err)
	}
	b := projectAccessBinding(t, r, w)
	b.Subjects = append(b.Subjects, rbacv1.Subject{Kind: rbacv1.ServiceAccountKind, Name: "default", Namespace: namespace(w)})
	if err := r.Update(ctx, b); err != nil {
		t.Fatal(err)
	}
	if err := r.ensureProjectAccess(ctx, w); err != nil {
		t.Fatal(err)
	}
	if b = projectAccessBinding(t, r, w); len(b.Subjects) != 1 || b.Subjects[0].Name != "controller" {
		t.Fatalf("widened subjects kept: %+v", b.Subjects)
	}
	if err := r.Delete(ctx, b); err != nil {
		t.Fatal(err)
	}
	if err := r.ensure(ctx, w); err != nil {
		t.Fatal("ensure did not recover a deleted binding:", err)
	}
	projectAccessBinding(t, r, w)
}

// The full lifecycle (provision, reconcile again, remove) under RBAC with no
// cluster-wide Secrets and no cluster-wide namespace delete.
func TestLifecycleNeedsNoClusterWideSecretsOrNamespaceDelete(t *testing.T) {
	ctx := context.Background()
	r, w, _ := fixture(t)
	r.Config.ProjectAccessClusterRole = testAccessRole
	rbac := &scopedRBAC{control: "control"}
	r.Client = rbac.client(r.Client.(client.WithWatch))
	// An operator model grant lives in the project namespace and is read there.
	if err := r.ensure(ctx, w); err != nil {
		t.Fatal(err)
	}
	if err := r.ensure(ctx, w); err != nil {
		t.Fatal("second reconcile:", err)
	}
	w.Spec.DesiredState = "Removed"
	w.Generation = 2
	if err := r.remove(ctx, w); err == nil || !strings.Contains(err.Error(), "waiting for namespace") {
		t.Fatal("remove did not delete the namespace:", err)
	}
	ns := &corev1.Namespace{}
	if err := r.Get(ctx, types.NamespacedName{Name: namespace(w)}, ns); !apierrors.IsNotFound(err) && ns.DeletionTimestamp.IsZero() {
		t.Fatal("project namespace not deleted:", err)
	}
	if len(rbac.denied) > 0 {
		t.Fatalf("out-of-scope access: %v", rbac.denied)
	}
}

// A project provisioned by a controller that still had cluster-wide grants has
// no access binding. Removing it after the grants are gone must bind first.
func TestRemoveBindsProjectAccessBeforeDeletingNamespace(t *testing.T) {
	ctx := context.Background()
	r, w, _ := fixture(t)
	if err := r.ensure(ctx, w); err != nil {
		t.Fatal(err)
	}
	r.Config.ProjectAccessClusterRole = testAccessRole
	rbac := &scopedRBAC{control: "control"}
	r.Client = rbac.client(r.Client.(client.WithWatch))
	w.Spec.DesiredState = "Removed"
	w.Generation = 2
	if err := r.remove(ctx, w); err == nil || !strings.Contains(err.Error(), "waiting for namespace") {
		t.Fatal("remove did not delete the namespace:", err)
	}
	if len(rbac.denied) > 0 {
		t.Fatalf("out-of-scope access: %v", rbac.denied)
	}
}

// Without the binding the simulated RBAC refuses project Secrets, so the
// lifecycle test above really depends on it.
func TestScopedRBACRefusesUnboundProjectSecrets(t *testing.T) {
	ctx := context.Background()
	r, w, _ := fixture(t)
	rbac := &scopedRBAC{control: "control"}
	r.Client = rbac.client(r.Client.(client.WithWatch))
	if err := r.ensure(ctx, w); err == nil {
		t.Fatal("ensure succeeded without project Secret access")
	}
	if len(rbac.denied) == 0 || !strings.Contains(rbac.denied[0], namespace(w)) {
		t.Fatalf("expected a refusal in the project namespace, got %v", rbac.denied)
	}
}

func TestProjectAccessConfigValidation(t *testing.T) {
	base := Config{Namespace: "control", Owner: "steve", Image: "example.com/worker@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", RuntimeClass: "gvisor", StorageClass: "delete", StorageSize: "1Gi"}
	for _, bad := range []Config{
		func() Config { c := base; c.ProjectAccessClusterRole = "Not A Role"; return c }(),
		func() Config {
			c := base
			c.ProjectAccessClusterRole = testAccessRole
			c.ControllerServiceAccount = "bad/account"
			return c
		}(),
	} {
		if err := bad.Validate(); err == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
	good := base
	good.ProjectAccessClusterRole = testAccessRole
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
}
