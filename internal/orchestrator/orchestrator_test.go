package orchestrator

import (
	"context"
	"errors"
	"strings"
	"testing"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/registration"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type stub struct{ name string }

func (s stub) ControllerName() string                               { return s.name }
func (s stub) RegistrationKind() string                             { return "" }
func (s stub) ValidateClass(c *api.OrchestratorClass) error         { return NoParameters(c) }
func (s stub) Ensure(context.Context, *api.ProjectWorkspace) error  { return nil }
func (s stub) Suspend(context.Context, *api.ProjectWorkspace) error { return nil }
func (s stub) Remove(context.Context, *api.ProjectWorkspace) error  { return nil }

func scheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = api.AddToScheme(s)
	_ = corev1.AddToScheme(s)
	return s
}

func workspace(class string) *api.ProjectWorkspace {
	return &api.ProjectWorkspace{ObjectMeta: metav1.ObjectMeta{Name: "project", Namespace: "control", UID: types.UID("12345678-1234-1234-1234-123456789012")}, Spec: api.WorkspaceSpec{Repository: "steve/project", DesiredState: "Running", OrchestratorClassName: class}}
}

func TestRegistryRejectsInvalidAndDuplicateNames(t *testing.T) {
	r, err := NewRegistry(stub{name: api.OrchestratorOrca})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Register(stub{name: api.OrchestratorOrca}); err == nil {
		t.Fatal("second adapter replaced the first")
	}
	for _, bad := range []string{"", "orca", "Projects.agentwork.dev/x", "projects.agentwork.dev/", strings.Repeat("a", 250) + ".dev/x"} {
		if err := r.Register(stub{name: bad}); err == nil {
			t.Errorf("registered invalid controllerName %q", bad)
		}
	}
	if err := r.Register(nil); err == nil {
		t.Fatal("nil adapter registered")
	}
	if err := r.Register(stub{name: "example.com/herdr"}); err != nil {
		t.Fatal("third-party controllerName refused", err)
	}
	names := []string{}
	for _, a := range r.Adapters() {
		names = append(names, a.ControllerName())
	}
	if strings.Join(names, ",") != "example.com/herdr,projects.agentwork.dev/orca" {
		t.Fatal("adapters not listed in order", names)
	}
	var none *Registry
	if _, ok := none.Lookup(api.OrchestratorT3); ok || len(none.Adapters()) != 0 {
		t.Fatal("nil registry serves adapters")
	}
}

func TestResolveClasses(t *testing.T) {
	ctx := context.Background()
	orca := stub{name: api.OrchestratorOrca}
	def := stub{name: api.OrchestratorT3}
	reg, _ := NewRegistry(orca)
	c := fake.NewClientBuilder().WithScheme(scheme()).WithObjects(
		&api.OrchestratorClass{ObjectMeta: metav1.ObjectMeta{Name: "orca"}, Spec: api.OrchestratorClassSpec{ControllerName: api.OrchestratorOrca}},
		&api.OrchestratorClass{ObjectMeta: metav1.ObjectMeta{Name: "herdr"}, Spec: api.OrchestratorClassSpec{ControllerName: "projects.agentwork.dev/herdr"}},
		&api.OrchestratorClass{ObjectMeta: metav1.ObjectMeta{Name: "orca-tuned"}, Spec: api.OrchestratorClassSpec{ControllerName: api.OrchestratorOrca, Parameters: &apiextensionsv1.JSON{Raw: []byte(`{"x":1}`)}}},
		&api.OrchestratorClass{ObjectMeta: metav1.ObjectMeta{Name: "orca-both"}, Spec: api.OrchestratorClassSpec{ControllerName: api.OrchestratorOrca, Parameters: &apiextensionsv1.JSON{Raw: []byte(`{}`)}, ParametersRef: &api.OrchestratorParametersReference{Kind: "ConfigMap", Name: "p"}}},
	).Build()

	// Empty class: the deployment default, including none.
	if a, err := Resolve(ctx, c, reg, def, workspace("")); err != nil || a != Access(def) {
		t.Fatal("empty class did not use the default", a, err)
	}
	if a, err := Resolve(ctx, c, reg, nil, workspace("")); err != nil || a != nil {
		t.Fatal("empty class without a default granted an orchestrator", a, err)
	}
	if a, err := Resolve(ctx, c, reg, def, workspace("orca")); err != nil || a != Access(orca) {
		t.Fatal("named class did not select its adapter", a, err)
	}
	for class, reason := range map[string]string{
		"missing":    ReasonClassNotFound,
		"herdr":      ReasonUnsupportedController,
		"orca-tuned": ReasonInvalidParameters,
		"orca-both":  ReasonInvalidParameters,
	} {
		a, err := Resolve(ctx, c, reg, def, workspace(class))
		var ce *ClassError
		if a != nil || !errors.As(err, &ce) || ce.Reason != reason || !strings.Contains(err.Error(), class) {
			t.Errorf("class %q: got %v, %v; want denial %s", class, a, err, reason)
		}
	}
	// A nil registry serves no class, and never falls back to the default.
	if a, err := Resolve(ctx, c, nil, def, workspace("orca")); a != nil || err == nil {
		t.Fatal("class resolved without a registry", a, err)
	}
}

