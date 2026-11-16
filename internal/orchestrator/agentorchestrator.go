package orchestrator

import (
	"context"
	"fmt"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	ao "git.cluster.aimingupward.com/steve/agent-project-controller/internal/agentorchestrator"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// AgentOrchestrator is the projects.agentwork.dev/agent-orchestrator adapter.
// Access is the worker's AO daemon, published by the controller as a
// per-cell Service, HTTPRoute and ingress policy (worker_ao.go). This adapter
// keeps an operator-readable ConfigMap with the cell's device address while
// the cell is Ready. Devices pair with one-time codes from the console; AO
// device tokens live only in the cell's volume, so removing the cell revokes
// them and nothing outside the cluster needs revoking.
type AgentOrchestrator struct {
	Client    client.Client
	Namespace string
	// Domain is AGENT_ORCHESTRATOR_DOMAIN; empty means no device address.
	Domain string
}

// NewAgentOrchestrator returns the Agent Orchestrator adapter.
func NewAgentOrchestrator(c client.Client, namespace, domain string) *AgentOrchestrator {
	return &AgentOrchestrator{Client: c, Namespace: namespace, Domain: domain}
}

func (a *AgentOrchestrator) ControllerName() string {
	return api.OrchestratorAgentOrchestrator
}
func (a *AgentOrchestrator) RegistrationKind() string { return "" }
func (a *AgentOrchestrator) ValidateClass(class *api.OrchestratorClass) error {
	return NoParameters(class)
}

func (a *AgentOrchestrator) Ensure(ctx context.Context, w *api.ProjectWorkspace) error {
	if a == nil || a.Client == nil || a.Namespace == "" || w == nil {
		return fmt.Errorf("agent-orchestrator adapter unavailable")
	}
	address := ao.Address(w.Name, a.Domain)
	note := "Pair a device: console POST /api/v1/workspaces/" + w.Name + "/ao-pairing returns a one-time code (desktop: Settings > Remote hosts > Add host; phone: open the aomobile:// link or enter the address and code)."
	if address == "" {
		note = "AGENT_ORCHESTRATOR_DOMAIN is unset: the daemon runs but no device can reach it."
	}
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: a.Namespace,
			Name:      agentOrchestratorConfigMapName(w),
			Labels: map[string]string{
				api.OwnerLabel: string(w.UID),
				"projects.agentwork.dev/agent-orchestrator": "host",
			},
		},
		Data: map[string]string{
			"workspace": w.Name,
			"address":   address,
			"note":      note,
		},
	}
	existing := &corev1.ConfigMap{}
	err := a.Client.Get(ctx, client.ObjectKey{Namespace: cm.Namespace, Name: cm.Name}, existing)
	if apierrors.IsNotFound(err) {
		return a.Client.Create(ctx, cm)
	}
	if err != nil {
		return err
	}
	if existing.Labels[api.OwnerLabel] != string(w.UID) {
		return fmt.Errorf("ConfigMap %s/%s is not owned by this workspace", cm.Namespace, cm.Name)
	}
	existing.Labels = cm.Labels
	existing.Data = cm.Data
	return a.Client.Update(ctx, existing)
}

func (a *AgentOrchestrator) Suspend(ctx context.Context, w *api.ProjectWorkspace) error {
	return a.deleteHost(ctx, w)
}

func (a *AgentOrchestrator) Remove(ctx context.Context, w *api.ProjectWorkspace) error {
	return a.deleteHost(ctx, w)
}

func (a *AgentOrchestrator) deleteHost(ctx context.Context, w *api.ProjectWorkspace) error {
	if a == nil || a.Client == nil || a.Namespace == "" || w == nil {
		return nil
	}
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: a.Namespace, Name: agentOrchestratorConfigMapName(w)}}
	err := a.Client.Delete(ctx, cm)
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

func agentOrchestratorConfigMapName(w *api.ProjectWorkspace) string {
	return "ao-host-" + string(w.UID)
}
