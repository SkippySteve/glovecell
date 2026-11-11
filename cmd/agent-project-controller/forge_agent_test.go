package main

import (
	"os"
	"path/filepath"
	"testing"

	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/forgeagent"
)

// With FORGE_AGENT_URL the controller uses the agent and must not also be
// given the administrative token; without it nothing changes.
func TestForgeAgentConfigurationFailsClosed(t *testing.T) {
	dir := t.TempDir()
	admin := filepath.Join(dir, "admin")
	projected := filepath.Join(dir, "projected")
	os.WriteFile(admin, []byte("admin-token\n"), 0o600)
	os.WriteFile(projected, []byte("sa-token\n"), 0o600)
	t.Setenv("GIT_HOST", "forgejo")
	t.Setenv("FORGEJO_ORIGIN", "https://git.example.com")
	t.Setenv("FORGEJO_OWNER", "steve")
	for _, c := range []struct {
		url, agentToken, adminToken string
		ok, agent                   bool
	}{
		{"", "", admin, true, false},
		{"http://forge-agent:8443", projected, "", true, true},
		{"http://forge-agent:8443", projected, admin, false, false},
		{"http://forge-agent:8443", "", "", false, false},
		{"ftp://forge-agent", projected, "", false, false},
	} {
		t.Setenv("FORGE_AGENT_URL", c.url)
		t.Setenv("FORGE_AGENT_TOKEN_FILE", c.agentToken)
		t.Setenv("FORGEJO_TOKEN_FILE", c.adminToken)
		host, kind, origin, owner, err := openGitHost()
		if (err == nil) != c.ok {
			t.Errorf("%+v: err=%v", c, err)
			continue
		}
		if err != nil {
			continue
		}
		_, isAgent := host.(*forgeagent.Client)
		if isAgent != c.agent || kind != "forgejo" || origin != "https://git.example.com" || owner != "steve" {
			t.Errorf("%+v: host=%T kind=%s origin=%s owner=%s", c, host, kind, origin, owner)
		}
	}
}
