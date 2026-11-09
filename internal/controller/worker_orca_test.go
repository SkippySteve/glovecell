package controller

import (
	"strings"
	"testing"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestUsesOrcaSpoke(t *testing.T) {
	w := &api.ProjectWorkspace{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"},
		Spec:       api.WorkspaceSpec{OrchestratorClassName: "orca"},
	}
	r := &Reconciler{Config: Config{OrcaHubSpoke: true, OrcaRuntimeImage: "img@sha256:abc"}}
	if !r.usesOrcaSpoke(w) {
		t.Fatal("expected orca spoke")
	}
	if r.orcaSpokeImage(w) != "img@sha256:abc" {
		t.Fatal(r.orcaSpokeImage(w))
	}
	cmd := strings.Join(r.orcaSpokeCommand(w), " ")
	if !strings.Contains(cmd, "orcad") || !strings.Contains(cmd, "orca-runtime.apc-") {
		t.Fatal(cmd)
	}
	if !strings.Contains(cmd, "/opt/orca/runtimes/node-") || !strings.Contains(cmd, "/opt/orca/node/bin/node") {
		t.Fatal("spoke start must probe both Node layouts:", cmd)
	}
	if svc := r.orcaSpokeService(w); svc == nil || svc.Name != "orca-runtime" {
		t.Fatal(svc)
	}
	r.Config.OrcaHubSpoke = false
	if r.usesOrcaSpoke(w) {
		t.Fatal("disabled")
	}
}
