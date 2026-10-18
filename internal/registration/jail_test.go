package registration

import (
	"errors"
	"testing"
)

func TestConfineAcceptsRootsAndDescendants(t *testing.T) {
	for _, path := range []string{"/workspace/repo", "/workspace/repo/src", "/workspace/worktrees/a"} {
		got, err := Confine(path, DefaultRoots)
		if err != nil || got != path {
			t.Fatalf("%s: got %q %v", path, got, err)
		}
	}
}

func TestConfineRejectsEscape(t *testing.T) {
	for _, path := range []string{
		"",
		"relative",
		"/workspace/repo/../..",
		"/workspace",
		"/workspace/repo-other",
		"/run/model/secret",
		"/workspace/worktrees/../repo/../../etc/passwd",
	} {
		if _, err := Confine(path, DefaultRoots); err == nil {
			t.Fatalf("accepted %q", path)
		}
	}
	// Cleaned escape under a root after .. collapse still fails when outside.
	if got, err := Confine("/workspace/repo/../worktrees/x", DefaultRoots); err != nil || got != "/workspace/worktrees/x" {
		t.Fatalf("sibling under allowed root: got %q %v", got, err)
	}
}

func TestJoinUnderRejectsTraversalNames(t *testing.T) {
	if _, err := JoinUnder("/workspace/repo", "..", DefaultRoots); err == nil {
		t.Fatal("accepted ..")
	}
	if _, err := JoinUnder("/workspace/repo", "a/b", DefaultRoots); err == nil {
		t.Fatal("accepted slash in name")
	}
	got, err := JoinUnder("/workspace/repo", "src", DefaultRoots)
	if err != nil || got != "/workspace/repo/src" {
		t.Fatalf("got %q %v", got, err)
	}
}

func TestRealpathUnderRejectsSymlinkEscape(t *testing.T) {
	realpath := func(p string) (string, error) {
		if p == "/workspace/repo/link" {
			return "/run/model/secret", nil
		}
		return p, nil
	}
	if _, err := RealpathUnder("/workspace/repo/link", DefaultRoots, realpath); err == nil {
		t.Fatal("symlink escape accepted")
	}
	got, err := RealpathUnder("/workspace/repo/src", DefaultRoots, realpath)
	if err != nil || got != "/workspace/repo/src" {
		t.Fatalf("got %q %v", got, err)
	}
	if _, err := RealpathUnder("/workspace/repo/missing", DefaultRoots, func(string) (string, error) {
		return "", errors.New("missing")
	}); err == nil {
		t.Fatal("missing realpath accepted")
	}
}
