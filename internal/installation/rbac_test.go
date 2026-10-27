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
			if has(r.Verbs, "bind") && (len(r.Resources) != 1 || r.Resources[0] != "clusterroles" || len(r.ResourceNames) != 1 || (r.ResourceNames[0] != workerExec && r.ResourceNames[0] != projectAccess)) {
				t.Errorf("ClusterRole %s grants bind beyond %s and %s", role.Name, workerExec, projectAccess)
			}
			if has(r.APIGroups, "", "*") && has(r.Resources, "secrets", "*") {
				t.Errorf("ClusterRole %s grants %v on Secrets cluster-wide", role.Name, r.Verbs)
			}
			if has(r.APIGroups, "", "*") && has(r.Resources, "namespaces", "*") && has(r.Verbs, "delete", "deletecollection", "update", "patch", "*") {
				t.Errorf("ClusterRole %s grants %v on namespaces cluster-wide", role.Name, r.Verbs)
			}
		}
	}
	if b := bindingTo(bindings, projectAccess); b != "" {
		t.Fatalf("ClusterRoleBinding %s makes project access cluster-wide", b)
	}
	access := roles[projectAccess]
	if len(access.Rules) != 2 || !sameRule(access.Rules[0], "secrets", "get", "create", "update", "patch", "delete") || !sameRule(access.Rules[1], "namespaces", "get", "delete") {
		t.Fatalf("project access ClusterRole must grant exactly project Secrets and the namespace object: %+v", access.Rules)
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
	if !strings.Contains(string(kustomization), "PROJECT_ACCESS_CLUSTER_ROLE="+projectAccess) {
		t.Fatal("config/deploy does not tell the controller which project access role to bind")
	}
	if !strings.Contains(string(raw), "kind: Role\nmetadata:\n  name: controller-secrets\nrules:\n  - apiGroups: ['']\n    resources: [secrets]\n") {
		t.Fatal("config/deploy has no namespaced Role for the control namespace's Secrets")
	}
}

const projectAccess = "agent-project-controller-project-access"

func bindingTo(bindings []rbacv1.ClusterRoleBinding, role string) string {
	for _, b := range bindings {
		if b.RoleRef.Name == role {
			return b.Name
		}
	}
	return ""
}

func sameRule(r rbacv1.PolicyRule, resource string, verbs ...string) bool {
	if len(r.APIGroups) != 1 || r.APIGroups[0] != "" || len(r.Resources) != 1 || r.Resources[0] != resource || len(r.ResourceNames) != 0 || len(r.Verbs) != len(verbs) {
		return false
	}
	for i := range verbs {
		if r.Verbs[i] != verbs[i] {
			return false
		}
	}
	return true
}

// The chart template: Secrets appear only in the project access ClusterRole
// (bound per project namespace) and the control namespace Role, and the main
// ClusterRole cannot delete or change namespaces.
func TestChartRBACHasNoClusterWideSecrets(t *testing.T) {
	raw, err := os.ReadFile("../../charts/agent-project-controller/templates/rbac.yaml")
	if err != nil {
		t.Fatal(err)
	}
	found, main := 0, false
	for _, doc := range strings.Split(string(raw), "\n---\n") {
		if strings.Contains(doc, "kind: ClusterRole\nmetadata:\n  name: {{ printf \"%s-%s\" .Release.Namespace .Release.Name | trunc 63 | trimSuffix \"-\" }}\n") {
			main = true
			if !strings.Contains(doc, "resources: [namespaces]\n    verbs: [get, list, watch, create]\n") {
				t.Fatalf("main ClusterRole namespaces rule changed:\n%s", doc)
			}
		}
		if !strings.Contains(doc, "secrets") {
			continue
		}
		switch {
		case strings.Contains(doc, "kind: ClusterRole\n") && strings.Contains(doc, `name: {{ include "apc.projectAccessRole" . }}`):
			found++
		case strings.Contains(doc, "kind: RoleBinding\n") && strings.Contains(doc, "name: controller-secrets\n") && !strings.Contains(doc, "rules:"):
		case strings.Contains(doc, "kind: Role\n") && strings.Contains(doc, "name: controller-secrets\n"):
			found++
		default:
			t.Fatalf("Secrets outside the project access ClusterRole and controller-secrets Role:\n%s", doc)
		}
	}
	if !main {
		t.Fatal("main ClusterRole not found; the test checks nothing")
	}
	if found != 2 {
		t.Fatalf("expected the project access ClusterRole and controller-secrets Role, found %d", found)
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
