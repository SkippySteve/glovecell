package controller

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/sessionhub"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestOrcaSpokeResources(t *testing.T) {
	w := &api.ProjectWorkspace{ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: "uid"}, Spec: api.WorkspaceSpec{OrchestratorClassName: "orca"}}
	r := &Reconciler{Config: Config{OrcaHubSpoke: true, OrcaRuntimeImage: "img@sha256:abc", OrcaSpokePublicDomain: "orca.example", OrcaSpokeGatewayName: "traefik-gateway", OrcaSpokeGatewayNamespace: "traefik-system", OrcaSpokeGatewaySection: "websecure-cluster-aimingupward", OrcaHubNamespace: "agent-project-control-orca-hub"}}
	if !r.usesOrcaSpoke(w) {
		t.Fatal("expected orca spoke")
	}
	if r.orcaSpokeImage(w) != "img@sha256:abc" {
		t.Fatal(r.orcaSpokeImage(w))
	}
	if got := r.orcaSpokeDialAddress(w); got != "wss://demo.orca.example" {
		t.Fatal(got)
	}
	cmd := strings.Join(r.orcaSpokeCommand(w), " ")
	if !strings.Contains(cmd, "orcad") || !strings.Contains(cmd, "wss://demo.orca.example") {
		t.Fatal(cmd)
	}
	env := r.orcaSpokeEnv(w, nil)
	if len(env) != 2 || env[1].Name != "ORCA_PUBLIC_ADDRESS" || env[1].Value != "wss://demo.orca.example" {
		t.Fatalf("%+v", env)
	}
	if svc := r.orcaSpokeService(w); svc == nil || svc.Name != "orca-runtime" {
		t.Fatal(svc)
	}
	route := r.orcaSpokeRoute(w)
	if route == nil || route.GetName() != "orca-runtime" {
		t.Fatal(route)
	}
	spec, _ := route.Object["spec"].(map[string]any)
	hosts, _ := spec["hostnames"].([]any)
	if len(hosts) != 1 || hosts[0] != "demo.orca.example" {
		t.Fatal(hosts)
	}
	policy := r.orcaSpokeIngressPolicy(w)
	if policy == nil || policy.GetName() != "orca-runtime" {
		t.Fatal(policy)
	}
	r.Config.OrcaHubSpoke = false
	if r.usesOrcaSpoke(w) || r.orcaSpokeService(w) != nil || r.orcaSpokeRoute(w) != nil {
		t.Fatal("disabled spoke still active")
	}
}

func TestOrcaSpokeClusterLocalWithoutDomain(t *testing.T) {
	w := &api.ProjectWorkspace{ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: "uid"}, Spec: api.WorkspaceSpec{OrchestratorClassName: "orca"}}
	r := &Reconciler{Config: Config{OrcaHubSpoke: true, OrcaRuntimeImage: "img@sha256:abc"}}
	got := r.orcaSpokeDialAddress(w)
	if !strings.HasPrefix(got, "ws://orca-runtime.") || !strings.Contains(got, "svc.cluster.local") {
		t.Fatal(got)
	}
	if r.orcaSpokeRoute(w) != nil || r.orcaSpokeIngressPolicy(w) != nil {
		t.Fatal("public route without domain")
	}
}

func hubOrcaCell(hub bool) (*Reconciler, *api.ProjectWorkspace) {
	w := &api.ProjectWorkspace{ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: "uid"}, Spec: api.WorkspaceSpec{OrchestratorClassName: "orca"}}
	if hub {
		w.Spec.SessionHub = &api.SessionHubSpec{Enabled: true}
	}
	r := &Reconciler{Config: Config{OrcaHubSpoke: true, OrcaRuntimeImage: "img@sha256:abc", SessionHubEnabled: true, SessionHubImage: testHubImage,
		SessionHubAgents: append(append([]sessionhub.Agent{}, testHubAgents...), sessionhub.Agent{ID: "cell-claude", DisplayName: "Claude", Command: "/usr/local/bin/claude-acp"})}}
	return r, w
}

// Only an Orca cell created with the hub, on an installation with the hub on,
// gets the slot; every other cell's command is unchanged, and the command
// does not depend on the allowlist (a change there must not restart workers).
func TestOrcaHubSlotOnlyInHubCells(t *testing.T) {
	r, w := hubOrcaCell(true)
	cmd := strings.Join(r.orcaSpokeCommand(w), " ")
	if !r.usesOrcaHubSlot(w) || !strings.Contains(cmd, "session-hub-acp --agent") || !strings.Contains(cmd, "/workspace/home/.local/bin/omp") || !strings.Contains(cmd, orcaHubSlotAgents) {
		t.Fatalf("hub cell without its slot: %s", cmd)
	}
	if !strings.Contains(cmd, "exec \"$NODE\" /opt/orca/server/orcad.js") {
		t.Fatalf("slot replaced orcad: %s", cmd)
	}
	r.Config.SessionHubAgents = testHubAgents
	if got := strings.Join(r.orcaSpokeCommand(w), " "); got != cmd || strings.Contains(got, "cell-codex") {
		t.Fatal("the worker command follows the allowlist")
	}

	plain, pw := hubOrcaCell(false)
	want := strings.Join(plain.orcaSpokeCommand(pw), " ")
	plain.Config.SessionHubEnabled, plain.Config.SessionHubImage, plain.Config.SessionHubAgents = false, "", nil
	if got := strings.Join(plain.orcaSpokeCommand(pw), " "); got != want || strings.Contains(got, "omp") || strings.Contains(got, "session-hub") {
		t.Fatalf("a cell without the hub changed:\n%s\n%s", got, want)
	}

	off, ow := hubOrcaCell(true)
	off.Config.SessionHubEnabled = false
	if off.usesOrcaHubSlot(ow) || strings.Contains(strings.Join(off.orcaSpokeCommand(ow), " "), "omp") {
		t.Fatal("slot with the installation's hub off")
	}

	other, xw := hubOrcaCell(true)
	xw.Spec.OrchestratorClassName = "t3"
	if other.usesOrcaHubSlot(xw) {
		t.Fatal("slot outside an Orca cell")
	}
}

