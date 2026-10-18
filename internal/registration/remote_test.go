package registration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalHelperRoundTripAndJail(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(filepath.Join(repo, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "src", "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "secret")
	if err := os.WriteFile(outside, []byte("nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(repo, "link")); err != nil {
		t.Fatal(err)
	}

	remote, _, err := StartLocalHelper(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer remote.Close()

	if err := remote.Ping(); err != nil {
		t.Fatal(err)
	}
	names, err := remote.ReadDir(repo)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(names, ",")
	if !strings.Contains(joined, "src") || !strings.Contains(joined, "link") {
		t.Fatal(names)
	}
	data, err := remote.Read(filepath.Join(repo, "src", "a.txt"), 0, 16)
	if err != nil || string(data) != "hello" {
		t.Fatalf("%q %v", data, err)
	}
	attrs, err := remote.Stat(filepath.Join(repo, "link"))
	if err != nil {
		t.Fatal(err)
	}
	if attrs.Mode&0o170000 != 0o120000 {
		t.Fatalf("expected symlink mode, got %#o", attrs.Mode)
	}
	if _, err := remote.Read(filepath.Join(repo, "link"), 0, 16); err == nil {
		t.Fatal("read through symlink escape succeeded")
	}
	if _, err := remote.Read(outside, 0, 16); err == nil {
		t.Fatal("direct outside read succeeded")
	}
	if err := remote.Create(filepath.Join(repo, "src", "a.txt"), 0o644); err == nil {
		t.Fatal("O_EXCL overwrite succeeded")
	}
	if err := remote.Create(filepath.Join(repo, "src", "b.txt"), 0o644); err != nil {
		t.Fatal(err)
	}
	n, err := remote.Write(filepath.Join(repo, "src", "b.txt"), 0, []byte("world"))
	if err != nil || n != 5 {
		t.Fatalf("%d %v", n, err)
	}
}
