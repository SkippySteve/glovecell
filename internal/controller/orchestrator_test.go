package controller

import (
	"context"
	"strings"
	"testing"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/orchestrator"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/registration"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
)

// adapterFixture is an accessFixture registered under a controllerName.
type adapterFixture struct {
	accessFixture
	name string
}

func (a *adapterFixture) ControllerName() string   { return a.name }
func (a *adapterFixture) RegistrationKind() string { return "" }
func (a *adapterFixture) ValidateClass(c *api.OrchestratorClass) error {
	return orchestrator.NoParameters(c)
}

func classFixture(t *testing.T, r *Reconciler, name, controllerName string) *api.OrchestratorClass {
	t.Helper()
	c := &api.OrchestratorClass{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: api.OrchestratorClassSpec{ControllerName: controllerName}}
	if e := r.Create(context.Background(), c); e != nil {
		t.Fatal(e)
	}
	return c
}

func withClass(t *testing.T, r *Reconciler, w *api.ProjectWorkspace, class string) {
	t.Helper()
	w.Spec.OrchestratorClassName = class
	if e := r.Update(context.Background(), w); e != nil {
		t.Fatal(e)
	}
}

func markSandboxReady(t *testing.T, r *Reconciler, w *api.ProjectWorkspace) *unstructured.Unstructured {
	t.Helper()
	ctx := context.Background()
	sandbox := &unstructured.Unstructured{}
	sandbox.SetGroupVersionKind(sandboxGVK)
	if e := r.Get(ctx, types.NamespacedName{Namespace: namespace(w), Name: "project"}, sandbox); e != nil {
		t.Fatal(e)
	}
	_ = unstructured.SetNestedSlice(sandbox.Object, []any{map[string]any{"type": "Ready", "status": "True"}}, "status", "conditions")
	if e := r.Update(ctx, sandbox); e != nil {
		t.Fatal(e)
	}
	return sandbox
}

func reconcileOrchestrator(t *testing.T, r *Reconciler, w *api.ProjectWorkspace) *api.ProjectWorkspace {
	t.Helper()
	ctx := context.Background()
	request := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: w.Namespace, Name: w.Name}}
	if _, e := r.Reconcile(ctx, request); e != nil {
		t.Fatal(e)
	}
	current := &api.ProjectWorkspace{}
	if e := r.Get(ctx, request.NamespacedName, current); e != nil {
		t.Fatal(e)
	}
	return current
}

func readyCondition(w *api.ProjectWorkspace) metav1.Condition {
	if c := meta.FindStatusCondition(w.Status.Conditions, "Ready"); c != nil {
		return *c
	}
	return metav1.Condition{}
}

// A workspace naming a class is served by that class's adapter, not by the
// deployment default.
func TestOrchestratorClassSelectsAdapter(t *testing.T) {
	r, w, _ := fixture(t)
	legacy := &accessFixture{}
	orca := &adapterFixture{name: api.OrchestratorOrca}
	r.Access = legacy
	var e error
	if r.Orchestrators, e = orchestrator.NewRegistry(orca); e != nil {
		t.Fatal(e)
	}
	classFixture(t, r, "orca", api.OrchestratorOrca)
	withClass(t, r, w, "orca")
	reconcileOrchestrator(t, r, w)
	sandbox := markSandboxReady(t, r, w)
	current := reconcileOrchestrator(t, r, w)
	if current.Status.Phase != "Ready" || orca.ensure != 1 || legacy.ensure != 0 {
		t.Fatalf("class adapter not used: phase %s, class ensure %d, default ensure %d", current.Status.Phase, orca.ensure, legacy.ensure)
	}
	if c := readyCondition(current); c.Message != "Project sandbox and orchestrator are ready" {
		t.Fatal("ready message", c.Message)
	}
	_ = r.Get(context.Background(), types.NamespacedName{Namespace: namespace(w), Name: "project"}, sandbox)
	if hostname, _, _ := unstructured.NestedString(sandbox.Object, "spec", "podTemplate", "spec", "hostname"); hostname != w.Name {
		t.Fatal("orchestrated worker does not carry the project hostname", hostname)
	}
	orca.fail = true
	current = reconcileOrchestrator(t, r, w)
	if c := readyCondition(current); current.Status.Phase == "Ready" || c.Reason != "OrchestratorPending" {
		t.Fatal("adapter failure did not hold the workspace out of Ready", current.Status.Phase, c.Reason)
	}
}

