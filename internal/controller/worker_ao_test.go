package controller

import (
	"context"
	"strings"
	"testing"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	ao "git.cluster.aimingupward.com/steve/agent-project-controller/internal/agentorchestrator"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const aoTestImage = "registry.example.com/ao@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func aoReconciler() *Reconciler {
	return &Reconciler{Config: Config{Image: "registry.example.com/ws@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", AgentOrchestratorEnabled: true, AgentOrchestratorImage: aoTestImage, AgentOrchestratorClass: "ao", AgentOrchestratorDomain: "ao.example", AgentOrchestratorGatewayName: "gw", AgentOrchestratorGatewayNamespace: "traefik-system", AgentOrchestratorGatewaySection: "lan"}}
}

func TestAgentOrchestratorWorkerResources(t *testing.T) {
	w := &api.ProjectWorkspace{ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: "uid"}, Spec: api.WorkspaceSpec{OrchestratorClassName: "ao"}}
	r := aoReconciler()
	if !r.usesAgentOrchestrator(w) || !r.usesWorkerRuntime(w) {
		t.Fatal("expected AO worker")
	}
	if r.workerImage(w) != aoTestImage {
		t.Fatal(r.workerImage(w))
	}
	cmd := strings.Join(r.workerCommand(w), " ")
	for _, want := range []string{"/opt/ao/resources/daemon/ao", "daemon", "remote-host enable >/dev/null", "require-device-credentials on >/dev/null", "AO_DATA_DIR=/workspace/ao"} {
		if !strings.Contains(cmd, want) {
			t.Fatalf("command lacks %q: %s", want, cmd)
		}
	}
	if strings.Contains(cmd, "--tunnel") || strings.Contains(cmd, " link") {
		t.Fatalf("AO must not use its tunnel or account link: %s", cmd)
	}
	env := r.agentOrchestratorEnv(w, nil)
	if len(env) != 2 || env[0].Name != "AO_DATA_DIR" || env[1].Name != "AO_PORT" {
		t.Fatalf("%+v", env)
	}
	svc := r.agentOrchestratorService(w)
	if svc == nil || svc.Name != "agent-orchestrator" || len(svc.Spec.Ports) != 1 || svc.Spec.Ports[0].Port != 3011 {
		t.Fatalf("%+v", svc)
	}
	route := r.agentOrchestratorRoute(w)
	if route == nil || route.GetName() != "agent-orchestrator" {
		t.Fatal(route)
	}
	spec, _ := route.Object["spec"].(map[string]any)
	if hosts, _ := spec["hostnames"].([]any); len(hosts) != 1 || hosts[0] != "demo.ao.example" {
		t.Fatal(hosts)
	}
	policy := r.agentOrchestratorIngressPolicy(w)
	if policy == nil || policy.GetName() != "agent-orchestrator" {
		t.Fatal(policy)
	}
	pspec, _ := policy.Object["spec"].(map[string]any)
	ingress, _ := pspec["ingress"].([]any)
	if len(ingress) != 1 {
		t.Fatalf("one ingress rule (Traefik) expected: %+v", ingress)
	}
	rule := ingress[0].(map[string]any)
	ports := rule["toPorts"].([]any)[0].(map[string]any)["ports"].([]any)
	if len(ports) != 1 || ports[0].(map[string]any)["port"] != "3011" {
		t.Fatalf("only the remote listener may be opened: %+v", ports)
	}
}

