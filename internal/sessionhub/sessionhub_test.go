package sessionhub

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const testToken = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestValidateAgentsRefuses(t *testing.T) {
	ok := Agent{ID: "cell-codex", DisplayName: "Codex", Command: "/usr/local/bin/codex-acp"}
	cases := map[string][]Agent{
		"empty":         nil,
		"bad id":        {{ID: "Codex", DisplayName: "x", Command: "/bin/x"}},
		"built-in id":   {{ID: "claude", DisplayName: "x", Command: "/bin/x"}},
		"relative":      {{ID: "a", DisplayName: "x", Command: "codex-acp"}},
		"unclean path":  {{ID: "a", DisplayName: "x", Command: "/usr/../bin/sh"}},
		"no name":       {{ID: "a", Command: "/bin/x"}},
		"control chars": {{ID: "a", DisplayName: "x\n", Command: "/bin/x"}},
		"duplicate":     {ok, ok},
	}
	for name, agents := range cases {
		if err := ValidateAgents(agents); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := ValidateAgents([]Agent{ok}); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseAgents([]byte(`[{"id":"a","displayName":"A","command":"/bin/x","shell":true}]`)); err == nil {
		t.Error("unknown field accepted")
	}
}

func TestGatewayConfig(t *testing.T) {
	agents := []Agent{{ID: "cell-codex", DisplayName: "Codex", Command: "/usr/local/bin/codex-acp"}}
	if _, err := GatewayConfig("short", agents); err == nil {
		t.Error("bad token accepted")
	}
	if _, err := GatewayConfig(testToken, nil); err == nil {
		t.Error("no agents accepted")
	}
	raw, err := GatewayConfig(testToken, agents)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Listener struct {
			Host           string
			AllowAnyOrigin bool
		}
		Auth      struct{ Token string }
		Providers []struct {
			ID      string
			Enabled bool
			Command string
			Args    []string
		}
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Listener.Host != "127.0.0.1" || cfg.Listener.AllowAnyOrigin || cfg.Auth.Token != testToken {
		t.Fatalf("listener or token: %+v", cfg)
	}
	enabled := []string{}
	for _, p := range cfg.Providers {
		if p.Enabled {
			enabled = append(enabled, p.ID)
			if p.Command != Binary || strings.Join(p.Args, " ") != "session-hub-connect --agent cell-codex" {
				t.Errorf("provider %s does not go through the launcher: %s %v", p.ID, p.Command, p.Args)
			}
		}
	}
	if strings.Join(enabled, ",") != "cell-codex" {
		t.Errorf("enabled providers %v, want only cell-codex", enabled)
	}
	if len(cfg.Providers) != len(builtIn)+1 {
		t.Errorf("every built-in must be listed as disabled")
	}
}

func startLauncher(t *testing.T, agents []Agent, max int) (*Launcher, string) {
	t.Helper()
	dir := t.TempDir()
	raw, _ := json.Marshal(agents)
	file := filepath.Join(dir, "agents.json")
	if err := os.WriteFile(file, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	l := &Launcher{Socket: filepath.Join(dir, "l.sock"), AgentsFile: file, MaxSessions: max, Grace: 200 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- l.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-errc; err != nil {
			t.Error(err)
		}
	})
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(l.Socket); err == nil {
			return l, file
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("launcher did not listen")
	return nil, ""
}

func TestLauncherRelaysAllowedAgent(t *testing.T) {
	l, _ := startLauncher(t, []Agent{{ID: "echo", DisplayName: "Echo", Command: "/bin/cat"}}, 2)
	info, err := os.Stat(l.Socket)
	if err != nil || info.Mode().Perm() != 0o660 {
		t.Fatalf("socket mode %v, %v", info.Mode().Perm(), err)
	}
	var out bytes.Buffer
	if err := Connect(l.Socket, "echo", strings.NewReader("{\"jsonrpc\":\"2.0\"}\n"), &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != "{\"jsonrpc\":\"2.0\"}\n" {
		t.Fatalf("relayed %q", out.String())
	}
}

func TestLauncherRefusesUnknownAgent(t *testing.T) {
	l, _ := startLauncher(t, []Agent{{ID: "echo", DisplayName: "Echo", Command: "/bin/cat"}}, 2)
	err := Connect(l.Socket, "sh", strings.NewReader(""), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "not allowlisted") {
		t.Fatalf("unknown agent: %v", err)
	}
}

func TestLauncherRefusesOversizedRequest(t *testing.T) {
	l, _ := startLauncher(t, []Agent{{ID: "echo", DisplayName: "Echo", Command: "/bin/cat"}}, 2)
	conn, err := net.Dial("unix", l.Socket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte(strings.Repeat("x", 600) + "\n")); err != nil {
		t.Fatal(err)
	}
	line, _ := bufio.NewReader(conn).ReadString('\n')
	if !strings.Contains(line, "error") {
		t.Fatalf("oversized request answered %q", line)
	}
}

// pidOf starts agent "sleep", which prints its pid and then ignores stdin.
func sleeper() Agent {
	return Agent{ID: "sleep", DisplayName: "Sleep", Command: "/bin/sh", Args: []string{"-c", "echo $$; exec sleep 60"}}
}

func dialAgent(t *testing.T, socket, agent string) (net.Conn, int) {
	t.Helper()
	conn, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(request{Agent: agent})
	conn.Write(append(b, '\n'))
	br := bufio.NewReader(conn)
	ack, _ := br.ReadString('\n')
	if !strings.Contains(ack, `"ok":true`) {
		t.Fatalf("agent refused: %q", ack)
	}
	line, _ := br.ReadString('\n')
	pid, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		t.Fatalf("pid line %q", line)
	}
	return conn, pid
}

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

// Interrupted operation: the hub side disappears mid-session. The agent must
// not outlive it by more than the grace period.
func TestLauncherKillsAgentWhenClientGoes(t *testing.T) {
	l, _ := startLauncher(t, []Agent{sleeper()}, 2)
	conn, pid := dialAgent(t, l.Socket, "sleep")
	if !alive(pid) {
		t.Fatal("agent not running")
	}
	conn.Close()
	deadline := time.Now().Add(3 * time.Second)
	for alive(pid) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if alive(pid) {
		syscall.Kill(pid, syscall.SIGKILL)
		t.Fatal("agent outlived its client")
	}
}

func TestLauncherBoundsSessions(t *testing.T) {
	l, _ := startLauncher(t, []Agent{sleeper()}, 1)
	conn, _ := dialAgent(t, l.Socket, "sleep")
	defer conn.Close()
	err := Connect(l.Socket, "sleep", strings.NewReader(""), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "too many") {
		t.Fatalf("second agent: %v", err)
	}
}

// An allowlist change in the cell's Secret applies to the next connection.
func TestLauncherRereadsAllowlist(t *testing.T) {
	l, file := startLauncher(t, []Agent{{ID: "echo", DisplayName: "Echo", Command: "/bin/cat"}}, 2)
	if err := os.WriteFile(file, []byte(`[{"id":"other","displayName":"Other","command":"/bin/cat"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Connect(l.Socket, "echo", strings.NewReader(""), io.Discard); err == nil {
		t.Fatal("removed agent still started")
	}
	os.WriteFile(file, []byte(`not json`), 0o600)
	if err := Connect(l.Socket, "other", strings.NewReader(""), io.Discard); err == nil || !strings.Contains(err.Error(), "allowlist unavailable") {
		t.Fatalf("broken allowlist must fail closed: %v", err)
	}
}

func TestConnectWithoutLauncher(t *testing.T) {
	err := Connect(filepath.Join(t.TempDir(), "none.sock"), "echo", strings.NewReader(""), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("missing launcher: %v", err)
	}
}
