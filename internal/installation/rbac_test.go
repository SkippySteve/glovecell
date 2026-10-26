package installation

import (
	"os"
	"strings"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	"sigs.k8s.io/yaml"
)

const workerExec = "agent-project-controller-worker-exec"

func has(list []string, values ...string) bool {
	for _, v := range list {
		for _, want := range values {
			if v == want {
				return true
			}
		}
	}
	return false
}

// The controller never holds pods/exec (or attach, port-forward, escalate,
// impersonate) cluster-wide. It gets exec per project namespace by binding the
// worker exec ClusterRole, and `bind` is limited to that one role.
func TestDeployRBACHasNoClusterWideExec(t *testing.T) {
	raw, err := os.ReadFile("../../config/deploy/rbac.yaml")
	if err != nil {
		t.Fatal(err)
	}
	roles := map[string]rbacv1.ClusterRole{}
	var bindings []rbacv1.ClusterRoleBinding
	for _, doc := range strings.Split(string(raw), "\n---\n") {
		var meta struct{ Kind string }
		if err := yaml.Unmarshal([]byte(doc), &meta); err != nil {
			t.Fatal(err)
		}
		switch meta.Kind {
		case "ClusterRole":
			var r rbacv1.ClusterRole
			if err := yaml.Unmarshal([]byte(doc), &r); err != nil {
				t.Fatal(err)
			}
			roles[r.Name] = r
		case "ClusterRoleBinding":
			var b rbacv1.ClusterRoleBinding
			if err := yaml.Unmarshal([]byte(doc), &b); err != nil {
				t.Fatal(err)
			}
			bindings = append(bindings, b)
		}
	}
	if len(bindings) == 0 {
		t.Fatal("no ClusterRoleBinding found; the test checks nothing")
	}
	for _, b := range bindings {
		if b.RoleRef.Name == workerExec {
			t.Fatalf("ClusterRoleBinding %s makes worker exec cluster-wide", b.Name)
		}
		role, ok := roles[b.RoleRef.Name]
		if !ok {
			t.Fatalf("ClusterRoleBinding %s refers to unknown ClusterRole %s", b.Name, b.RoleRef.Name)
		}
		for _, r := range role.Rules {
			if has(r.APIGroups, "", "*") && has(r.Resources, "pods/exec", "pods/attach", "pods/portforward", "pods/*", "*") {
				t.Errorf("ClusterRole %s grants %v on %v cluster-wide", role.Name, r.Verbs, r.Resources)
			}
			if has(r.Verbs, "escalate", "impersonate", "*") {
				t.Errorf("ClusterRole %s grants %v", role.Name, r.Verbs)
			}
			if has(r.Verbs, "bind") && (len(r.Resources) != 1 || r.Resources[0] != "clusterroles" || len(r.ResourceNames) != 1 || r.ResourceNames[0] != workerExec) {
				t.Errorf("ClusterRole %s grants bind beyond %s", role.Name, workerExec)
			}
		}
	}
	exec := roles[workerExec]
	if len(exec.Rules) != 1 || len(exec.Rules[0].Resources) != 1 || exec.Rules[0].Resources[0] != "pods/exec" || len(exec.Rules[0].Verbs) != 1 || exec.Rules[0].Verbs[0] != "create" {
		t.Fatalf("worker exec ClusterRole must grant exactly create on pods/exec: %+v", exec.Rules)
	}
	kustomization, err := os.ReadFile("../../config/deploy/kustomization.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(kustomization), "WORKER_EXEC_CLUSTER_ROLE="+workerExec) {
		t.Fatal("config/deploy does not tell the controller which role to bind")
	}
}

// The chart template: pods/exec appears only in the worker exec ClusterRole.
// Rendering is covered by TestHelmPackage when Helm is installed.
func TestChartRBACHasNoClusterWideExec(t *testing.T) {
	raw, err := os.ReadFile("../../charts/agent-project-controller/templates/rbac.yaml")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, doc := range strings.Split(string(raw), "\n---\n") {
		if !strings.Contains(doc, "pods/exec") && !strings.Contains(doc, "pods/attach") && !strings.Contains(doc, "pods/portforward") {
			continue
		}
		if !strings.Contains(doc, "kind: ClusterRole\n") || !strings.Contains(doc, `name: {{ include "apc.workerExecRole" . }}`) || strings.Count(doc, "resources:") != 1 {
			t.Fatalf("pods/exec outside the worker exec ClusterRole:\n%s", doc)
		}
		found = true
	}
	if !found {
		t.Fatal("worker exec ClusterRole missing from the chart")
	}
	if strings.Contains(string(raw), "escalate") || strings.Contains(string(raw), "impersonate") {
		t.Fatal("chart grants escalate or impersonate")
	}
}