// An unknown, missing or unsupported class grants nothing and says why.
func TestUnknownOrchestratorClassFailsClosed(t *testing.T) {
	ctx := context.Background()
	r, w, _ := fixture(t)
	legacy := &accessFixture{}
	orca := &adapterFixture{name: api.OrchestratorOrca}
	r.Access = legacy
	r.Orchestrators, _ = orchestrator.NewRegistry(orca)
	withClass(t, r, w, "chosen")

	current := reconcileOrchestrator(t, r, w)
	c := readyCondition(current)
	if current.Status.Phase != "Blocked" || c.Reason != orchestrator.ReasonClassNotFound || !strings.Contains(c.Message, `"chosen" not found`) {
		t.Fatal("missing class not refused", current.Status.Phase, c.Reason, c.Message)
	}
	if orca.ensure != 0 || legacy.ensure != 0 || orca.suspend == 0 || legacy.suspend == 0 {
		t.Fatal("refused class granted access or left adapters running", orca.ensure, legacy.ensure, orca.suspend, legacy.suspend)
	}

	unsupported := classFixture(t, r, "chosen", "projects.agentwork.dev/herdr")
	current = reconcileOrchestrator(t, r, w)
	if c = readyCondition(current); current.Status.Phase != "Blocked" || c.Reason != orchestrator.ReasonUnsupportedController {
		t.Fatal("class without an installed adapter not refused", current.Status.Phase, c.Reason)
	}
	_ = r.Delete(ctx, unsupported)

	withParams := &api.OrchestratorClass{ObjectMeta: metav1.ObjectMeta{Name: "chosen"}, Spec: api.OrchestratorClassSpec{ControllerName: api.OrchestratorOrca, ParametersRef: &api.OrchestratorParametersReference{Kind: "ConfigMap", Name: "x"}}}
	if e := r.Create(ctx, withParams); e != nil {
		t.Fatal(e)
	}
	current = reconcileOrchestrator(t, r, w)
	if c = readyCondition(current); current.Status.Phase != "Blocked" || c.Reason != orchestrator.ReasonInvalidParameters {
		t.Fatal("unsupported parameters not refused", current.Status.Phase, c.Reason)
	}
	if orca.ensure != 0 || legacy.ensure != 0 {
		t.Fatal("refused class granted access")
	}
	_ = r.Delete(ctx, withParams)

	// Recovery: once the class is valid the workspace proceeds.
	classFixture(t, r, "chosen", api.OrchestratorOrca)
	reconcileOrchestrator(t, r, w)
	markSandboxReady(t, r, w)
	current = reconcileOrchestrator(t, r, w)
	if current.Status.Phase != "Ready" || orca.ensure != 1 || legacy.ensure != 0 {
		t.Fatal("valid class did not recover", current.Status.Phase, orca.ensure, legacy.ensure)
	}
}

// A class adapter's revocation failure keeps storage; the deployment default
// is revoked too. Removal completes once revocation succeeds.
func TestOrchestratorClassRemovalWaitsForRevocation(t *testing.T) {
	ctx := context.Background()
	r, w, _ := fixture(t)
	legacy := &accessFixture{}
	orca := &adapterFixture{name: api.OrchestratorOrca}
	orca.fail = true
	r.Access = legacy
	r.Orchestrators, _ = orchestrator.NewRegistry(orca)
	classFixture(t, r, "orca", api.OrchestratorOrca)
	withClass(t, r, w, "orca")
	if e := r.ensure(ctx, w); e != nil {
		t.Fatal(e)
	}
	w.Spec.DesiredState = "Removed"
	if e := r.remove(ctx, w); e == nil {
		t.Fatal("adapter revocation failure reported removal")
	}
	ns := &corev1.Namespace{}
	if e := r.Get(ctx, types.NamespacedName{Name: namespace(w)}, ns); e != nil || !ns.DeletionTimestamp.IsZero() {
		t.Fatal("storage deleted before orchestrator revocation", e)
	}
	if orca.remove == 0 || legacy.remove == 0 {
		t.Fatal("class adapter and deployment default not both revoked", orca.remove, legacy.remove)
	}
	orca.fail = false
	_ = r.remove(ctx, w)
	if e := r.remove(ctx, w); e != nil {
		t.Fatal("removal did not recover", e)
	}
}

