package registration

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFuseMountLooksLikeGitToGitAndOrca(t *testing.T) {
	if os.Getenv("APC_ORCA_INTEGRATION") != "1" {
		t.Skip("set APC_ORCA_INTEGRATION=1 to test against a disposable Orca profile")
	}
	if _, err := os.Stat("/dev/fuse"); err != nil {
		t.Skip("no /dev/fuse")
	}
	if _, err := exec.LookPath("fusermount3"); err != nil {
		t.Skip("no fusermount3")
	}
	orca, err := exec.LookPath("orca-ide")
	if err != nil {
		orca, err = exec.LookPath("orca")
	}
	if err != nil {
		t.Skip("no orca")
	}

	repo := t.TempDir()
	mount := filepath.Join(t.TempDir(), "mnt")
	if err := os.MkdirAll(mount, 0o700); err != nil {
		t.Fatal(err)
	}
	run := func(argv ...string) {
		t.Helper()
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.Dir = repo
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %s", err, out)
		}
	}
	run("git", "init")
	if err := os.WriteFile(filepath.Join(repo, "README"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("git", "add", ".")
	run("git", "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-m", "i")

	remote, cmd, err := StartLocalHelper(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer remote.Close()
	_ = cmd
	server, err := MountRemote(mount, "apc:test", repo, remote)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = server.Unmount()
		_ = exec.Command("fusermount3", "-u", mount).Run()
	}()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(mount, "README")); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	out, err := exec.Command("git", "-C", mount, "rev-parse", "--is-inside-work-tree").CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "true" {
		t.Fatalf("git through fuse: %v %s", err, out)
	}

	add := exec.Command(orca, "repo", "add", "--path", mount, "--json")
	addOut, addErr := add.CombinedOutput()
	t.Logf("orca add: %s", addOut)
	if addErr != nil {
		t.Fatalf("orca add failed: %v %s", addErr, addOut)
	}
	id := parseOrcaRepoID(string(addOut))
	if id == "" {
		t.Fatalf("no repo id in %s", addOut)
	}
	del := exec.Command(orca, "project", "setup-delete", "--setup", id, "--force", "--json")
	if out, err := del.CombinedOutput(); err != nil {
		t.Fatalf("orca delete id=%s: %v %s", id, err, out)
	}
}
