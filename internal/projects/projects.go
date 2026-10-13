// Package projects prepares additional enrolled checkouts on one workspace volume.
package projects

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/forgejo"
)

type Entry struct {
	api.RepositoryProject
	Origin   string `json:"origin"`
	Username string `json:"username,omitempty"`
	Token    string `json:"token,omitempty"`
}

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)

func Read(path string) ([]Entry, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(raw) > 128<<10 {
		return nil, errors.New("project registry is too large")
	}
	var entries []Entry
	if json.Unmarshal(raw, &entries) != nil || len(entries) > 16 {
		return nil, errors.New("invalid project registry")
	}
	names := map[string]bool{}
	for _, entry := range entries {
		if err := entry.Validate(); err != nil {
			return nil, err
		}
		if names[entry.Name] {
			return nil, errors.New("duplicate project registry name")
		}
		names[entry.Name] = true
	}
	return entries, nil
}
func (e Entry) Validate() error {
	parts := strings.Split(e.Repository, "/")
	if !namePattern.MatchString(e.Name) || len(parts) != 2 || forgejo.ValidateRepository(e.Repository, parts[0]) != nil {
		return errors.New("invalid project registry entry")
	}
	u, err := url.Parse(e.Origin)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.Port() != "" && u.Port() != "443" {
		return errors.New("invalid project origin")
	}
	switch e.Host {
	case "forgejo":
		if e.Public || e.Username == "" || e.Token == "" || strings.ContainsAny(e.Username+e.Token, "\r\n\x00") || u.Hostname() == "github.com" || u.Hostname() == "api.github.com" {
			return errors.New("invalid scoped Forgejo grant")
		}
	case "github":
		if e.Origin != "https://github.com" || e.Username != "" || e.Token != "" {
			return errors.New("GitHub project requires anonymous public read-only access")
		}
	default:
		return errors.New("unsupported project host")
	}
	return nil
}
func (e Entry) Directory(workspace string) string {
	return filepath.Join(workspace, "projects", e.Name)
}
func (e Entry) URL() string { return e.Origin + "/" + e.Repository + ".git" }

func git(ctx context.Context, args ...string) error {
	c := exec.CommandContext(ctx, "git", args...)
	c.Stdout, c.Stderr = io.Discard, io.Discard
	if c.Run() != nil {
		return errors.New("enrolled repository Git operation failed; verify its grant and inspected connection")
	}
	return nil
}
func plainDirectory(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != filepath.Clean(path) {
		return errors.New("project storage must not contain symlinks")
	}
	return nil
}

func Prepare(ctx context.Context, workspace string, entries []Entry) error {
	if len(entries) == 0 {
		return nil
	}
	for _, path := range []string{filepath.Join(workspace, "projects"), filepath.Join(workspace, ".apc", "enrolled")} {
		if err := plainDirectory(path); err != nil {
			return err
		}
	}
	for _, e := range entries {
		if err := prepare(ctx, workspace, e); err != nil {
			return err
		}
	}
	return nil
}
func prepare(ctx context.Context, workspace string, e Entry) error {
	if err := e.Validate(); err != nil {
		return err
	}
	target := e.Directory(workspace)
	stage := filepath.Join(workspace, "projects", ".enrolling-"+e.Name)
	marker := filepath.Join(workspace, ".apc", "enrolled", e.Name+".json")
	// The receipt contains only immutable ownership metadata, never credentials.
	receipt, _ := json.Marshal(e.RepositoryProject)
	saved, err := os.ReadFile(marker)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if len(saved) > 0 && string(saved) != string(receipt) {
		return errors.New("project ownership receipt mismatch")
	}
	if len(saved) == 0 {
		if _, err := os.Lstat(target); !os.IsNotExist(err) {
			return errors.New("refusing to adopt an existing project directory")
		}
		if _, err := os.Lstat(stage); !os.IsNotExist(err) {
			return errors.New("refusing to remove an unowned enrollment staging directory")
		}
		tmp, err := os.CreateTemp(filepath.Dir(marker), ".enroll-")
		if err != nil {
			return err
		}
		defer os.Remove(tmp.Name())
		if _, err = tmp.Write(receipt); err != nil {
			tmp.Close()
			return err
		}
		if err = tmp.Sync(); err != nil {
			tmp.Close()
			return err
		}
		if err = tmp.Close(); err != nil {
			return err
		}
		if err = os.Link(tmp.Name(), marker); err != nil {
			return err
		}
	}
	if _, err := os.Lstat(target); os.IsNotExist(err) {
		if st, err := os.Lstat(stage); err == nil && st.Mode()&os.ModeSymlink != 0 {
			return errors.New("enrollment staging directory must not be a symlink")
		}
		if err := os.RemoveAll(stage); err != nil {
			return err
		}
		if err := git(ctx, "clone", "--", e.URL(), stage); err != nil {
			return err
		}
		if err := git(ctx, "-C", stage, "switch", "-c", "agent/"+e.Name); err != nil {
			return err
		}
		if err := os.Rename(stage, target); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if err := plainDirectory(target); err != nil {
		return err
	}
	c := exec.CommandContext(ctx, "git", "-C", target, "config", "--get", "remote.origin.url")
	origin, err := c.Output()
	if err != nil || strings.TrimSpace(string(origin)) != e.URL() {
		return errors.New("enrolled repository origin changed; refusing automatic replacement")
	}
	if err := git(ctx, "-C", target, "fetch", "origin"); err != nil {
		return err
	}
	if e.Username != "" {
		for key, value := range map[string]string{"user.name": e.Username, "user.email": e.Username + "@agentwork.invalid"} {
			if err := git(ctx, "-C", target, "config", key, value); err != nil {
				return err
			}
		}
	}
	return nil
}

func Register(ctx context.Context) error {
	entries, err := Read("/identity/projects.json")
	if err != nil {
		return err
	}
	for _, e := range entries {
		const check = `import sqlite3,sys
c=sqlite3.connect('file:/workspace/.apc/t3/userdata/state.sqlite?mode=ro',uri=True)
rows=c.execute('select workspace_root from projection_projects where workspace_root=? and deleted_at is null',(sys.argv[1],)).fetchall()
sys.exit(0 if len(rows)==1 else 3 if len(rows)==0 else 4)`
		verify := func() error {
			return exec.CommandContext(ctx, "/usr/bin/python3", "-c", check, e.Directory("/workspace")).Run()
		}
		if err := verify(); err == nil {
			continue
		} else {
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 3 {
				return errors.New("T3 project registration could not be inspected")
			}
		}
		c := exec.CommandContext(ctx, "/opt/t3/t3", "project", "add", e.Directory("/workspace"), "--title", e.Name+": "+e.Directory("/workspace"), "--base-dir", "/workspace/.apc/t3")
		c.Stdout, c.Stderr = io.Discard, io.Discard
		if c.Run() != nil {
			return errors.New("enrolled checkout could not be registered with T3")
		}
		if verify() != nil {
			return errors.New("T3 project registration was not verified")
		}
	}
	return nil
}