func TestClassReconcilerSetsAccepted(t *testing.T) {
	ctx := context.Background()
	reg, _ := NewRegistry(stub{name: api.OrchestratorOrca})
	classes := []client.Object{
		&api.OrchestratorClass{ObjectMeta: metav1.ObjectMeta{Name: "orca", Generation: 3}, Spec: api.OrchestratorClassSpec{ControllerName: api.OrchestratorOrca}},
		&api.OrchestratorClass{ObjectMeta: metav1.ObjectMeta{Name: "herdr"}, Spec: api.OrchestratorClassSpec{ControllerName: "projects.agentwork.dev/herdr"}},
		&api.OrchestratorClass{ObjectMeta: metav1.ObjectMeta{Name: "foreign"}, Spec: api.OrchestratorClassSpec{ControllerName: "example.com/other"}},
	}
	c := fake.NewClientBuilder().WithScheme(scheme()).WithObjects(classes...).WithStatusSubresource(classes...).Build()
	r := &ClassReconciler{Client: c, Registry: reg}
	for _, name := range []string{"orca", "herdr", "foreign", "gone"} {
		if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: name}}); err != nil {
			t.Fatal(name, err)
		}
	}
	get := func(name string) *api.OrchestratorClass {
		o := &api.OrchestratorClass{}
		if err := c.Get(ctx, types.NamespacedName{Name: name}, o); err != nil {
			t.Fatal(err)
		}
		return o
	}
	if cond := meta.FindStatusCondition(get("orca").Status.Conditions, api.OrchestratorClassAccepted); cond == nil || cond.Status != metav1.ConditionTrue || cond.ObservedGeneration != 3 {
		t.Fatal("installed adapter not accepted", cond)
	}
	if cond := meta.FindStatusCondition(get("herdr").Status.Conditions, api.OrchestratorClassAccepted); cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != ReasonUnsupportedController {
		t.Fatal("unknown controller accepted", cond)
	}
	if len(get("foreign").Status.Conditions) != 0 {
		t.Fatal("wrote status on a class another controller owns")
	}
}

func ledger(uid string, owner string, data map[string]string) *corev1.ConfigMap {
	return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "control", Name: registration.ConfigMapName(uid), Labels: map[string]string{api.OwnerLabel: owner}}, Data: data}
}

