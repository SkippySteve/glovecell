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

// Only a Herdr cell with a session hub runs session-hub-herdr; it stays off
// in Herdr cells without a hub and in every other class.
func TestHerdrSessionHubPanes(t *testing.T) {
	t.Parallel()
	r := &Reconciler{Config: Config{HerdrCoreEnabled: true}}
	plain := &api.ProjectWorkspace{ObjectMeta: metav1.ObjectMeta{Name: "demo"}, Spec: api.WorkspaceSpec{OrchestratorClassName: "herdr"}}
	hub := &api.ProjectWorkspace{ObjectMeta: metav1.ObjectMeta{Name: "demo"}, Spec: api.WorkspaceSpec{OrchestratorClassName: "herdr", SessionHub: &api.SessionHubSpec{Enabled: true}}}
	if cmd := strings.Join(r.workerCommand(hub), " "); !strings.Contains(cmd, `"/usr/local/bin/agent-project-controller" session-hub-herdr &`) || strings.Index(cmd, "server start") > strings.Index(cmd, "session-hub-herdr") {
		t.Fatalf("hub cell must start session-hub-herdr after herdr-core: %q", cmd)
	}
	if cmd := strings.Join(r.workerCommand(plain), " "); strings.Contains(cmd, "session-hub") {
		t.Fatalf("herdr cell without a hub: %q", cmd)
	}
	orca := &api.ProjectWorkspace{ObjectMeta: metav1.ObjectMeta{Name: "demo"}, Spec: api.WorkspaceSpec{OrchestratorClassName: "orca", SessionHub: &api.SessionHubSpec{Enabled: true}}}
	if cmd := strings.Join(r.workerCommand(orca), " "); strings.Contains(cmd, "session-hub-herdr") {
		t.Fatalf("non-herdr hub cell: %q", cmd)
	}
}
