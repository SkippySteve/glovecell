package controller

import (
	"context"
	"testing"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

const testHubLogsRole = "agent-project-controller-hub-logs"

func hubLogsFixture(t *testing.T) (*Reconciler, *api.ProjectWorkspace) {
	t.Helper()
	r, w, _ := fixture(t)
	if err := r.ensure(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	r.Config.OrcaHubSpoke = true
	r.Config.OrcaHubNamespace = "hub"
	r.Config.HubLogsClusterRole = testHubLogsRole
	return r, w
}

func hubLogsBinding(t *testing.T, r *Reconciler, w *api.ProjectWorkspace) *rbacv1.RoleBinding {
	t.Helper()
	b := &rbacv1.RoleBinding{}
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: namespace(w), Name: hubLogsAccessName}, b); err != nil {
		t.Fatal(err)
	}
	return b
}

func assertHubOnly(t *testing.T, b *rbacv1.RoleBinding, w *api.ProjectWorkspace, account string) {
	t.Helper()
	if b.RoleRef != (rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: testHubLogsRole}) {
		t.Fatalf("roleRef %+v", b.RoleRef)
	}
	if len(b.Subjects) != 1 || b.Subjects[0] != (rbacv1.Subject{Kind: rbacv1.ServiceAccountKind, Name: account, Namespace: "hub"}) {
		t.Fatalf("subjects %+v", b.Subjects)
	}
	if b.Labels[api.OwnerLabel] != string(w.UID) {
		t.Fatal("binding missing owner label")
	}
}

func TestHubLogsAccessIsBoundPerProjectNamespace(t *testing.T) {
	ctx := context.Background()
	r, w := hubLogsFixture(t)
	if err := r.ensureHubLogsAccess(ctx, w); err != nil {
		t.Fatal(err)
	}
	assertHubOnly(t, hubLogsBinding(t, r, w), w, DefaultHubServiceAccount)
	var clusterBindings rbacv1.ClusterRoleBindingList
	if err := r.List(ctx, &clusterBindings); err != nil {
		t.Fatal(err)
	}
	if len(clusterBindings.Items) != 0 {
		t.Fatal("controller created a ClusterRoleBinding")
	}
	r.Config.HubServiceAccount = "other-hub"
	if err := r.ensureHubLogsAccess(ctx, w); err != nil {
		t.Fatal(err)
	}
	assertHubOnly(t, hubLogsBinding(t, r, w), w, "other-hub")
}

// Unset role, or hubspoke off: no binding (installations that still grant
// the hub cluster-wide read keep working unchanged).
func TestHubLogsAccessOffLeavesRBACUnchanged(t *testing.T) {
	ctx := context.Background()
	for name, mutate := range map[string]func(*Reconciler){
		"unset":        func(r *Reconciler) { r.Config.HubLogsClusterRole = "" },
		"hubspoke off": func(r *Reconciler) { r.Config.OrcaHubSpoke = false },
	} {
		r, w := hubLogsFixture(t)
		mutate(r)
		if err := r.ensureHubLogsAccess(ctx, w); err != nil {
			t.Fatal(name, err)
		}
		b := &rbacv1.RoleBinding{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: namespace(w), Name: hubLogsAccessName}, b); !apierrors.IsNotFound(err) {
			t.Fatal(name, "binding created", err)
		}
	}
}

func TestHubLogsAccessRejectsUnownedBinding(t *testing.T) {
	ctx := context.Background()
	r, w := hubLogsFixture(t)
	foreign := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: hubLogsAccessName, Namespace: namespace(w)},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "cluster-admin"},
		Subjects:   []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: "default", Namespace: namespace(w)}},
	}
	if err := r.Create(ctx, foreign); err != nil {
		t.Fatal(err)
	}
	if err := r.ensureHubLogsAccess(ctx, w); err == nil {
		t.Fatal("unowned binding was adopted")
	}
	if b := hubLogsBinding(t, r, w); b.RoleRef.Name != "cluster-admin" {
		t.Fatal("unowned binding was changed")
	}
}

// A widened binding is reset, and one pointing at another role is replaced.
func TestHubLogsAccessResetsDrift(t *testing.T) {
	ctx := context.Background()
	r, w := hubLogsFixture(t)
	if err := r.ensureHubLogsAccess(ctx, w); err != nil {
		t.Fatal(err)
	}
	b := hubLogsBinding(t, r, w)
	b.Subjects = append(b.Subjects, rbacv1.Subject{Kind: rbacv1.ServiceAccountKind, Name: "default", Namespace: namespace(w)})
	if err := r.Update(ctx, b); err != nil {
		t.Fatal(err)
	}
	if err := r.ensureHubLogsAccess(ctx, w); err != nil {
		t.Fatal(err)
	}
	assertHubOnly(t, hubLogsBinding(t, r, w), w, DefaultHubServiceAccount)
	if err := r.Delete(ctx, hubLogsBinding(t, r, w)); err != nil {
		t.Fatal(err)
	}
	if err := r.Create(ctx, &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: hubLogsAccessName, Namespace: namespace(w), Labels: map[string]string{api.OwnerLabel: string(w.UID)}},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "edit"},
		Subjects:   []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: DefaultHubServiceAccount, Namespace: "hub"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.ensureHubLogsAccess(ctx, w); err != nil {
		t.Fatal(err)
	}
	assertHubOnly(t, hubLogsBinding(t, r, w), w, DefaultHubServiceAccount)
}

func TestHubLogsConfigValidation(t *testing.T) {
	if err := validateHubLogs("", "", false); err != nil {
		t.Fatal(err)
	}
	if err := validateHubLogs(testHubLogsRole, "", false); err == nil {
		t.Fatal("hub logs role accepted without hubspoke")
	}
	if err := validateHubLogs("Bad_Role", "", true); err == nil {
		t.Fatal("invalid role accepted")
	}
	if err := validateHubLogs(testHubLogsRole, "bad account", true); err == nil {
		t.Fatal("invalid account accepted")
	}
	if err := validateHubLogs(testHubLogsRole, "orca-hub", true); err != nil {
		t.Fatal(err)
	}
}
