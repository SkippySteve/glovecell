package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHandoffIntakeConfigurationFailsClosed(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "token")
	short := filepath.Join(dir, "short")
	os.WriteFile(good, []byte("intake-token-0123456789abcdef0123456789\n"), 0o600)
	os.WriteFile(short, []byte("short\n"), 0o600)
	for _, c := range []struct {
		url, file string
		ok, on    bool
	}{
		{"", "", true, false},
		{"", good, false, false},
		{"http://intake:8791", "", false, false},
		{"http://intake:8791", filepath.Join(dir, "missing"), false, false},
		{"http://intake:8791", short, false, false},
		{"http://intake:8791", good, true, true},
	} {
		t.Setenv("HANDOFF_URL", c.url)
		t.Setenv("HANDOFF_TOKEN_FILE", c.file)
		h, err := handoffsFromEnv()
		if (err == nil) != c.ok || (h != nil) != c.on {
			t.Errorf("url=%q file=%q: handoffs=%v err=%v", c.url, c.file, h != nil, err)
		}
		if h != nil && h.Token != "intake-token-0123456789abcdef0123456789" {
			t.Errorf("token not trimmed: %q", h.Token)
		}
	}
}
