package orchestrator

import (
	"context"
	"fmt"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/herdr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Herdr is the projects.agentwork.dev/herdr adapter. Access is the worker's
// herdr-core (`hc server`) plus an operator-readable ConfigMap with the hub
// `--via` recipe. Phone and PC reach the cell through a hub herdr-core; this
// adapter does not open SSH or Traefik routes (docs/herdr.md).
type Herdr struct {
	Client    client.Client
	Namespace string
}

// NewHerdr returns the herdr-core adapter.
func NewHerdr(c client.Client, namespace string) *Herdr {
	return &Herdr{Client: c, Namespace: namespace}
}

func (a *Herdr) ControllerName() string   { return api.OrchestratorHerdr }
func (a *Herdr) RegistrationKind() string { return "" }
func (a *Herdr) ValidateClass(class *api.OrchestratorClass) error {
	return NoParameters(class)
}

func (a *Herdr) Ensure(ctx context.Context, w *api.ProjectWorkspace) error {
	if a == nil || a.Client == nil || a.Namespace == "" || w == nil {
		return fmt.Errorf("herdr adapter unavailable")
	}
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: a.Namespace,
			Name:      herdrMachineConfigMapName(w),
			Labels: map[string]string{
				api.OwnerLabel:                 string(w.UID),
				"projects.agentwork.dev/herdr": "machine",
			},
		},
		Data: map[string]string{
			"workspace": w.Name,
			"via":       herdr.MachineViaCommand(w.Name),
			"connect":   herdr.DefaultConnect,
			"note":      "On the hub: hc machine add --name <cell> --via '" + herdr.MachineViaCommand(w.Name) + "'. Android SSHs to the hub herdr-core over LAN/WireGuard; PC Herdr GUI uses the same hub.",
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
	existing.Labels = cm.Labels
	existing.Data = cm.Data
	return a.Client.Update(ctx, existing)
}

func (a *Herdr) Suspend(ctx context.Context, w *api.ProjectWorkspace) error {
	return a.deleteMachine(ctx, w)
}

func (a *Herdr) Remove(ctx context.Context, w *api.ProjectWorkspace) error {
	return a.deleteMachine(ctx, w)
}

func (a *Herdr) deleteMachine(ctx context.Context, w *api.ProjectWorkspace) error {
	if a == nil || a.Client == nil || a.Namespace == "" || w == nil {
		return nil
	}
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: a.Namespace, Name: herdrMachineConfigMapName(w)}}
	err := a.Client.Delete(ctx, cm)
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

func herdrMachineConfigMapName(w *api.ProjectWorkspace) string {
	return "herdr-machine-" + string(w.UID)
}
