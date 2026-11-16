package orchestrator

import (
	"context"
	"testing"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestAgentOrchestratorEnsurePublishesAddress(t *testing.T) {
	t.Parallel()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	a := NewAgentOrchestrator(c, "control", "ao.example")
	if a.ControllerName() != "projects.agentwork.dev/agent-orchestrator" {
		t.Fatal(a.ControllerName())
	}
	w := &api.ProjectWorkspace{ObjectMeta: metav1.ObjectMeta{Name: "scrapecast", UID: types.UID("uid-1")}}
	for i := 0; i < 2; i++ { // idempotent
		if err := a.Ensure(context.Background(), w); err != nil {
			t.Fatal(err)
		}
	}
	cm := &corev1.ConfigMap{}
	key := types.NamespacedName{Namespace: "control", Name: "ao-host-uid-1"}
	if err := c.Get(context.Background(), key, cm); err != nil {
		t.Fatal(err)
	}
	if cm.Data["address"] != "https://scrapecast.ao.example" || cm.Labels[api.OwnerLabel] != "uid-1" {
		t.Fatalf("%+v", cm)
	}
	if err := a.Suspend(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(context.Background(), key, cm); err == nil {
		t.Fatal("expected ConfigMap deleted on suspend")
	}
	if err := a.Remove(context.Background(), w); err != nil {
		t.Fatal("remove of nothing must succeed", err)
	}
}

func TestAgentOrchestratorRefusesForeignConfigMap(t *testing.T) {
	t.Parallel()
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	foreign := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "control", Name: "ao-host-uid-1", Labels: map[string]string{api.OwnerLabel: "someone-else"}}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(foreign).Build()
	w := &api.ProjectWorkspace{ObjectMeta: metav1.ObjectMeta{Name: "scrapecast", UID: types.UID("uid-1")}}
	if err := NewAgentOrchestrator(c, "control", "ao.example").Ensure(context.Background(), w); err == nil {
		t.Fatal("adopted a ConfigMap owned by another workspace")
	}
}

func TestAgentOrchestratorRejectsParameters(t *testing.T) {
	t.Parallel()
	a := NewAgentOrchestrator(nil, "control", "")
	class := &api.OrchestratorClass{Spec: api.OrchestratorClassSpec{ControllerName: api.OrchestratorAgentOrchestrator, ParametersRef: &api.OrchestratorParametersReference{Kind: "ConfigMap", Name: "x"}}}
	if err := a.ValidateClass(class); err == nil {
		t.Fatal("expected parameters rejected")
	}
}