// The real Orca adapter: removal waits until the registrar ledger shows the
// Orca registration revoked, as clientsPending always has.
func TestOrcaClassRemovalWaitsForRegistrarRevocation(t *testing.T) {
	ctx := context.Background()
	r, w, _ := fixture(t)
	r.Orchestrators, _ = orchestrator.NewRegistry(orchestrator.NewOrca(r.Client, r.Config.Namespace))
	classFixture(t, r, "orca", api.OrchestratorOrca)
	withClass(t, r, w, "orca")
	if e := r.ensure(ctx, w); e != nil {
		t.Fatal(e)
	}
	uid := string(w.UID)
	ledger := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: r.Config.Namespace, Name: registration.ConfigMapName(uid), Labels: map[string]string{api.OwnerLabel: uid}}, Data: map[string]string{"purpose": "client-registration", "orca.state": registration.StateRegistered, "orca.id": "11111111-2222-3333-4444-555555555555"}}
	if e := r.Create(ctx, ledger); e != nil {
		t.Fatal(e)
	}
	w.Spec.DesiredState = "Removed"
	for i := 0; i < 2; i++ {
		if e := r.remove(ctx, w); e == nil || !strings.Contains(e.Error(), "orca") {
			t.Fatal("removal ignored an active Orca registration", e)
		}
	}
	ns := &corev1.Namespace{}
	if e := r.Get(ctx, types.NamespacedName{Name: namespace(w)}, ns); e != nil || !ns.DeletionTimestamp.IsZero() {
		t.Fatal("storage deleted before Orca revocation", e)
	}
	ledger.Data["orca.state"] = registration.StateRevoked
	if e := r.Update(ctx, ledger); e != nil {
		t.Fatal(e)
	}
	_ = r.remove(ctx, w)
	if e := r.remove(ctx, w); e != nil {
		t.Fatal("removal after Orca revocation", e)
	}
}

// A class that was bound and then deleted blocks removal until it is
// restored; a class that never granted anything does not.
func TestUnresolvableClassRemoval(t *testing.T) {
	ctx := context.Background()
	t.Run("never bound", func(t *testing.T) {
		r, w, _ := fixture(t)
		r.Orchestrators, _ = orchestrator.NewRegistry()
		withClass(t, r, w, "typo")
		if e := r.ensure(ctx, w); e != nil {
			t.Fatal(e)
		}
		w.Spec.DesiredState = "Removed"
		_ = r.remove(ctx, w)
		if e := r.remove(ctx, w); e != nil {
			t.Fatal("a class that never granted access blocked removal", e)
		}
	})
	t.Run("bound then deleted", func(t *testing.T) {
		r, w, _ := fixture(t)
		orca := &adapterFixture{name: api.OrchestratorOrca}
		r.Orchestrators, _ = orchestrator.NewRegistry(orca)
		class := classFixture(t, r, "orca", api.OrchestratorOrca)
		withClass(t, r, w, "orca")
		reconcileOrchestrator(t, r, w)
		markSandboxReady(t, r, w)
		if current := reconcileOrchestrator(t, r, w); current.Status.Phase != "Ready" {
			t.Fatal("setup", current.Status.Phase)
		}
		if e := r.Delete(ctx, class); e != nil {
			t.Fatal(e)
		}
		before := orca.suspend
		if current := reconcileOrchestrator(t, r, w); current.Status.Phase != "Blocked" || orca.suspend <= before {
			t.Fatal("deleted class left the workspace running or its adapter unsuspended", current.Status.Phase)
		}
		if e := r.Get(ctx, types.NamespacedName{Namespace: w.Namespace, Name: w.Name}, w); e != nil {
			t.Fatal(e)
		}
		w.Spec.DesiredState = "Removed"
		for i := 0; i < 2; i++ {
			if e := r.remove(ctx, w); e == nil || !strings.Contains(e.Error(), "not found") {
				t.Fatal("removal skipped revocation for a deleted class", e)
			}
		}
		classFixture(t, r, "orca", api.OrchestratorOrca)
		_ = r.remove(ctx, w)
		if e := r.remove(ctx, w); e != nil || orca.remove == 0 {
			t.Fatal("removal did not recover after the class was restored", e, orca.remove)
		}
	})
}

// With T3 Connect disabled, a class-based workspace still cannot skip T3
// history left from before the class was set.
func TestClassBasedRemovalKeepsT3HistoryCheck(t *testing.T) {
	ctx := context.Background()
	r, w, _ := fixture(t)
	r.Orchestrators, _ = orchestrator.NewRegistry(orchestrator.NewOrca(r.Client, r.Config.Namespace))
	classFixture(t, r, "orca", api.OrchestratorOrca)
	withClass(t, r, w, "orca")
	if e := r.ensure(ctx, w); e != nil {
		t.Fatal(e)
	}
	marker := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: r.Config.Namespace, Name: "t3-connect-" + strings.ReplaceAll(string(w.UID), "-", "") + "-intent"}, Data: map[string]string{"action": "t3-link-requested"}}
	if e := r.Create(ctx, marker); e != nil {
		t.Fatal(e)
	}
	w.Spec.DesiredState = "Removed"
	if e := r.remove(ctx, w); e == nil || !strings.Contains(e.Error(), "T3") {
		t.Fatal("class-based removal bypassed T3 revocation", e)
	}
}
