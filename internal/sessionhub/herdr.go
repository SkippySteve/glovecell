package sessionhub

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/microsoft/agent-host-protocol/clients/go/ahp"
	"github.com/microsoft/agent-host-protocol/clients/go/ahptypes"
)

// HerdrPanes keeps the Herdr side of a session-hub cell in step with the
// hub (docs/session-hub.md, "Herdr"). It runs in the workspace container of
// a Herdr cell, next to the cell's herdr-core, and:
//
//   - opens one herdr-core terminal running session-hub-chat for every hub
//     session that has none, so a chat started in T3 Code, Orca or Agent
//     Orchestrator shows up in the Herdr apps;
//   - writes one herdr-core template per hub agent (`hc run cell-codex
//     "fix the tests"` starts a hub chat with that task).
//
// It never closes terminals: a terminal the operator closed stays closed.
type HerdrPanes struct {
	// List returns the hub's sessions.
	List func(ctx context.Context) ([]ahptypes.SessionSummary, error)
	// AgentsFile is the cell's allowlist as the workspace container sees it
	// (ClientAgentsFile); it names the templates. Absent: no templates.
	AgentsFile string
	// HC runs the herdr-core CLI.
	HC           func(ctx context.Context, args ...string) ([]byte, error)
	TemplatesDir string
	Cwd          string // working directory of new terminals

	handled map[string]bool // sessions with a terminal, or that got one
	seen    map[string]bool // sessions in the previous poll
}

// ClientAgentsFile is the cell's agent allowlist as mounted into the
// workspace container of a session-hub cell, beside the token. Only id and
// displayName are used; outside hub cells it is absent.
const ClientAgentsFile = "/run/session-hub-client/agents.json"

// ReadClientAgents reads the agents an app should offer. A missing file
// means none; entries with an invalid id are skipped.
func ReadClientAgents(path string) ([]ahptypes.AgentInfo, error) {
	if path == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var entries []struct {
		ID          string `json:"id"`
		DisplayName string `json:"displayName"`
	}
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	out := []ahptypes.AgentInfo{}
	for _, e := range entries {
		if idPattern.MatchString(e.ID) {
			out = append(out, ahptypes.AgentInfo{Provider: e.ID, DisplayName: e.DisplayName})
		}
	}
	return out, nil
}

// herdrTemplateMarker marks templates this program owns; others are never
// overwritten or removed.
const herdrTemplateMarker = "generated-by: glovecell session-hub-herdr"

// herdrTerminalName is a stable, valid herdr-core terminal name for a
// session: the agent id and a hash of the session URI.
func herdrTerminalName(agent, session string) string {
	sum := sha256.Sum256([]byte(session))
	prefix := agent
	if len(prefix) > 19 {
		prefix = prefix[:19]
	}
	prefix = strings.TrimRight(prefix, "-")
	if prefix == "" {
		prefix = "hub"
	}
	return prefix + "-" + hex.EncodeToString(sum[:4])
}

// ChatCommand is the argv of a terminal that shows one hub session.
func ChatCommand(session string) []string {
	return []string{Binary, "session-hub-chat", "--session", session}
}

// Sync runs one round: templates, then terminals for new sessions. A
// session gets a terminal once it has been listed in two rounds, so a
// session-hub-chat that just created it has time to label its own terminal.
func (p *HerdrPanes) Sync(ctx context.Context) error {
	if p.handled == nil {
		p.handled, p.seen = map[string]bool{}, map[string]bool{}
	}
	if p.TemplatesDir != "" {
		agents, err := ReadClientAgents(p.AgentsFile)
		if err == nil {
			err = p.writeTemplates(agents)
		}
		if err != nil {
			log.Printf("session-hub-herdr: templates: %v", err)
		}
	}
	sessions, err := p.List(ctx)
	if err != nil {
		return err
	}
	now := map[string]bool{}
	var errs []error
	for _, s := range sessions {
		id := string(s.Resource)
		if !strings.HasPrefix(id, "ahp-session:/") || p.handled[id] {
			continue
		}
		now[id] = true
		if !p.seen[id] {
			continue
		}
		open, err := p.hasTerminal(ctx, id)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if !open {
			if err := p.open(ctx, s.Provider, id); err != nil {
				errs = append(errs, err)
				continue
			}
		}
		p.handled[id] = true
	}
	p.seen = now
	return errors.Join(errs...)
}

// hasTerminal reports whether any terminal, live or finished, carries the
// session's label.
func (p *HerdrPanes) hasTerminal(ctx context.Context, session string) (bool, error) {
	out, err := p.HC(ctx, "ls", "--all", "--json", "--label", HerdrSessionLabel+"="+session)
	if err != nil {
		return false, fmt.Errorf("hc ls: %w", err)
	}
	var list struct {
		Terminals []json.RawMessage `json:"terminals"`
	}
	if err := json.Unmarshal(out, &list); err != nil {
		return false, fmt.Errorf("hc ls: %w", err)
	}
	return len(list.Terminals) > 0, nil
}

