// Package sessionhub wires the session hub (docs/session-hub.md) into a cell.
//
// The hub container runs AHP-ACP Gateway and keeps sessions; it never runs an
// agent itself. Each agent the gateway starts is `agent-project-controller
// session-hub-connect --agent <id>`, which asks the launcher container (built
// from the workspace image, with the workspace's mounts and environment) to
// start that agent over a unix socket and relays its standard input and output.
package sessionhub

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
	"unicode"
)

const (
	// Port and Path are the gateway's AHP listener inside the pod. The host is
	// always loopback; nothing outside the pod reaches it.
	Port = 8765
	Path = "/ahp"
	// SocketPath is the launcher's unix socket, in a volume only the hub and
	// launcher containers mount.
	SocketPath = "/run/session-hub/launcher.sock"
	// DatabasePath is the gateway's SQLite file.
	DatabasePath = "/var/lib/session-hub/gateway.db"
	// Binary is this program inside the hub and workspace images.
	Binary = "/usr/local/bin/agent-project-controller"
	// MaxAgents bounds the allowlist.
	MaxAgents = 16
)

// builtIn lists the gateway's bundled providers (ahp-acp-gateway.default.json
// at the pinned commit). Every one is disabled, and an allowlisted agent may
// not reuse an id: an override would inherit the built-in's fields, such as
// an npx command that downloads code at run time.
var builtIn = []string{"copilot", "claude", "gemini", "qwen", "auggie", "qoder", "codex", "opencode", "openclaw", "kiro", "hermes", "kimi", "pi"}

var idPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// Agent is one allowlisted agent: the ACP command the launcher runs in the
// workspace container. Command must be an absolute path; there is no shell.
type Agent struct {
	ID          string   `json:"id"`
	DisplayName string   `json:"displayName"`
	Command     string   `json:"command"`
	Args        []string `json:"args,omitempty"`
}

// ParseAgents reads and validates an allowlist (SESSION_HUB_AGENTS_JSON).
func ParseAgents(raw []byte) ([]Agent, error) {
	var agents []Agent
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&agents); err != nil {
		return nil, fmt.Errorf("session hub agents: %w", err)
	}
	if err := ValidateAgents(agents); err != nil {
		return nil, err
	}
	return agents, nil
}

// ValidateAgents refuses anything the launcher should not run.
func ValidateAgents(agents []Agent) error {
	if len(agents) == 0 {
		return errors.New("session hub agents: at least one agent required")
	}
	if len(agents) > MaxAgents {
		return fmt.Errorf("session hub agents: at most %d", MaxAgents)
	}
	seen := map[string]bool{}
	for _, a := range agents {
		if !idPattern.MatchString(a.ID) {
			return fmt.Errorf("session hub agent %q: id must match %s", a.ID, idPattern)
		}
		for _, b := range builtIn {
			if a.ID == b {
				return fmt.Errorf("session hub agent %q: id is a built-in gateway provider; use another, such as cell-%s", a.ID, a.ID)
			}
		}
		if seen[a.ID] {
			return fmt.Errorf("session hub agent %q: duplicate id", a.ID)
		}
		seen[a.ID] = true
		if a.DisplayName == "" || len(a.DisplayName) > 64 || !printable(a.DisplayName) {
			return fmt.Errorf("session hub agent %q: displayName must be 1-64 printable characters", a.ID)
		}
		if !path.IsAbs(a.Command) || path.Clean(a.Command) != a.Command || len(a.Command) > 256 || !printable(a.Command) {
			return fmt.Errorf("session hub agent %q: command must be a clean absolute path", a.ID)
		}
		if len(a.Args) > 16 {
			return fmt.Errorf("session hub agent %q: at most 16 args", a.ID)
		}
		for _, arg := range a.Args {
			if len(arg) > 256 || !printable(arg) {
				return fmt.Errorf("session hub agent %q: args must be printable and at most 256 characters", a.ID)
			}
		}
	}
	return nil
}

func printable(s string) bool {
	for _, r := range s {
		if !unicode.IsPrint(r) {
			return false
		}
	}
	return true
}

// ValidToken reports whether t is a 32-byte hex token.
func ValidToken(t string) bool {
	b, err := hex.DecodeString(t)
	return err == nil && len(b) == 32 && strings.ToLower(t) == t
}

// GatewayConfig renders the gateway's config overlay for one cell: loopback
// listener, the cell's token, every built-in provider off, and one provider
// per allowlisted agent that goes through the launcher.
func GatewayConfig(token string, agents []Agent) ([]byte, error) {
	if !ValidToken(token) {
		return nil, errors.New("session hub token must be 64 lowercase hex characters")
	}
	if err := ValidateAgents(agents); err != nil {
		return nil, err
	}
	type provider struct {
		ID          string   `json:"id"`
		Enabled     bool     `json:"enabled"`
		DisplayName string   `json:"displayName,omitempty"`
		Description string   `json:"description,omitempty"`
		Command     string   `json:"command,omitempty"`
		Args        []string `json:"args,omitempty"`
	}
	providers := []provider{}
	for _, id := range builtIn {
		providers = append(providers, provider{ID: id, Enabled: false})
	}
	for _, a := range agents {
		providers = append(providers, provider{
			ID: a.ID, Enabled: true, DisplayName: a.DisplayName,
			Description: "Runs in this cell's workspace.",
			Command:     Binary, Args: []string{"session-hub-connect", "--agent", a.ID},
		})
	}
	cfg := map[string]any{
		"version":   1,
		"listener":  map[string]any{"host": "127.0.0.1", "port": Port, "path": Path, "allowAnyOrigin": false, "allowedOrigins": []string{}},
		"auth":      map[string]any{"token": token},
		"database":  map[string]any{"path": DatabasePath},
		"providers": providers,
	}
	return json.MarshalIndent(cfg, "", "  ")
}
