package herdr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Machine is one entry of `hc machine ls --json` on the hub.
type Machine struct {
	Name   string `json:"name"`
	Target string `json:"target"`
	Via    bool   `json:"via"`
}

// Core is the hub's herdr-core machine list.
type Core interface {
	List(ctx context.Context) ([]Machine, error)
	Add(ctx context.Context, name, via string) error
	Remove(ctx context.Context, name string) error
}

// Enroller keeps glove-hub's machine list equal to the Ready workspaces whose
// OrchestratorClass is Class: one machine per workspace, named after it and
// reached with herdr-via. Machines the operator added by hand (any other
// target) are left alone.
type Enroller struct {
	Client    client.Client
	Namespace string
	Class     string
	Core      Core
}

// herdr-core 0.1.19 machine names: 1-32 of a-z 0-9 - _ . starting with a
// letter or digit, and not shaped like a terminal address (t7, t1b).
var (
	machineName  = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,31}$`)
	terminalLike = regexp.MustCompile(`^t[0-9][a-z0-9]*$`)
)

// ValidMachineName reports whether a workspace name can be a hub machine name.
func ValidMachineName(name string) bool {
	return machineName.MatchString(name) && !terminalLike.MatchString(name)
}

func managed(m Machine) bool {
	return m.Via && strings.HasPrefix(m.Target, viaPrefix+" ")
}

// Sync adds missing machines and removes managed ones whose workspace is gone,
// not Ready or no longer uses the class. One failing machine does not stop the
// others; their errors are returned together.
func (e *Enroller) Sync(ctx context.Context) error {
	var list api.ProjectWorkspaceList
	if err := e.Client.List(ctx, &list, client.InNamespace(e.Namespace)); err != nil {
		return err
	}
	want := map[string]string{}
	for i := range list.Items {
		w := &list.Items[i]
		if w.Spec.OrchestratorClassName != e.Class || w.Status.Phase != api.PhaseReady {
			continue
		}
		if !ValidMachineName(w.Name) {
			slog.Warn("workspace name cannot be a herdr machine name; not enrolled", "workspace", w.Name)
			continue
		}
		want[w.Name] = MachineViaCommand(w.Name)
	}
	machines, err := e.Core.List(ctx)
	if err != nil {
		return err
	}
	have := map[string]Machine{}
	var errs []error
	for _, m := range machines {
		have[m.Name] = m
		if !managed(m) || want[m.Name] == m.Target {
			continue
		}
		if err := e.Core.Remove(ctx, m.Name); err != nil {
			errs = append(errs, fmt.Errorf("remove %s: %w", m.Name, err))
			continue
		}
		delete(have, m.Name)
		slog.Info("herdr machine removed", "machine", m.Name)
	}
	names := make([]string, 0, len(want))
	for name := range want {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if m, ok := have[name]; ok {
			if !managed(m) {
				slog.Warn("herdr machine name taken by a hand-added machine; not enrolled", "workspace", name)
			}
			continue
		}
		if err := e.Core.Add(ctx, name, want[name]); err != nil {
			errs = append(errs, fmt.Errorf("add %s: %w", name, err))
			continue
		}
		slog.Info("herdr machine added", "machine", name)
	}
	return errors.Join(errs...)
}

// Run syncs now and then every interval until ctx ends. Errors are logged and
// retried; a cell whose hc server is still starting joins on a later pass.
func (e *Enroller) Run(ctx context.Context, interval time.Duration) error {
	for {
		if err := e.Sync(ctx); err != nil {
			slog.Warn("herdr hub sync incomplete; retrying", "error", err.Error())
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(interval):
		}
	}
}

// CLICore drives the hub's herdr-core with its CLI (`herdr-core machine ...`).
type CLICore struct {
	Binary string
	Env    []string
}

func (c CLICore) run(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, c.Binary, args...)
	cmd.Env = c.Env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		// --json errors arrive on stdout: {"error":{"kind":...,"message":...}}.
		var e struct {
			Error struct{ Kind, Message string } `json:"error"`
		}
		if json.Unmarshal(out, &e) == nil && e.Error.Kind != "" {
			return nil, fmt.Errorf("%s: %s", e.Error.Kind, e.Error.Message)
		}
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

func (c CLICore) List(ctx context.Context) ([]Machine, error) {
	out, err := c.run(ctx, "machine", "ls", "--json")
	if err != nil {
		return nil, err
	}
	var doc struct {
		Machines []Machine `json:"machines"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		return nil, fmt.Errorf("parse machine list: %w", err)
	}
	return doc.Machines, nil
}

func (c CLICore) Add(ctx context.Context, name, via string) error {
	_, err := c.run(ctx, "machine", "add", "--json", "--name", name, "--via", via)
	return err
}

func (c CLICore) Remove(ctx context.Context, name string) error {
	_, err := c.run(ctx, "machine", "rm", "--json", name)
	return err
}
