package projects

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
)

func command(t *testing.T, args ...string) {
	t.Helper()
	if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
		t.Fatalf("fixture git: %s %v", out, err)
	}
}
func TestInterruptedCloneRecoveryAndExistingFiles(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	workspace := filepath.Join(root, "workspace")
	command(t, "init", "-b", "main", source)
	if err := os.WriteFile(filepath.Join(source, "README.md"), []byte("fixture\n"), 0600); err != nil {
		t.Fatal(err)
	}
	command(t, "-C", source, "add", "README.md")
	command(t, "-C", source, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "fixture")
	// Rewrite only the disposable HTTPS fixture to its disposable local Git source.
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "url.file://"+source+".insteadOf")
	t.Setenv("GIT_CONFIG_VALUE_0", "https://github.com/fixture/library.git")
	entry := Entry{RepositoryProject: api.RepositoryProject{Name: "library", Host: "github", Repository: "fixture/library", Public: true}, Origin: "https://github.com"}
	if err := os.MkdirAll(filepath.Join(workspace, "repo"), 0700); err != nil {
		t.Fatal(err)
	}
	existing := filepath.Join(workspace, "repo", "existing.txt")
	if err := os.WriteFile(existing, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Prepare(ctx, workspace, []Entry{entry}); err == nil {
		t.Fatal("cancelled clone succeeded")
	}
	if err := Prepare(context.Background(), workspace, []Entry{entry}); err != nil {
		t.Fatal("recovery", err)
	}
	if err := Prepare(context.Background(), workspace, []Entry{entry}); err != nil {
		t.Fatal("restart", err)
	}
	if raw, err := os.ReadFile(existing); err != nil || string(raw) != "keep" {
		t.Fatal("original checkout lost")
	}
	if _, err := os.Stat(filepath.Join(entry.Directory(workspace), "README.md")); err != nil {
		t.Fatal("enrolled checkout missing")
	}
}
func TestUnownedDirectoriesAndSymlinkEscapesDenied(t *testing.T) {
	for _, link := range []bool{false, true} {
		root := t.TempDir()
		entry := Entry{RepositoryProject: api.RepositoryProject{Name: "library", Host: "github", Repository: "fixture/library", Public: true}, Origin: "https://github.com"}
		if err := os.MkdirAll(filepath.Join(root, "projects"), 0700); err != nil {
			t.Fatal(err)
		}
		target := entry.Directory(root)
		if link {
			if err := os.Symlink(t.TempDir(), target); err != nil {
				t.Fatal(err)
			}
		} else {
			if err := os.Mkdir(target, 0700); err != nil {
				t.Fatal(err)
			}
		}
		if err := Prepare(context.Background(), root, []Entry{entry}); err == nil {
			t.Fatal("unowned checkout adopted")
		}
		if _, err := os.Lstat(target); err != nil {
			t.Fatal("unowned directory removed")
		}
	}
}

func TestUnownedStagingFilesSurviveEnrollment(t *testing.T) {
	root := t.TempDir()
	entry := Entry{RepositoryProject: api.RepositoryProject{Name: "library", Host: "github", Repository: "fixture/library", Public: true}, Origin: "https://github.com"}
	stage := filepath.Join(root, "projects", ".enrolling-library")
	if err := os.MkdirAll(stage, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(stage, "existing-data")
	if err := os.WriteFile(file, []byte("retain"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Prepare(context.Background(), root, []Entry{entry}); err == nil {
		t.Fatal("unowned staging directory adopted")
	}
	if raw, err := os.ReadFile(file); err != nil || string(raw) != "retain" {
		t.Fatal("existing staging data removed")
	}
}
