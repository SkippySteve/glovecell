package registration

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveOrcaBinaryKeepsSessionChoiceAndAvoidsScreenReader(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"orca", "orca-ide", "orca-dev", "session-orca"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	for _, test := range []struct {
		name, configured, dev, worktree, want string
	}{
		{name: "desktop", want: "orca-ide"},
		{name: "managed terminal", worktree: "workspace", want: "orca"},
		{name: "development session", dev: "/development", want: "orca-dev"},
		{name: "configured session", configured: "session-orca", worktree: "workspace", want: "session-orca"},
		{name: "missing configured executable", configured: "unavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("ORCA_CLI_COMMAND", test.configured)
			t.Setenv("ORCA_DEV_REPO_ROOT", test.dev)
			t.Setenv("ORCA_WORKTREE_ID", test.worktree)
			want := ""
			if test.want != "" {
				want = filepath.Join(dir, test.want)
			}
			if got := ResolveOrcaBinary(); got != want {
				t.Fatalf("got %q, want %q", got, want)
			}
		})
	}
	if err := os.Remove(filepath.Join(dir, "orca-ide")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ORCA_CLI_COMMAND", "")
	t.Setenv("ORCA_DEV_REPO_ROOT", "")
	t.Setenv("ORCA_WORKTREE_ID", "")
	if got := ResolveOrcaBinary(); got != "" {
		t.Fatalf("desktop fell back to %q after orca-ide disappeared", got)
	}
}