// The Orca adapter grants nothing in the cluster today; removal waits until
// the registrar ledger shows the Orca registration revoked.
func TestOrcaAdapterEnsureSuspendRemove(t *testing.T) {
	ctx := context.Background()
	w := workspace("orca")
	uid := string(w.UID)
	c := fake.NewClientBuilder().WithScheme(scheme()).Build()
	a := NewOrca(c, "control")
	if a.ControllerName() != api.OrchestratorOrca || a.RegistrationKind() != "orca" {
		t.Fatal("orca adapter misnamed")
	}
	if err := a.Ensure(ctx, w); err != nil {
		t.Fatal("ensure", err)
	}
	if err := a.Suspend(ctx, w); err != nil {
		t.Fatal("suspend", err)
	}
	if err := a.Remove(ctx, w); err != nil {
		t.Fatal("remove without a ledger (registrar never ran)", err)
	}
	cm := ledger(uid, uid, map[string]string{"purpose": "client-registration", "orca.state": string(registration.StateRegistered), "orca.id": "11111111-2222-3333-4444-555555555555", "t3.state": string(registration.StateRevoked)})
	if err := c.Create(ctx, cm); err != nil {
		t.Fatal(err)
	}
	if err := a.Remove(ctx, w); err == nil || !strings.Contains(err.Error(), "registered") {
		t.Fatal("removal reported done while Orca registration is active", err)
	}
	for _, state := range []string{string(registration.StatePending), "half-done"} {
		cm.Data["orca.state"] = state
		if err := c.Update(ctx, cm); err != nil {
			t.Fatal(err)
		}
		if err := a.Remove(ctx, w); err == nil {
			t.Fatalf("removal with Orca state %q reported done", state)
		}
	}
	cm.Data["orca.state"] = string(registration.StateRevoked)
	if err := c.Update(ctx, cm); err != nil {
		t.Fatal(err)
	}
	if err := a.Remove(ctx, w); err != nil {
		t.Fatal("removal after revocation", err)
	}
	// A ledger owned by another workspace is never trusted.
	cm.Labels[api.OwnerLabel] = "someone-else"
	if err := c.Update(ctx, cm); err != nil {
		t.Fatal(err)
	}
	if err := a.Remove(ctx, w); err == nil {
		t.Fatal("foreign ledger trusted")
	}
	if err := (&Registrar{Name: api.OrchestratorOrca, Kind: "orca"}).Remove(ctx, w); err == nil {
		t.Fatal("removal without a ledger reader reported done")
	}
	if err := a.ValidateClass(&api.OrchestratorClass{Spec: api.OrchestratorClassSpec{ControllerName: api.OrchestratorOrca, ParametersRef: &api.OrchestratorParametersReference{Kind: "ConfigMap", Name: "x"}}}); err == nil {
		t.Fatal("orca accepted parameters it does not understand")
	}
}

func TestCommandAdapterWaitsOnCustomKindsOnly(t *testing.T) {
	ctx := context.Background()
	w := workspace("command")
	uid := string(w.UID)
	cm := ledger(uid, uid, map[string]string{"purpose": "client-registration", "t3.state": string(registration.StateRegistered), "orca.state": string(registration.StateRegistered), "herdr.state": string(registration.StateRevoked)})
	c := fake.NewClientBuilder().WithScheme(scheme()).WithObjects(cm).Build()
	a := NewCommand(c, "control")
	if err := a.Remove(ctx, w); err != nil {
		t.Fatal("command adapter waited on built-in kinds", err)
	}
	cm.Data["herdr.state"] = string(registration.StateRegistered)
	if err := c.Update(ctx, cm); err != nil {
		t.Fatal(err)
	}
	if err := a.Remove(ctx, w); err == nil || !strings.Contains(err.Error(), "herdr") {
		t.Fatal("command adapter ignored an active custom client", err)
	}
}

func TestWording(t *testing.T) {
	if reason, _ := Wording(stub{name: api.OrchestratorT3}); reason != "T3ConnectPending" {
		t.Fatal("T3 wording changed", reason)
	}
	if reason, _ := Wording(nil); reason != "T3ConnectPending" {
		t.Fatal("unnamed default wording changed", reason)
	}
	if reason, msg := Wording(NewOrca(nil, "")); reason != "OrchestratorPending" || strings.Contains(msg, "T3") {
		t.Fatal("orca uses T3 wording", reason, msg)
	}
}
