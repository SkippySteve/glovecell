package controller

import (
	"strings"
	"testing"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestUsesHerdrCore(t *testing.T) {
	t.Parallel()
	w := &api.ProjectWorkspace{ObjectMeta: metav1.ObjectMeta{Name: "demo"}, Spec: api.WorkspaceSpec{OrchestratorClassName: "herdr"}}
	r := &Reconciler{Config: Config{HerdrCoreEnabled: true}}
	if !r.usesHerdrCore(w) {
		t.Fatal("expected herdr")
	}
	cmd := strings.Join(r.workerCommand(w), " ")
	if !strings.Contains(cmd, "server start") || !strings.Contains(cmd, "hc") {
		t.Fatalf("command=%q", cmd)
	}
	r.Config.HerdrCoreEnabled = false
	if r.usesHerdrCore(w) {
		t.Fatal("disabled")
	}
	sleep := strings.Join(r.workerCommand(w), " ")
	if strings.Contains(sleep, "server start") {
		t.Fatalf("expected sleep worker, got %q", sleep)
	}
}