func TestAgentOrchestratorInertUnlessEnabledAndNamed(t *testing.T) {
	other := &api.ProjectWorkspace{ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: "uid"}, Spec: api.WorkspaceSpec{OrchestratorClassName: "orca"}}
	r := aoReconciler()
	if r.usesAgentOrchestrator(other) || r.agentOrchestratorService(other) != nil || r.agentOrchestratorRoute(other) != nil || r.agentOrchestratorIngressPolicy(other) != nil {
		t.Fatal("AO resources for another class")
	}
	if r.workerImage(other) != r.Config.Image {
		t.Fatal("other class lost the shared workspace image")
	}
	w := &api.ProjectWorkspace{ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: "uid"}, Spec: api.WorkspaceSpec{OrchestratorClassName: "ao"}}
	r.Config.AgentOrchestratorEnabled = false
	if r.usesAgentOrchestrator(w) || r.agentOrchestratorService(w) != nil || r.workerImage(w) != r.Config.Image {
		t.Fatal("disabled AO still active")
	}
	r = aoReconciler()
	r.Config.AgentOrchestratorDomain = ""
	if r.agentOrchestratorService(w) == nil {
		t.Fatal("service expected without domain")
	}
	if r.agentOrchestratorRoute(w) != nil || r.agentOrchestratorIngressPolicy(w) != nil {
		t.Fatal("published without a domain")
	}
}

func TestAgentOrchestratorConfigValidation(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*Config)
		ok   bool
	}{
		{"valid", func(*Config) {}, true},
		{"disabled ignores image", func(c *Config) { c.AgentOrchestratorEnabled = false; c.AgentOrchestratorImage = "" }, true},
		{"tag image", func(c *Config) { c.AgentOrchestratorImage = "registry.example.com/ao:latest" }, false},
		{"bad class", func(c *Config) { c.AgentOrchestratorClass = "Not_A_Name" }, false},
		{"domain with scheme", func(c *Config) { c.AgentOrchestratorDomain = "https://ao.example" }, false},
		{"domain without gateway", func(c *Config) { c.AgentOrchestratorGatewaySection = "" }, false},
		{"no domain", func(c *Config) { c.AgentOrchestratorDomain = ""; c.AgentOrchestratorGatewayName = "" }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := aoReconciler().Config
			tc.edit(&c)
			if err := c.validateAgentOrchestrator(); (err == nil) != tc.ok {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestAgentOrchestratorUnpromotedPlaceholder(t *testing.T) {
	for image, want := range map[string]bool{
		"registry.example.com/image-publisher/glovecell-ao-workspace:unpromoted": true,
		aoTestImage:                      false,
		"registry.example.com/ao:latest": false,
		"":                               false,
	} {
		if got := AgentOrchestratorUnpromoted(image); got != want {
			t.Fatalf("%q: got %v", image, got)
		}
	}
}

// An AO cell created with a session hub starts the daemon with the slot
// prelude, which reads the hub's allowlist inside the container; nothing about
// slots is in the pod spec, so an allowlist change never counts as drift.
// AO cells without a hub keep today's command.
func TestAgentOrchestratorSessionHubSlots(t *testing.T) {
	ctx := context.Background()
	for _, hub := range []bool{true, false} {
		r, w, _ := fixture(t)
		hubOn(r, w)
		if !hub {
			w.Spec.SessionHub = nil
		}
		r.Config.AgentOrchestratorEnabled = true
		r.Config.AgentOrchestratorImage = aoTestImage
		r.Config.AgentOrchestratorClass = "ao"
		w.Spec.OrchestratorClassName = "ao"
		if err := r.ensure(ctx, w); err != nil {
			t.Fatal(err)
		}
		spec := workerPodSpec(t, r, namespace(w))
		want := ao.WorkerCommand()
		if hub {
			want = ao.HubWorkerCommand()
		}
		if got := container(spec, "workspace").Command; strings.Join(got, "\x00") != strings.Join(want, "\x00") {
			t.Fatalf("hub=%v command: %v", hub, got)
		}
		for _, c := range append(append(spec.InitContainers[:0:0], spec.InitContainers...), spec.Containers...) {
			for _, e := range c.Env {
				if strings.HasPrefix(e.Name, "GLOVECELL_AO_HUB_") {
					t.Fatalf("%s has %s in the pod spec", c.Name, e.Name)
				}
			}
		}
	}
}