func runSh(t *testing.T, script string, args ...string) (string, string, int) {
	t.Helper()
	var out, errOut strings.Builder
	c := exec.Command("/bin/sh", append([]string{"-c", script, "sh"}, args...)...)
	c.Stdout, c.Stderr = &out, &errOut
	err := c.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return out.String(), errOut.String(), code
}

// The shim answers Orca's version probe, refuses the model listing and any
// non-chat use, and runs the connector for the allowlist's first agent
// without Orca's OMP arguments. Without a usable allowlist it refuses all.
func TestOrcaHubSlotScript(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "apc")
	called := filepath.Join(dir, "called")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nprintf '%s ' \"$@\" >"+called+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	agents := filepath.Join(dir, "agents.json")
	script := orcaHubSlotScript(fake, agents)
	if strings.Split(script, "\n")[1] != orcaHubSlotMarker {
		t.Fatal("marker must be the second line")
	}
	refused := func(args ...string) {
		t.Helper()
		if _, stderr, code := runSh(t, script, args...); code != 2 || !strings.Contains(stderr, "omp:") {
			t.Fatalf("%v: %d %q", args, code, stderr)
		}
		if _, err := os.Stat(called); err == nil {
			t.Fatalf("%v reached the connector", args)
		}
	}
	// No allowlist (outside a hub cell): Orca's probe fails, so no OMP chat.
	refused("--version")
	refused("acp")
	for _, bad := range []string{"", "[]", `[{"displayName":"x","id":"cell-codex"}]`, `[{"id":"Cell","displayName":"x"}]`, `[{"id":"` + strings.Repeat("a", 33) + `","displayName":"x"}]`} {
		os.WriteFile(agents, []byte(bad), 0o644)
		refused("acp")
	}

	// The allowlist exactly as the controller writes it into the cell's Secret.
	raw, _ := json.Marshal(append(append([]sessionhub.Agent{}, testHubAgents...), sessionhub.Agent{ID: "cell-claude", DisplayName: "Claude", Command: "/usr/local/bin/claude-acp", Args: []string{"--x"}}))
	os.WriteFile(agents, raw, 0o644)
	out, _, code := runSh(t, script, "--version")
	// Orca reads the first x.y.z on stdout (parseCliVersion) and needs OMP >= 17.0.5.
	if code != 0 || !regexp.MustCompile(`^17\.0\.5[^\w.+-]`).MatchString(out) {
		t.Fatalf("version %d %q", code, out)
	}
	for _, args := range [][]string{nil, {"foo"}, {"--help"}, {"acp", "--session-dir", "/tmp/x"}} {
		refused(args...)
	}
	if _, stderr, code := runSh(t, script, "acp", "--plugin-dir", "/tmp/visuals"); code != 0 {
		t.Fatalf("chat start %d %q", code, stderr)
	}
	got, _ := os.ReadFile(called)
	if string(got) != "session-hub-acp --agent cell-codex " {
		t.Fatalf("connector args %q", got)
	}
}

// Startup writes the shim, replaces its own earlier copy, leaves anyone
// else's omp alone, and never stops Orca from starting.
func TestOrcaHubSlotInstall(t *testing.T) {
	home := t.TempDir()
	slot := filepath.Join(home, ".local/bin/omp")
	first := orcaHubSlotScript("/bin/true", "/run/a.json")
	if _, stderr, code := runSh(t, orcaHubSlotInstall(home, first)); code != 0 || stderr != "" {
		t.Fatalf("install %d %q", code, stderr)
	}
	info, err := os.Stat(slot)
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatal(info, err)
	}
	if b, _ := os.ReadFile(slot); string(b) != first {
		t.Fatalf("content %q", b)
	}
	second := orcaHubSlotScript("/bin/true", "/run/b.json")
	runSh(t, orcaHubSlotInstall(home, second))
	if b, _ := os.ReadFile(slot); string(b) != second {
		t.Fatal("own shim not replaced")
	}
	if _, err := os.Stat(slot + ".tmp"); err == nil {
		t.Fatal("temporary file left")
	}

	foreign := "#!/bin/sh\necho real omp\n"
	os.WriteFile(slot, []byte(foreign), 0o755)
	if _, stderr, code := runSh(t, orcaHubSlotInstall(home, first)); code != 0 || !strings.Contains(stderr, "is not Glovecell's") {
		t.Fatalf("foreign %d %q", code, stderr)
	}
	if b, _ := os.ReadFile(slot); string(b) != foreign {
		t.Fatal("someone else's omp was replaced")
	}

	blocked := filepath.Join(t.TempDir(), "file")
	os.WriteFile(blocked, nil, 0o600)
	if _, stderr, code := runSh(t, orcaHubSlotInstall(blocked, first)+"echo started\n"); code != 0 || !strings.Contains(stderr, "Orca starts without it") {
		t.Fatalf("unwritable home %d %q", code, stderr)
	}
}

func TestShellSingleQuote(t *testing.T) {
	for _, s := range []string{"", "plain", "it's", "a'b'c", "$HOME `x` \"y\" \\n"} {
		out, _, code := runSh(t, "printf '%s' "+shellSingleQuote(s))
		if code != 0 || out != s {
			t.Fatalf("%q -> %q", s, out)
		}
	}
}
