package installation

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

func renderVoice(t *testing.T, voice map[string]any) ([]byte, error) {
	t.Helper()
	values := map[string]any{
		"controllerImage": "registry.example.com/controller@sha256:" + strings.Repeat("a", 64),
		"workspaceImage":  "registry.example.com/workspace@sha256:" + strings.Repeat("b", 64),
		"config":          map[string]any{"FORGEJO_ORIGIN": "https://git.example.com", "FORGEJO_OWNER": "operator", "PUBLIC_ORIGIN": "https://projects.example.com", "OIDC_ISSUER": "https://identity.example.com/realm", "OIDC_CLIENT_ID": "projects"},
	}
	if voice != nil {
		values["voice"] = voice
	}
	path := filepath.Join(t.TempDir(), "values.json")
	b, _ := json.Marshal(values)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return exec.Command("helm", "template", "qualification", "../../charts/agent-project-controller", "-n", "test-control", "-f", path).CombinedOutput()
}

func voiceDocs(t *testing.T, out []byte) map[string]map[string]any {
	t.Helper()
	docs := map[string]map[string]any{}
	for _, d := range bytes.Split(out, []byte("\n---")) {
		var m map[string]any
		if err := yaml.Unmarshal(d, &m); err != nil || m == nil {
			continue
		}
		meta, _ := m["metadata"].(map[string]any)
		docs[m["kind"].(string)+"/"+meta["name"].(string)] = m
	}
	return docs
}

func TestHelmVoice(t *testing.T) {
	if os.Getenv("APC_HELM_TEST") != "true" {
		t.Skip("set APC_HELM_TEST=true with Helm installed")
	}
	out, err := renderVoice(t, nil)
	if err != nil {
		t.Fatalf("default render: %v: %s", err, out)
	}
	if bytes.Contains(out, []byte("glovecell-voice")) {
		t.Fatal("voice service rendered while disabled")
	}
	config, err := os.ReadFile("../../docs/examples/glovecell-voice-config.json")
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(config, &cfg); err != nil {
		t.Fatal(err)
	}
	voice := map[string]any{
		"enabled":        true,
		"image":          "registry.example.com/glovecell-voice@sha256:" + strings.Repeat("c", 64),
		"existingSecret": "glovecell-voice",
		"config":         cfg,
		"networkPolicy": map[string]any{
			"enabled":    true,
			"notifyFrom": []any{map[string]any{"namespaceSelector": map[string]any{"matchLabels": map[string]any{"kubernetes.io/metadata.name": "agent-workspace-orca-pilot"}}}},
			"egress":     []any{map[string]any{"ports": []any{map[string]any{"protocol": "TCP", "port": 443}}}},
		},
	}
	out, err = renderVoice(t, voice)
	if err != nil {
		t.Fatalf("voice render: %v: %s", err, out)
	}
	docs := voiceDocs(t, out)
	dep := docs["Deployment/glovecell-voice"]
	if dep == nil {
		t.Fatalf("no voice Deployment:\n%s", out)
	}
	spec := dep["spec"].(map[string]any)
	if spec["replicas"].(float64) != 1 || spec["strategy"].(map[string]any)["type"] != "Recreate" {
		t.Fatal("voice must run one replica with Recreate")
	}
	pod := spec["template"].(map[string]any)["spec"].(map[string]any)
	if pod["automountServiceAccountToken"] != false {
		t.Fatal("voice pod mounts a Kubernetes token")
	}
	c := pod["containers"].([]any)[0].(map[string]any)
	sc := c["securityContext"].(map[string]any)
	if sc["readOnlyRootFilesystem"] != true || sc["allowPrivilegeEscalation"] != false {
		t.Fatalf("container security context %v", sc)
	}
	text := string(out)
	for _, want := range []string{
		"secretName: \"glovecell-voice\"",
		"secretName: \"qualification-db-app\"",
		"secretName: \"qualification-db-ca\"",
		"VOICE_FORGEJO_TOKEN_FILE",
		"/var/run/glovecell-voice/secrets/forgejo-token",
		"kind: NetworkPolicy",
		"agent-workspace-orca-pilot",
		"\\\"voice-approval\\\"",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("voice render lacks %s", want)
		}
	}
	if docs["ConfigMap/glovecell-voice"] == nil || docs["Service/glovecell-voice"] == nil || docs["NetworkPolicy/glovecell-voice"] == nil {
		t.Fatal("voice ConfigMap, Service or NetworkPolicy missing")
	}
	dbPolicy := docs["NetworkPolicy/qualification-db"]
	if dbPolicy == nil || !strings.Contains(toYAML(t, dbPolicy), "glovecell-voice") {
		t.Fatal("shared database does not admit the voice pod")
	}
	if bytes.Contains(out, []byte("kind: Secret\n")) {
		t.Fatal("chart creates a Secret for the voice service")
	}
	for name, mutate := range map[string]func(map[string]any){
		"mutable image":           func(v map[string]any) { v["image"] = "registry.example.com/glovecell-voice:latest" },
		"no secret":               func(v map[string]any) { v["existingSecret"] = "" },
		"no config":               func(v map[string]any) { v["config"] = map[string]any{} },
		"external without secret": func(v map[string]any) { v["database"] = map[string]any{"mode": "external"} },
		"unknown database mode":   func(v map[string]any) { v["database"] = map[string]any{"mode": "sqlite"} },
	} {
		bad := map[string]any{}
		for k, v := range voice {
			bad[k] = v
		}
		mutate(bad)
		if out, err := renderVoice(t, bad); err == nil {
			t.Errorf("%s accepted:\n%s", name, out)
		}
	}
	voice["database"] = map[string]any{"mode": "external", "existingSecret": "voice-db", "uriKey": "url", "caKey": "ca"}
	out, err = renderVoice(t, voice)
	if err != nil {
		t.Fatalf("external database: %v: %s", err, out)
	}
	if !strings.Contains(string(out), "secretName: \"voice-db\"") || strings.Contains(toYAML(t, voiceDocs(t, out)["NetworkPolicy/qualification-db"]), "glovecell-voice") {
		t.Fatal("external voice database not wired, or the controller database still admits the voice pod")
	}
}

func toYAML(t *testing.T, v any) string {
	b, err := yaml.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
