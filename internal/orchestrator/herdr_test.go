package orchestrator

import (
	"context"
	"strings"
	"testing"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestHerdrEnsurePublishesViaRecipe(t *testing.T) {
	t.Parallel()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	a := NewHerdr(c, "control")
	w := &api.ProjectWorkspace{ObjectMeta: metav1.ObjectMeta{Name: "scrapecast", UID: types.UID("uid-1")}}
	if err := a.Ensure(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	cm := &corev1.ConfigMap{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "control", Name: "herdr-machine-uid-1"}, cm); err != nil {
		t.Fatal(err)
	}
	if cm.Data["via"] != "agent-project-controller herdr-via scrapecast" {
		t.Fatalf("via=%q", cm.Data["via"])
	}
	if !strings.Contains(cm.Data["note"], "hc machine add") {
		t.Fatalf("note=%q", cm.Data["note"])
	}
	if err := a.Remove(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "control", Name: "herdr-machine-uid-1"}, cm); err == nil {
		t.Fatal("expected ConfigMap deleted")
	}
}

func TestHerdrRejectsParameters(t *testing.T) {
	t.Parallel()
	a := NewHerdr(nil, "control")
	class := &api.OrchestratorClass{Spec: api.OrchestratorClassSpec{ControllerName: api.OrchestratorHerdr, ParametersRef: &api.OrchestratorParametersReference{Kind: "ConfigMap", Name: "x"}}}
	if err := a.ValidateClass(class); err == nil {
		t.Fatal("expected parameters rejected")
	}
}