func (p *HerdrPanes) open(ctx context.Context, agent, session string) error {
	args := []string{"run", "--bg", "--json", "--no-parent", "--no-notify",
		"--name", herdrTerminalName(agent, session),
		"--label", HerdrSessionLabel + "=" + session}
	if p.Cwd != "" {
		args = append(args, "--cwd", p.Cwd)
	}
	args = append(append(args, "--"), ChatCommand(session)...)
	if out, err := p.HC(ctx, args...); err != nil {
		if bytes.Contains(out, []byte("name_taken")) {
			return nil // a terminal of that name already shows it
		}
		return fmt.Errorf("hc run for %s: %w", session, err)
	}
	return nil
}

// herdrTemplate is the herdr-core template that starts a new hub chat with
// one agent. The body is empty, so `hc run <agent> "task"` sends just the
// task as the chat's first message.
func herdrTemplate(a ahptypes.AgentInfo, cwd string) string {
	name := oneLine(a.DisplayName, 40)
	if name == "" {
		name = a.Provider
	}
	command := Binary + " session-hub-chat --agent " + a.Provider + " --new"
	if cwd != "" {
		command += " --cwd " + cwd
	}
	return "---\n" +
		"description: New " + name + " chat in the cell's session hub, shared with the other apps\n" +
		"command: " + command + "\n" +
		herdrTemplateMarker + "\n" +
		"---\n"
}

func (p *HerdrPanes) writeTemplates(agents []ahptypes.AgentInfo) error {
	if err := os.MkdirAll(p.TemplatesDir, 0o755); err != nil {
		return err
	}
	want := map[string]string{}
	for _, a := range agents {
		if idPattern.MatchString(a.Provider) {
			want[a.Provider+".md"] = herdrTemplate(a, p.Cwd)
		}
	}
	entries, err := os.ReadDir(p.TemplatesDir)
	if err != nil {
		return err
	}
	ours := map[string]bool{}
	for _, e := range entries {
		raw, err := os.ReadFile(filepath.Join(p.TemplatesDir, e.Name()))
		if err == nil && bytes.Contains(raw, []byte(herdrTemplateMarker)) {
			ours[e.Name()] = true
		}
		if _, wanted := want[e.Name()]; err == nil && wanted && !ours[e.Name()] {
			log.Printf("session-hub-herdr: leaving the operator's own template %s alone", e.Name())
			delete(want, e.Name())
		}
	}
	var errs []error
	names := make([]string, 0, len(want))
	for n := range want {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		path := filepath.Join(p.TemplatesDir, n)
		if current, err := os.ReadFile(path); err == nil && string(current) == want[n] {
			continue
		}
		errs = append(errs, os.WriteFile(path, []byte(want[n]), 0o644))
	}
	for n := range ours {
		if _, keep := want[n]; !keep {
			errs = append(errs, os.Remove(filepath.Join(p.TemplatesDir, n)))
		}
	}
	return errors.Join(errs...)
}

// RunHerdrPanes polls the hub every interval until ctx ends. It waits for
// the hub and its token quietly: the hub may start after the worker.
func RunHerdrPanes(ctx context.Context, hubURL, tokenFile, agentsFile, templatesDir, cwd string, interval time.Duration) error {
	if err := loopbackHub(hubURL); err != nil {
		return fmt.Errorf("session-hub-herdr: %w", err)
	}
	var hub *ahp.Client
	list := func(ctx context.Context) ([]ahptypes.SessionSummary, error) {
		if hub != nil {
			select {
			case <-hub.Done():
				hub = nil
			default:
			}
		}
		if hub == nil {
			token, err := hubAccess(hubURL, tokenFile)
			if err != nil {
				return nil, err
			}
			client, _, err := dialHub(ctx, hubURL, token, "glovecell-herdr-panes-"+randomID())
			if err != nil {
				return nil, err
			}
			hub = client
		}
		sessions, err := listHubSessions(ctx, hub)
		if err != nil {
			_ = hub.Shutdown(ctx)
			hub = nil
			return nil, err
		}
		return sessions, nil
	}
	p := &HerdrPanes{List: list, AgentsFile: agentsFile, HC: runHC, TemplatesDir: templatesDir, Cwd: cwd}
	lastErr := ""
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		err := p.Sync(ctx)
		msg := ""
		if err != nil {
			msg = err.Error()
		}
		if msg != lastErr {
			if msg != "" {
				log.Printf("session-hub-herdr: %s (retrying)", msg)
			} else if lastErr != "" {
				log.Printf("session-hub-herdr: connected to the session hub")
			}
			lastErr = msg
		}
		select {
		case <-ctx.Done():
			if hub != nil {
				_ = hub.Shutdown(context.Background())
			}
			return nil
		case <-ticker.C:
		}
	}
}

func runHC(ctx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, "hc", args...)
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.Bytes(), err
}
