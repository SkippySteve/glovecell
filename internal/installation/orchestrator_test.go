package installation

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestChartOrchestratorClassCRDMatchesSource(t *testing.T) {
	source, err := os.ReadFile("../../config/crd/orchestratorclasses.yaml")
	if err != nil {
		t.Fatal(err)
	}
	chart, err := os.ReadFile("../../charts/agent-project-controller/crds/orchestratorclasses.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(source, chart) {
		t.Fatal("packaged OrchestratorClass CRD differs from controller schema")
	}
	kustomization, err := os.ReadFile("../../config/crd/kustomization.yaml")
	if err != nil || !bytes.Contains(kustomization, []byte("- orchestratorclasses.yaml")) {
		t.Fatal("kustomize CRDs omit OrchestratorClass", err)
	}
}

// The controller reads classes and writes only their status.
func TestDeployRBACOrchestratorClassesReadOnly(t *testing.T) {
	for _, path := range []string{"../../config/deploy/rbac.yaml", "../../charts/agent-project-controller/templates/rbac.yaml"} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		text := string(raw)
		if !strings.Contains(text, "resources: [orchestratorclasses]\n    verbs: [get, list, watch]\n") || !strings.Contains(text, "resources: [orchestratorclasses/status]\n    verbs: [get, update, patch]\n") {
			t.Fatalf("%s: OrchestratorClass rules missing or wider than read plus status", path)
		}
	}
}

func TestHelmOrchestratorClasses(t *testing.T) {
	if os.Getenv("APC_HELM_TEST") != "true" {
		t.Skip("set APC_HELM_TEST=true with Helm installed")
	}
	config := map[string]any{"FORGEJO_ORIGIN": "https://git.example.com", "FORGEJO_OWNER": "operator", "PUBLIC_ORIGIN": "https://projects.example.com", "OIDC_ISSUER": "https://identity.example.com/realm", "OIDC_CLIENT_ID": "projects"}
	values := map[string]any{"controllerImage": "registry.example.com/controller@sha256:" + strings.Repeat("a", 64), "workspaceImage": "registry.example.com/workspace@sha256:" + strings.Repeat("b", 64), "config": config}
	path := filepath.Join(t.TempDir(), "values.json")
	render := func() ([]byte, error) {
		b, _ := json.Marshal(values)
		if err := os.WriteFile(path, b, 0600); err != nil {
			t.Fatal(err)
		}
		return exec.Command("helm", "template", "qualification", "../../charts/agent-project-controller", "-n", "test-control", "-f", path, "--include-crds").CombinedOutput()
	}
	output, err := render()
	if err != nil {
		t.Fatalf("default: %v: %s", err, output)
	}
	if !bytes.Contains(output, []byte("name: orchestratorclasses.projects.agentwork.dev")) {
		t.Fatal("OrchestratorClass CRD not packaged")
	}
	if bytes.Contains(output, []byte("apiVersion: projects.agentwork.dev/v1alpha1\nkind: OrchestratorClass\n")) {
		t.Fatal("default chart creates orchestrator classes")
	}
	classes := map[string]any{"orca": map[string]any{"enabled": true}, "command": map[string]any{"enabled": true, "name": "herdr"}}
	values["orchestratorClasses"] = classes
	if output, err = render(); err != nil {
		t.Fatalf("orca and command: %v: %s", err, output)
	}
	for _, want := range []string{"name: orca\n", "controllerName: projects.agentwork.dev/orca", "name: herdr\n", "controllerName: projects.agentwork.dev/command"} {
		if !bytes.Contains(output, []byte(want)) {
			t.Fatalf("missing %q", want)
		}
	}
	if bytes.Contains(output, []byte("controllerName: projects.agentwork.dev/t3")) {
		t.Fatal("t3 class created without being enabled")
	}
	classes["t3"] = map[string]any{"enabled": true}
	if _, err = render(); err == nil {
		t.Fatal("t3 class accepted without T3 Connect")
	}
	values["t3Connect"] = map[string]any{"enabled": true, "existingClaim": "t3-account"}
	if output, err = render(); err != nil || !bytes.Contains(output, []byte("controllerName: projects.agentwork.dev/t3")) {
		t.Fatalf("t3 class with T3 Connect: %v: %s", err, output)
	}
	classes["orca"] = map[string]any{"enabled": "yes"}
	if _, err = render(); err == nil {
		t.Fatal("non-boolean enabled accepted")
	}
	classes["orca"] = map[string]any{"enabled": true, "name": "Bad_Name"}
	if _, err = render(); err == nil {
		t.Fatal("invalid class name accepted")
	}
}
