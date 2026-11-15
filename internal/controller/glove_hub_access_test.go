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

const testGloveHubRole = "agent-project-controller-glove-hub-exec"

func gloveHubFixture(t *testing.T) (*Reconciler, *api.ProjectWorkspace) {
	t.Helper()
	r, w, _ := fixture(t)
	if err := r.ensure(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	r.Config.HerdrCoreEnabled = true
	r.Config.GloveHubExecClusterRole = testGloveHubRole
	r.Config.GloveHubNamespace = "glove-hub"
	r.Config.GloveHubServiceAccount = "glove-hub"
	w.Spec.OrchestratorClassName = "herdr"
	return r, w
}

func gloveHubBinding(t *testing.T, r *Reconciler, w *api.ProjectWorkspace) (*rbacv1.RoleBinding, error) {
	t.Helper()
	b := &rbacv1.RoleBinding{}
	err := r.Get(context.Background(), types.NamespacedName{Namespace: namespace(w), Name: gloveHubAccessName}, b)
	return b, err
}

// A Herdr cell grants the hub exactly one RoleBinding; switching the cell to
// another class withdraws it.
func TestGloveHubAccessFollowsHerdrClass(t *testing.T) {
	ctx := context.Background()
	r, w := gloveHubFixture(t)
	if err := r.ensureGloveHubAccess(ctx, w); err != nil {
		t.Fatal(err)
	}
	b, err := gloveHubBinding(t, r, w)
	if err != nil {
		t.Fatal(err)
	}
	if b.RoleRef != (rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: testGloveHubRole}) {
		t.Fatalf("roleRef %+v", b.RoleRef)
	}
	if len(b.Subjects) != 1 || b.Subjects[0] != (rbacv1.Subject{Kind: rbacv1.ServiceAccountKind, Name: "glove-hub", Namespace: "glove-hub"}) {
		t.Fatalf("subjects %+v", b.Subjects)
	}
	var clusterBindings rbacv1.ClusterRoleBindingList
	if err := r.List(ctx, &clusterBindings); err != nil || len(clusterBindings.Items) != 0 {
		t.Fatalf("cluster-wide binding: %v %v", clusterBindings.Items, err)
	}
	w.Spec.OrchestratorClassName = "orca"
	if err := r.ensureGloveHubAccess(ctx, w); err != nil {
		t.Fatal(err)
	}
	if _, err := gloveHubBinding(t, r, w); !apierrors.IsNotFound(err) {
		t.Fatalf("binding kept after leaving herdr: %v", err)
	}
}

func TestGloveHubAccessOffLeavesRBACUnchanged(t *testing.T) {
	ctx := context.Background()
	for name, mutate := range map[string]func(*Reconciler){
		"unset":     func(r *Reconciler) { r.Config.GloveHubExecClusterRole = "" },
		"herdr off": func(r *Reconciler) { r.Config.HerdrCoreEnabled = false },
	} {
		r, w := gloveHubFixture(t)
		mutate(r)
		if err := r.ensureGloveHubAccess(ctx, w); err != nil {
			t.Fatal(name, err)
		}
		if _, err := gloveHubBinding(t, r, w); !apierrors.IsNotFound(err) {
			t.Fatal(name, "binding created", err)
		}
	}
}

func TestGloveHubAccessNeverDeletesUnownedBinding(t *testing.T) {
	ctx := context.Background()
	r, w := gloveHubFixture(t)
	w.Spec.OrchestratorClassName = "orca"
	if err := r.Create(ctx, &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: gloveHubAccessName, Namespace: namespace(w)},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "view"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.ensureGloveHubAccess(ctx, w); err == nil {
		t.Fatal("unowned binding accepted")
	}
	if _, err := gloveHubBinding(t, r, w); err != nil {
		t.Fatal("unowned binding deleted", err)
	}
}

func TestValidateGloveHub(t *testing.T) {
	if err := validateGloveHub("", "", "", false); err != nil {
		t.Fatal(err)
	}
	if err := validateGloveHub(testGloveHubRole, "glove-hub", "glove-hub", false); err == nil {
		t.Fatal("accepted without herdr-core")
	}
	if err := validateGloveHub(testGloveHubRole, "", "glove-hub", true); err == nil {
		t.Fatal("accepted without namespace")
	}
	if err := validateGloveHub(testGloveHubRole, "glove-hub", "Glove_Hub", true); err == nil {
		t.Fatal("accepted invalid account")
	}
}
