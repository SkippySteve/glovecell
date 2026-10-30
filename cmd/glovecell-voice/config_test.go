package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExampleConfigLoads(t *testing.T) {
	c, err := loadConfig("../../docs/examples/glovecell-voice-config.json")
	if err != nil {
		t.Fatal(err)
	}
	if !c.Gate.Enabled || len(c.Rooms) != 3 || c.Targets["codex"].Agent != "codex" {
		t.Fatalf("%+v", c)
	}
}

func TestConfigRefusals(t *testing.T) {
	base, _ := os.ReadFile("../../docs/examples/glovecell-voice-config.json")
	dir := t.TempDir()
	for name, edit := range map[string]func(string) string{
		"unknown field":     func(s string) string { return strings.Replace(s, `"steve":`, `"stevee": "x", "steve":`, 1) },
		"no steve":          func(s string) string { return strings.Replace(s, `"steve": "@steve:matrix.example.com",`, ``, 1) },
		"gate without room": func(s string) string { return strings.Replace(s, `"approvals": true`, `"approvals": false`, 1) },
		"room without gate": func(s string) string { return strings.Replace(s, `"enabled": true`, `"enabled": false`, 1) },
		"bad duration":      func(s string) string { return strings.Replace(s, `"24h"`, `"a day"`, 1) },
	} {
		p := filepath.Join(dir, strings.ReplaceAll(name, " ", "-"))
		os.WriteFile(p, []byte(edit(string(base))), 0o600)
		if _, err := loadConfig(p); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestSecretFiles(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "token")
	os.WriteFile(p, []byte("s3cret-value\n"), 0o600)
	t.Setenv("VOICE_TEST_SECRET", p)
	if v, err := secret("VOICE_TEST_SECRET", true); err != nil || v != "s3cret-value" {
		t.Fatalf("%q %v", v, err)
	}
	t.Setenv("VOICE_TEST_SECRET", filepath.Join(dir, "missing"))
	if _, err := secret("VOICE_TEST_SECRET", true); err == nil || strings.Contains(err.Error(), dir) {
		t.Fatalf("missing file: %v", err)
	}
	if v, err := secret("VOICE_TEST_SECRET", false); err != nil || v != "" {
		t.Fatalf("optional missing file: %q %v", v, err)
	}
	t.Setenv("VOICE_TEST_SECRET", "")
	if _, err := secret("VOICE_TEST_SECRET", true); err == nil {
		t.Fatal("required secret missing")
	}
}
