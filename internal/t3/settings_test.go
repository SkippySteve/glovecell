package t3

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func instances(t *testing.T, hub []HubAgent) map[string]map[string]any {
	t.Helper()
	raw, _ := json.Marshal(serverSettings(hub))
	var doc struct {
		ProviderInstances map[string]map[string]any `json:"providerInstances"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc.ProviderInstances
}

func TestServerSettingsWithoutHub(t *testing.T) {
	got := instances(t, nil)
	codex := got["codex"]
	cfg := codex["config"].(map[string]any)
	if codex["driver"] != "codex" || codex["enabled"] != true || cfg["binaryPath"] != "/usr/local/bin/apc-codex" || cfg["homePath"] != "/workspace/.apc/codex" {
		t.Fatalf("codex must go through the brokered bridge: %v", codex)
	}
	for _, off := range []string{"claudeAgent", "opencode", "antigravity"} {
		if got[off]["enabled"] != false {
			t.Fatalf("%s must stay off", off)
		}
	}
	for id := range got {
		if len(id) > 4 && id[:4] == "hub-" {
			t.Fatalf("hub provider %s without a hub", id)
		}
	}
}

func TestServerSettingsHubProviders(t *testing.T) {
	got := instances(t, []HubAgent{{ID: "cell-codex", DisplayName: "Codex"}})
	hub := got["hub-cell-codex"]
	cfg := hub["config"].(map[string]any)
	args := cfg["commandArgs"].([]any)
	if hub["driver"] != "acpRegistry" || hub["displayName"] != "Codex (shared)" || cfg["source"] != "local" ||
		cfg["commandPath"] != "/usr/local/bin/agent-project-controller" || len(args) != 3 || args[0] != "session-hub-acp" || args[2] != "cell-codex" {
		t.Fatalf("hub provider %v", hub)
	}
}

func TestHubAgentsFrom(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agents.json")
	if hubAgentsFrom(path) != nil {
		t.Fatal("no file must add no hub providers")
	}
	os.WriteFile(path, []byte("not json"), 0o600)
	if hubAgentsFrom(path) != nil {
		t.Fatal("malformed must add no hub providers")
	}
	// The controller's allowlist carries command and args too; only id and
	// displayName are used.
	os.WriteFile(path, []byte(`[{"id":"cell-codex","displayName":"Codex","command":"/usr/local/bin/codex-acp"},{"id":"--evil","displayName":"x"},{"id":"Bad Id"}]`), 0o600)
	got := hubAgentsFrom(path)
	if len(got) != 1 || got[0].ID != "cell-codex" || got[0].DisplayName != "Codex" {
		t.Fatalf("only valid ids pass: %v", got)
	}
}

func TestProjectExists(t *testing.T) {
	if !projectExists([]byte("ERROR (#2): ProjectAlreadyExistsError: An active project already exists for '/workspace/repo'.")) {
		t.Fatal("an existing project is success")
	}
	if projectExists([]byte("permission denied")) {
		t.Fatal("other failures are not")
	}
}
