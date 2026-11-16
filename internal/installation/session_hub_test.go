package installation

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestHelmSessionHub(t *testing.T) {
	if os.Getenv("APC_HELM_TEST") != "true" {
		t.Skip("set APC_HELM_TEST=true with Helm installed")
	}
	base := func(v map[string]any) {
		delete(v, "gloveHub")
		delete(v, "herdrCore")
		delete(v, "orchestratorClasses")
	}
	out, err := renderGloveHub(t, base)
	if err != nil {
		t.Fatalf("default render: %v: %s", err, out)
	}
	if !bytes.Contains(out, []byte(`SESSION_HUB_ENABLED: "false"`)) || bytes.Contains(out, []byte("SESSION_HUB_IMAGE")) {
		t.Fatalf("session hub not off by default")
	}

	hub := "registry.example.com/session-hub@sha256:" + strings.Repeat("d", 64)
	agents := []map[string]any{{"id": "cell-codex", "displayName": "Codex", "command": "/usr/local/bin/codex-acp"}}
	out, err = renderGloveHub(t, func(v map[string]any) {
		base(v)
		v["sessionHub"] = map[string]any{"enabled": true, "image": hub, "agents": agents}
	})
	if err != nil {
		t.Fatalf("render: %v: %s", err, out)
	}
	for _, want := range []string{`SESSION_HUB_ENABLED: "true"`, "SESSION_HUB_IMAGE: " + `"` + hub + `"`, `cell-codex`} {
		if !bytes.Contains(out, []byte(want)) {
			t.Fatalf("missing %s", want)
		}
	}

	refused := map[string]func(v map[string]any){
		"tag image": func(v map[string]any) {
			v["sessionHub"] = map[string]any{"enabled": true, "image": "registry.example.com/session-hub:latest", "agents": agents}
		},
		"no agents": func(v map[string]any) {
			v["sessionHub"] = map[string]any{"enabled": true, "image": hub}
		},
		"relative command": func(v map[string]any) {
			v["sessionHub"] = map[string]any{"enabled": true, "image": hub, "agents": []map[string]any{{"id": "a", "displayName": "A", "command": "codex-acp"}}}
		},
		"unknown field": func(v map[string]any) {
			v["sessionHub"] = map[string]any{"enabled": true, "image": hub, "agents": []map[string]any{{"id": "a", "displayName": "A", "command": "/bin/a", "shell": true}}}
		},
		"config override": func(v map[string]any) {
			v["config"].(map[string]any)["SESSION_HUB_ENABLED"] = "true"
		},
	}
	for name, mutate := range refused {
		m := mutate
		if out, err := renderGloveHub(t, func(v map[string]any) { base(v); m(v) }); err == nil {
			t.Errorf("%s: rendered: %s", name, out[:min(200, len(out))])
		}
	}
}
