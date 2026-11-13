package hubspoke

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

func TestAccessEnsureAndRemove(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = api.AddToScheme(scheme)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	uid := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	w := &api.ProjectWorkspace{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "control", UID: types.UID(uid)},
		Spec:       api.WorkspaceSpec{DesiredState: "Running"},
	}
	a := NewOrcaAccess(c, "control", true)
	if err := a.Ensure(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	if err := SetState(context.Background(), c, "control", uid, "orca", LedgerRecord{State: StateEnrolled, ID: "env-1", Name: "demo"}); err != nil {
		t.Fatal(err)
	}
	if err := a.Remove(context.Background(), w); err == nil {
		t.Fatal("Remove while enrolled should wait")
	}
	if err := SetState(context.Background(), c, "control", uid, "orca", LedgerRecord{State: StateRevoked, ID: "env-1", Name: "demo"}); err != nil {
		t.Fatal(err)
	}
	if err := a.Remove(context.Background(), w); err != nil {
		t.Fatal(err)
	}
}

func TestAccessDisabledIsNoop(t *testing.T) {
	a := NewOrcaAccess(nil, "", false)
	w := &api.ProjectWorkspace{Spec: api.WorkspaceSpec{DesiredState: "Running"}}
	if err := a.Ensure(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	if err := a.Remove(context.Background(), w); err != nil {
		t.Fatal(err)
	}
}

func TestConfigMapName(t *testing.T) {
	if ConfigMapName("071ae7d4-9f18-4c40-b58f-17c23a9c2117") != "hubspoke-071ae7d49f184c40b58f17c23a9c2117" {
		t.Fatal(ConfigMapName("071ae7d4-9f18-4c40-b58f-17c23a9c2117"))
	}
}
