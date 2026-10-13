package controller

import (
	"context"
	"errors"
	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"strings"
	"testing"
)

type accessFixture struct {
	fail                    bool
	ensure, suspend, remove int
}

func (a *accessFixture) Ensure(context.Context, *api.ProjectWorkspace) error {
	a.ensure++
	if a.fail {
		return errors.New("T3 registration pending")
	}
	return nil
}
func (a *accessFixture) Suspend(context.Context, *api.ProjectWorkspace) error {
	a.suspend++
	return nil
}
func (a *accessFixture) Remove(context.Context, *api.ProjectWorkspace) error {
	a.remove++
	if a.fail {
		return errors.New("T3 revocation pending")
	}
	return nil
}
func TestApplicationRegistrationGatesReadyAndRecovers(t *testing.T) {
	ctx := context.Background()
	r, w, _ := fixture(t)
	access := &accessFixture{fail: true}
	r.Access = access
	if e := r.ensure(ctx, w); e != nil {
		t.Fatal(e)
	}
	sandbox := &unstructured.Unstructured{}
	sandbox.SetGroupVersionKind(sandboxGVK)
	if e := r.Get(ctx, types.NamespacedName{Namespace: namespace(w), Name: "project"}, sandbox); e != nil {
		t.Fatal(e)
	}
	_ = unstructured.SetNestedSlice(sandbox.Object, []any{map[string]any{"type": "Ready", "status": "True"}}, "status", "conditions")
	if e := r.Update(ctx, sandbox); e != nil {
		t.Fatal(e)
	}
	request := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: w.Namespace, Name: w.Name}}
	if _, e := r.Reconcile(ctx, request); e != nil {
		t.Fatal(e)
	}
	current := &api.ProjectWorkspace{}
	_ = r.Get(ctx, request.NamespacedName, current)
	if current.Status.Phase == "Ready" || access.ensure != 1 {
		t.Fatal("project reported ready without application registration")
	}
	access.fail = false
	if _, e := r.Reconcile(ctx, request); e != nil {
		t.Fatal(e)
	}
	_ = r.Get(ctx, request.NamespacedName, current)
	if current.Status.Phase != "Ready" {
		t.Fatal("registration did not recover", current.Status.Phase)
	}
	hostname, _, _ := unstructured.NestedString(sandbox.Object, "spec", "podTemplate", "spec", "hostname")
	if hostname != w.Name {
		t.Fatal("environment does not identify the project")
	}
}
func TestApplicationRevocationFailurePreservesStorageAndRetries(t *testing.T) {
	ctx := context.Background()
	r, w, host := fixture(t)
	access := &accessFixture{fail: true}
	r.Access = access
	if e := r.ensure(ctx, w); e != nil {
		t.Fatal(e)
	}
	w.Spec.DesiredState = "Removed"
	if e := r.remove(ctx, w); e == nil {
		t.Fatal("application revocation failure reported removal")
	}
	namespaceObject := &corev1.Namespace{}
	if e := r.Get(ctx, types.NamespacedName{Name: namespace(w)}, namespaceObject); e != nil || !namespaceObject.DeletionTimestamp.IsZero() {
		t.Fatal("storage deleted before remote revocation", e)
	}
	if host.revokeCalls == 0 || access.remove == 0 {
		t.Fatal("independent repository/application revocation not attempted")
	}
	access.fail = false
	_ = r.remove(ctx, w)
	if e := r.remove(ctx, w); e != nil {
		t.Fatal("removal did not recover", e)
	}
}
func TestDisabledRegistrationManagerCannotSkipExternalRevocation(t *testing.T) {
	ctx := context.Background()
	r, w, _ := fixture(t)
	if e := r.ensure(ctx, w); e != nil {
		t.Fatal(e)
	}
	marker := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: r.Config.Namespace, Name: "t3-connect-" + strings.ReplaceAll(string(w.UID), "-", "") + "-intent"}, Data: map[string]string{"action": "t3-link-requested"}}
	if e := r.Create(ctx, marker); e != nil {
		t.Fatal(e)
	}
	w.Spec.DesiredState = "Removed"
	if e := r.remove(ctx, w); e == nil {
		t.Fatal("disabled manager bypassed external revocation")
	}
	namespaceObject := &corev1.Namespace{}
	if e := r.Get(ctx, types.NamespacedName{Name: namespace(w)}, namespaceObject); e != nil {
		t.Fatal("storage deleted without revocation", e)
	}
}

func TestManagedHostnameMigrationPreservesProjectIdentityAndVolume(t *testing.T) {
	ctx := context.Background()
	r, w, _ := fixture(t)
	if e := r.ensure(ctx, w); e != nil {
		t.Fatal(e)
	}
	before := &corev1.Secret{}
	if e := r.Get(ctx, types.NamespacedName{Namespace: r.Config.Namespace, Name: identity(w)}, before); e != nil {
		t.Fatal(e)
	}
	r.Access = &accessFixture{}
	if e := r.ensure(ctx, w); e == nil {
		t.Fatal("template replacement did not require observation")
	}
	if e := r.ensure(ctx, w); e != nil {
		t.Fatal("managed template migration did not recover", e)
	}
	after := &corev1.Secret{}
	_ = r.Get(ctx, types.NamespacedName{Namespace: r.Config.Namespace, Name: identity(w)}, after)
	if string(before.Data["username"]) != string(after.Data["username"]) || string(before.Data["password"]) != string(after.Data["password"]) {
		t.Fatal("migration changed project Git identity")
	}
	volume := &corev1.PersistentVolumeClaim{}
	if e := r.Get(ctx, types.NamespacedName{Namespace: namespace(w), Name: "workspace"}, volume); e != nil {
		t.Fatal("migration removed project storage", e)
	}
	sandbox := &unstructured.Unstructured{}
	sandbox.SetGroupVersionKind(sandboxGVK)
	_ = r.Get(ctx, types.NamespacedName{Namespace: namespace(w), Name: "project"}, sandbox)
	old := sandbox.DeepCopy()
	_ = unstructured.SetNestedField(sandbox.Object, "another-project", "spec", "podTemplate", "spec", "hostname")
	if e := r.Patch(ctx, sandbox, client.MergeFrom(old)); e != nil {
		t.Fatal(e)
	}
	if e := r.ensure(ctx, w); e == nil {
		t.Fatal("foreign hostname adopted during migration")
	}
}
