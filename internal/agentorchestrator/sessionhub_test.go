package agentorchestrator

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSlotEnv(t *testing.T) {
	if SlotEnv("droid") != "GLOVECELL_AO_HUB_DROID" || SlotEnv("dsh") != "GLOVECELL_AO_HUB_DSH" {
		t.Fatal("slot env names")
	}
}

// The daemon of a cell without a hub starts exactly as before; a hub cell's
// command only adds the slot prelude, before the daemon starts.
func TestHubWorkerCommand(t *testing.T) {
	plain, hub := WorkerCommand()[2], HubWorkerCommand()[2]
	if strings.Contains(plain, "session-hub") || strings.Contains(plain, "GLOVECELL_AO_HUB") {
		t.Fatalf("plain command knows about slots: %s", plain)
	}
	if strings.Replace(hub, hubPrelude(AgentsFile), "", 1) != plain {
		t.Fatal("hub command differs from the plain one beyond the prelude")
	}
	if !strings.Contains(hub, AgentsFile) || strings.Index(hub, "GLOVECELL_AO_HUB_DROID") > strings.Index(hub, `"$AO" daemon &`) {
		t.Fatal("slots must be set up from the agents file before the daemon starts")
	}
}

// Run the prelude against allowlist files: slots go to the first valid ids in
// order and first on PATH, and AO's harness updates go off; no file, an
// empty or malformed one leaves PATH and AO's settings alone.
func TestHubPrelude(t *testing.T) {
	dir := t.TempDir()
	run := func(content string) string {
		t.Helper()
		file := filepath.Join(dir, "agents.json")
		_ = os.Remove(file)
		if content != "" {
			if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		script := "set -e\n" + hubPrelude(file) + `echo "$PATH|${GLOVECELL_AO_HUB_DROID:-}|${GLOVECELL_AO_HUB_DSH:-}|${AO_HARNESS_UPDATE_CHECKS:-}|${AO_MANAGED_HARNESS_INSTALLS:-}|$#"`
		cmd := exec.Command("/bin/sh", "-c", script)
		cmd.Env = []string{"PATH=/usr/bin:/bin"}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		return strings.TrimSpace(string(out))
	}
	off := "/usr/bin:/bin|||||0"
	for name, content := range map[string]string{
		"no file":   "",
		"empty":     "[]",
		"not array": `{"id":"cell-claude"}`,
		"malformed": `[{"id":`,
		"bad ids":   `[{"id":"Cell"},{"id":"a b"},{"id":"$(id)"},{"id":7},"cell-x"]`,
	} {
		if got := run(content); got != off {
			t.Errorf("%s: %s", name, got)
		}
	}
	if got := run(`[{"id":"cell-claude","displayName":"Claude","command":"/usr/local/bin/claude-acp"}]`); got != SlotDir+"/droid:/usr/bin:/bin|cell-claude||off|off|0" {
		t.Fatalf("one agent: %s", got)
	}
	got := run(`[{"id":"Bad"},{"id":"cell-codex","displayName":"Codex"},{"id":"cell-claude","displayName":"Claude"},{"id":"cell-grok","displayName":"Grok"}]`)
	if got != SlotDir+"/dsh:"+SlotDir+"/droid:/usr/bin:/bin|cell-codex|cell-claude|off|off|0" {
		t.Fatalf("three agents: %s", got)
	}
}

// slotScript installs the slot script as <dir>/<slot>/<slot>, with the
// connector replaced by a recorder, and returns its path.
func slotScript(t *testing.T, slot string) (script, record string) {
	t.Helper()
	raw, err := os.ReadFile("../../config/agent-orchestrator/session-hub-slot.sh")
	if err != nil {
		t.Fatal(err)
	}
	const connector = "/usr/local/bin/agent-project-controller"
	if !strings.Contains(string(raw), "exec "+connector+" session-hub-acp --agent \"$agent\"") {
		t.Fatal("slot script no longer execs the connector")
	}
	dir := t.TempDir()
	record = filepath.Join(dir, "record")
	fake := filepath.Join(dir, "connector")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho \"$*\" > "+record+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	script = filepath.Join(dir, slot, slot)
	if err := os.MkdirAll(filepath.Dir(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script, []byte(strings.Replace(string(raw), connector, fake, 1)), 0o755); err != nil {
		t.Fatal(err)
	}
	return script, record
}

func TestSlotScript(t *testing.T) {
	for _, tc := range []struct {
		slot, env string
		args      []string
		code      int
		want      string
	}{
		// AO 0.15.2 Chat launches.
		{"droid", "GLOVECELL_AO_HUB_DROID=cell-claude", []string{"--settings", "/workspace/ao/x.json", "exec", "--output-format", "acp-daemon"}, 0, "session-hub-acp --agent cell-claude"},
		{"droid", "GLOVECELL_AO_HUB_DROID=cell-claude", []string{"exec", "--output-format", "acp-daemon", "--skip-permissions-unsafe"}, 0, "session-hub-acp --agent cell-claude"},
		{"dsh", "GLOVECELL_AO_HUB_DSH=cell-codex", []string{"--profile", "acp"}, 0, "session-hub-acp --agent cell-codex"},
		// Terminal agent, probes and other uses are refused.
		{"droid", "GLOVECELL_AO_HUB_DROID=cell-claude", []string{"fix the bug"}, 64, ""},
		{"droid", "GLOVECELL_AO_HUB_DROID=cell-claude", []string{"--version"}, 64, ""},
		{"droid", "GLOVECELL_AO_HUB_DROID=cell-claude", []string{"acp-daemon"}, 64, ""},
		{"dsh", "GLOVECELL_AO_HUB_DSH=cell-codex", []string{"--profile", "acp", "--model", "x"}, 64, ""},
		{"dsh", "GLOVECELL_AO_HUB_DSH=cell-codex", nil, 64, ""},
		// Unmapped or malformed mappings never reach the connector.
		{"droid", "", []string{"exec", "--output-format", "acp-daemon"}, 127, ""},
		{"droid", "GLOVECELL_AO_HUB_DSH=cell-codex", []string{"exec", "--output-format", "acp-daemon"}, 127, ""},
		{"dsh", "GLOVECELL_AO_HUB_DSH=cell codex", []string{"--profile", "acp"}, 127, ""},
		{"dsh", "GLOVECELL_AO_HUB_DSH=$(id)", []string{"--profile", "acp"}, 127, ""},
		{"claude", "GLOVECELL_AO_HUB_CLAUDE=cell-claude", []string{"--profile", "acp"}, 127, ""},
	} {
		script, record := slotScript(t, tc.slot)
		cmd := exec.Command(script, tc.args...)
		cmd.Env = []string{"PATH=/usr/bin:/bin"}
		if tc.env != "" {
			cmd.Env = append(cmd.Env, tc.env)
		}
		out, err := cmd.CombinedOutput()
		code := 0
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			code = exit.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		got, _ := os.ReadFile(record)
		if code != tc.code || strings.TrimSpace(string(got)) != tc.want {
			t.Errorf("%s %v (%s): exit %d, connector %q, output %s", tc.slot, tc.args, tc.env, code, got, out)
		}
	}
}
