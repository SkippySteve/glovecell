package controller

import (
	"strings"
	"testing"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
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
