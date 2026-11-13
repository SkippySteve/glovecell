package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"sort"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/registration"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Registrar is an adapter for orchestrators whose access today is a
// registration made by the control-host registrar (`agent-project-controller
// register --watch`, internal/registration): Orca and custom command clients.
//
// The registrar mounts a Ready workspace on the control host and registers the
// mount with the client; it revokes the registration when the workspace leaves
// Ready, and records each step in the ledger ConfigMap clients-<uid> in the
// control namespace. The controller grants nothing itself, so:
//
//   - Ensure grants nothing and does not wait for the registrar (it registers
//     only after the workspace is Ready, so waiting would deadlock);
//   - Suspend grants nothing to take back; leaving Ready is what makes the
//     registrar revoke;
//   - Remove returns an error until the ledger shows this orchestrator's
//     registrations revoked or absent, so removal waits for revocation.
type Registrar struct {
	// Name is the controllerName, for example api.OrchestratorOrca.
	Name string
	// Kind is the registrar client kind ("orca"), or "command" for custom
	// command clients, whose ledger records use their own kind names.
	Kind string
	// Client reads the ledger; Namespace is the control namespace.
	Client    client.Reader
	Namespace string
}

// NewOrca returns the legacy Orca adapter for the control-host registrar
// (desktop FUSE mounts). Prefer hub-and-spoke when HUBSPOKE_ORCA_ENABLED=true:
// internal/hubspoke.NewOrcaAccess, with orcad in each gVisor spoke and
// `agent-project-controller hubspoke --watch` on the hub (docs/hubspoke.md).
func NewOrca(c client.Reader, namespace string) *Registrar {
	return &Registrar{Name: api.OrchestratorOrca, Kind: "orca", Client: c, Namespace: namespace}
}

// NewCommand returns the adapter for custom command clients configured on
// the registrar with APC_CLIENT_<KIND>_REGISTER/REVOKE.
func NewCommand(c client.Reader, namespace string) *Registrar {
	return &Registrar{Name: api.OrchestratorCommand, Kind: "command", Client: c, Namespace: namespace}
}

func (a *Registrar) ControllerName() string { return a.Name }
func (a *Registrar) RegistrationKind() string {
	return a.Kind
}
func (a *Registrar) ValidateClass(class *api.OrchestratorClass) error { return NoParameters(class) }

// Ensure grants nothing: the registrar registers the workspace once it is
// Ready. TODO(#51): start and check the per-cell Orca runtime.
func (a *Registrar) Ensure(context.Context, *api.ProjectWorkspace) error { return nil }

// Suspend grants nothing to take back: the registrar revokes when the
// workspace leaves Ready. TODO(#51): stop the per-cell Orca runtime.
func (a *Registrar) Suspend(context.Context, *api.ProjectWorkspace) error { return nil }

// Remove waits until the registrar ledger shows no pending or registered
// client of this adapter's kind. A ledger it cannot read or trust blocks.
func (a *Registrar) Remove(ctx context.Context, w *api.ProjectWorkspace) error {
	if a.Client == nil || a.Namespace == "" {
		return errors.New("orchestrator registrar ledger unavailable; removal waits")
	}
	cm := &corev1.ConfigMap{}
	err := a.Client.Get(ctx, client.ObjectKey{Namespace: a.Namespace, Name: registration.ConfigMapName(string(w.UID))}, cm)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if cm.Labels[api.OwnerLabel] != string(w.UID) {
		return errors.New("client ledger ownership mismatch")
	}
	ledger, err := registration.DecodeConfigMapData(cm.Data)
	if err != nil {
		return err
	}
	kinds := make([]string, 0, len(ledger.Clients))
	for kind := range ledger.Clients {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	for _, kind := range kinds {
		rec := ledger.Clients[kind]
		if !a.matches(kind) && !a.matches(rec.Kind) {
			continue
		}
		switch rec.State {
		case registration.StateAbsent, registration.StateRevoked:
		case registration.StatePending, registration.StateRegistered:
			return fmt.Errorf("%s client %q is %s; waiting for the registrar to revoke it", a.Kind, kind, rec.State)
		default:
			return fmt.Errorf("%s client %q has unknown state %q", a.Kind, kind, rec.State)
		}
	}
	return nil
}

// matches reports whether a ledger kind belongs to this adapter. Command
// clients use their own kind names, so "command" matches every kind other
// than the built-in t3 and orca.
func (a *Registrar) matches(kind string) bool {
	if kind == "" {
		return false
	}
	if a.Kind == "command" {
		return kind != "t3" && kind != "orca"
	}
	return kind == a.Kind
}
