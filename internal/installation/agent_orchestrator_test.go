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

func renderAgentOrchestrator(t *testing.T, mutate func(values map[string]any)) ([]byte, error) {
	t.Helper()
	values := map[string]any{
		"controllerImage":     "registry.example.com/controller@sha256:" + strings.Repeat("a", 64),
		"workspaceImage":      "registry.example.com/workspace@sha256:" + strings.Repeat("b", 64),
		"config":              map[string]any{"FORGEJO_ORIGIN": "https://git.example.com", "FORGEJO_OWNER": "operator", "PUBLIC_ORIGIN": "https://projects.example.com", "OIDC_ISSUER": "https://identity.example.com/realm", "OIDC_CLIENT_ID": "projects"},
		"orchestratorClasses": map[string]any{"ao": map[string]any{"enabled": true}},
		"agentOrchestrator": map[string]any{
			"enabled": true,
			"image":   "registry.example.com/ao-workspace@sha256:" + strings.Repeat("c", 64),
			"domain":  "ao.example.com",
			"gateway": map[string]any{"name": "gw", "namespace": "traefik-system", "sectionName": "lan"},
		},
	}
	if mutate != nil {
		mutate(values)
	}
	path := filepath.Join(t.TempDir(), "values.json")
	b, _ := json.Marshal(values)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return exec.Command("helm", "template", "qualification", "../../charts/agent-project-controller", "-n", "test-control", "-f", path).CombinedOutput()
}

func TestHelmAgentOrchestrator(t *testing.T) {
	if os.Getenv("APC_HELM_TEST") != "true" {
		t.Skip("set APC_HELM_TEST=true with Helm installed")
	}
	out, err := renderAgentOrchestrator(t, func(v map[string]any) {
		delete(v, "agentOrchestrator")
		delete(v, "orchestratorClasses")
	})
	if err != nil {
		t.Fatalf("default render: %v: %s", err, out)
	}
	if !bytes.Contains(out, []byte(`AGENT_ORCHESTRATOR_ENABLED: "false"`)) || bytes.Contains(out, []byte("AGENT_ORCHESTRATOR_IMAGE")) || bytes.Contains(out, []byte("projects.agentwork.dev/agent-orchestrator")) {
		t.Fatalf("AO active by default: %s", out)
	}

	out, err = renderAgentOrchestrator(t, nil)
	if err != nil {
		t.Fatalf("render: %v: %s", err, out)
	}
	for _, want := range []string{
		`AGENT_ORCHESTRATOR_ENABLED: "true"`,
		`AGENT_ORCHESTRATOR_IMAGE: "registry.example.com/ao-workspace@sha256:` + strings.Repeat("c", 64) + `"`,
		`AGENT_ORCHESTRATOR_CLASS: "ao"`,
		`AGENT_ORCHESTRATOR_DOMAIN: "ao.example.com"`,
		`AGENT_ORCHESTRATOR_GATEWAY_SECTION: "lan"`,
		"controllerName: projects.agentwork.dev/agent-orchestrator",
	} {
		if !bytes.Contains(out, []byte(want)) {
			t.Fatalf("missing %q", want)
		}
	}

	for name, tc := range map[string]struct {
		mutate func(map[string]any)
		err    string
	}{
		"class without AO": {func(v map[string]any) { v["agentOrchestrator"].(map[string]any)["enabled"] = false }, "orchestratorClasses.ao.enabled requires agentOrchestrator.enabled"},
		"tag image": {func(v map[string]any) {
			v["agentOrchestrator"].(map[string]any)["image"] = "registry.example.com/ao:latest"
		}, "agentOrchestrator.image must use @sha256"},
		"domain with scheme": {func(v map[string]any) { v["agentOrchestrator"].(map[string]any)["domain"] = "https://ao.example.com" }, "agentOrchestrator.domain must be a DNS suffix"},
		"domain without gateway": {func(v map[string]any) {
			v["agentOrchestrator"].(map[string]any)["gateway"] = map[string]any{"name": "gw"}
		}, "agentOrchestrator.gateway.name/namespace/sectionName required"},
		"config override": {func(v map[string]any) { v["config"].(map[string]any)["AGENT_ORCHESTRATOR_DOMAIN"] = "x.example.com" }, "config.AGENT_ORCHESTRATOR_DOMAIN is managed by the chart"},
	} {
		out, err := renderAgentOrchestrator(t, tc.mutate)
		if err == nil || !bytes.Contains(out, []byte(tc.err)) {
			t.Fatalf("%s: want %q, got %v: %s", name, tc.err, err, out)
		}
	}
}
