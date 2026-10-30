package v1alpha1

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

func TestSchemeKnowsOrchestratorClass(t *testing.T) {
	s := runtime.NewScheme()
	if err := AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"ProjectWorkspace", "OrchestratorClass", "OrchestratorClassList"} {
		if !s.Recognizes(GroupVersion.WithKind(kind)) {
			t.Fatalf("scheme does not know %s", kind)
		}
	}
	c := &OrchestratorClass{ObjectMeta: metav1.ObjectMeta{Name: "t3"}, Spec: OrchestratorClassSpec{ControllerName: OrchestratorT3, Parameters: &apiextensionsv1.JSON{Raw: []byte(`{"a":1}`)}}}
	copied := c.DeepCopyObject().(*OrchestratorClass)
	copied.Spec.Parameters.Raw[2] = 'b'
	if string(c.Spec.Parameters.Raw) != `{"a":1}` {
		t.Fatal("deep copy shares parameters")
	}
}

func startEnvtest(t *testing.T) client.Client {
	t.Helper()
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("set KUBEBUILDER_ASSETS to run a local Kubernetes API server")
	}
	env := &envtest.Environment{CRDDirectoryPaths: []string{filepath.Join("..", "..", "config", "crd")}, ErrorIfCRDPathMissing: true}
	cfg, e := env.Start()
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if e := env.Stop(); e != nil {
			t.Error(e)
		}
	})
	s := runtime.NewScheme()
	_ = AddToScheme(s)
	_ = corev1.AddToScheme(s)
	c, e := client.New(cfg, client.Options{Scheme: s})
	if e != nil {
		t.Fatal(e)
	}
	return c
}

// spec.orchestratorClassName may be set once (at creation, or later on a
// workspace that had none) and then never changed or cleared.
func TestCRDOrchestratorClassNameImmutable(t *testing.T) {
	c := startEnvtest(t)
	ctx := context.Background()
	if e := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "control"}}); e != nil {
		t.Fatal(e)
	}
	w := &ProjectWorkspace{ObjectMeta: metav1.ObjectMeta{Name: "orca-project", Namespace: "control"}, Spec: WorkspaceSpec{Repository: "operator/repo", DesiredState: "Running", OrchestratorClassName: "orca"}}
	if e := c.Create(ctx, w); e != nil {
		t.Fatal("create with class", e)
	}
	key := client.ObjectKeyFromObject(w)
	w.Spec.OrchestratorClassName = "t3"
	if e := c.Update(ctx, w); !apierrors.IsInvalid(e) {
		t.Fatal("class change accepted", e)
	}
	_ = c.Get(ctx, key, w)
	w.Spec.OrchestratorClassName = ""
	if e := c.Update(ctx, w); !apierrors.IsInvalid(e) {
		t.Fatal("class cleared", e)
	}
	_ = c.Get(ctx, key, w)
	w.Spec.DesiredState = "Paused"
	if e := c.Update(ctx, w); e != nil || w.Spec.OrchestratorClassName != "orca" {
		t.Fatal("unrelated update with the class kept rejected", e)
	}

	plain := &ProjectWorkspace{ObjectMeta: metav1.ObjectMeta{Name: "plain", Namespace: "control"}, Spec: WorkspaceSpec{Repository: "operator/repo", DesiredState: "Running"}}
	if e := c.Create(ctx, plain); e != nil {
		t.Fatal("create without class", e)
	}
	plain.Spec.OrchestratorClassName = "Not_A_Name"
	if e := c.Update(ctx, plain); !apierrors.IsInvalid(e) {
		t.Fatal("invalid class name accepted", e)
	}
	_ = c.Get(ctx, client.ObjectKeyFromObject(plain), plain)
	plain.Spec.OrchestratorClassName = "t3"
	if e := c.Update(ctx, plain); e != nil {
		t.Fatal("setting a class on a workspace without one rejected", e)
	}
	plain.Spec.OrchestratorClassName = "orca"
	if e := c.Update(ctx, plain); !apierrors.IsInvalid(e) {
		t.Fatal("class change after it was set accepted", e)
	}
}

func TestCRDOrchestratorClassValidation(t *testing.T) {
	c := startEnvtest(t)
	ctx := context.Background()
	class := &OrchestratorClass{ObjectMeta: metav1.ObjectMeta{Name: "t3"}, Spec: OrchestratorClassSpec{ControllerName: OrchestratorT3}}
	if e := c.Create(ctx, class); e != nil {
		t.Fatal("create", e)
	}
	class.Spec.ControllerName = OrchestratorOrca
	if e := c.Update(ctx, class); !apierrors.IsInvalid(e) {
		t.Fatal("controllerName change accepted", e)
	}
	_ = c.Get(ctx, client.ObjectKeyFromObject(class), class)
	class.Spec.Parameters = &apiextensionsv1.JSON{Raw: []byte(`{"x":"y"}`)}
	class.Spec.ParametersRef = &OrchestratorParametersReference{Group: "", Kind: "ConfigMap", Name: "x", Namespace: "control"}
	if e := c.Update(ctx, class); !apierrors.IsInvalid(e) {
		t.Fatal("parameters and parametersRef together accepted", e)
	}
	_ = c.Get(ctx, client.ObjectKeyFromObject(class), class)
	class.Spec.Parameters = &apiextensionsv1.JSON{Raw: []byte(`{"nested":{"keep":true}}`)}
	if e := c.Update(ctx, class); e != nil {
		t.Fatal("inline parameters rejected", e)
	}
	_ = c.Get(ctx, client.ObjectKeyFromObject(class), class)
	if class.Spec.Parameters == nil || string(class.Spec.Parameters.Raw) != `{"nested":{"keep":true}}` {
		t.Fatal("parameters pruned", class.Spec.Parameters)
	}
	for _, bad := range []string{"", "t3", "Projects.agentwork.dev/t3", "projects.agentwork.dev/"} {
		o := &OrchestratorClass{ObjectMeta: metav1.ObjectMeta{Name: "bad"}, Spec: OrchestratorClassSpec{ControllerName: bad}}
		if e := c.Create(ctx, o); !apierrors.IsInvalid(e) {
			t.Errorf("controllerName %q accepted: %v", bad, e)
			_ = c.Delete(ctx, o)
		}
	}
	class.Status.Conditions = []metav1.Condition{{Type: OrchestratorClassAccepted, Status: metav1.ConditionTrue, Reason: "Accepted", Message: "ok", LastTransitionTime: metav1.Now()}}
	if e := c.Status().Update(ctx, class); e != nil {
		t.Fatal("status subresource", e)
	}
}
