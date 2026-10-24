package main

import (
	"os"
	"path/filepath"
	"testing"

	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/network"
)

func TestHandoffIntakeConfigurationFailsClosed(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "token")
	short := filepath.Join(dir, "short")
	os.WriteFile(good, []byte("intake-token-0123456789abcdef0123456789\n"), 0o600)
	os.WriteFile(short, []byte("short\n"), 0o600)
	for _, c := range []struct {
		url, file, sandbox string
		ok, on             bool
	}{
		{"", "", "", true, false},
		{"", good, "", false, false},
		{"", "", "orca-pilot", false, false},
		{"http://intake:8791", "", "", false, false},
		{"http://intake:8791", filepath.Join(dir, "missing"), "", false, false},
		{"http://intake:8791", short, "", false, false},
		{"http://intake:8791", good, "", true, true},
		{"http://intake:8791", good, "orca-pilot", true, true},
		{"http://intake:8791", good, "../orca", false, false},
	} {
		t.Setenv("HANDOFF_URL", c.url)
		t.Setenv("HANDOFF_TOKEN_FILE", c.file)
		t.Setenv("HANDOFF_SANDBOX", c.sandbox)
		h, err := handoffsFromEnv()
		if (err == nil) != c.ok || (h != nil) != c.on {
			t.Errorf("url=%q file=%q sandbox=%q: handoffs=%v err=%v", c.url, c.file, c.sandbox, h != nil, err)
		}
		if h != nil && h.Sandbox != c.sandbox {
			t.Errorf("sandbox %q, want %q", h.Sandbox, c.sandbox)
		}
		if h != nil && h.Token != "intake-token-0123456789abcdef0123456789" {
			t.Errorf("token not trimmed: %q", h.Token)
		}
	}
}

func TestLANGrantConfigurationFailsClosed(t *testing.T) {
	cilium := network.Cilium{}
	native := network.NetworkPolicy{}
	for _, c := range []struct {
		allowed, denied string
		provider        network.Provider
		ok, on          bool
	}{
		{"", "", cilium, true, false},
		{"", "", native, true, false},
		{"192.168.110.0/24", "192.168.105.0/24,192.168.110.1", cilium, true, true},
		{"192.168.110.0/24", "", cilium, false, false},
		{"192.168.110.0/24", "192.168.105.0/24", native, false, false},
		{"0.0.0.0/0", "192.168.105.0/24", cilium, false, false},
		{"192.168.110.2", "192.168.105.0/24", cilium, false, false},
	} {
		t.Setenv("LAN_GRANT_ALLOWED_CIDRS", c.allowed)
		t.Setenv("LAN_GRANT_DENIED_CIDRS", c.denied)
		lan, err := lanFromEnv(c.provider)
		if (err == nil) != c.ok || lan.Enabled() != c.on {
			t.Errorf("%q %q %T: enabled=%v err=%v", c.allowed, c.denied, c.provider, lan.Enabled(), err)
		}
	}
}
