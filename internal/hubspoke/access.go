package hubspoke

import (
	"context"
	"errors"
	"fmt"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/orchestrator"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Access is the orchestrator adapter for hub-and-spoke runtimes. The hub
// process (`agent-project-controller hubspoke --watch`) enrolls spokes; this
// adapter blocks removal until the hub has revoked.
type Access struct {
	Kind      string // "orca" today; "t3" later
	Name      string // controllerName
	Client    client.Reader
	Namespace string
	// Enabled turns on hub-spoke waits. When false, Ensure is a no-op and
	// Remove does not wait (legacy desktop registrar path).
	Enabled bool
}

var _ orchestrator.Adapter = (*Access)(nil)

// NewOrcaAccess returns the Orca hub-spoke adapter.
func NewOrcaAccess(c client.Reader, namespace string, enabled bool) *Access {
	return &Access{
		Kind:      "orca",
		Name:      api.OrchestratorOrca,
		Client:    c,
		Namespace: namespace,
		Enabled:   enabled,
	}
}

func (a *Access) ControllerName() string   { return a.Name }
func (a *Access) RegistrationKind() string { return a.Kind }
func (a *Access) ValidateClass(class *api.OrchestratorClass) error {
	return orchestrator.NoParameters(class)
}

// Ensure grants nothing in-cluster. The hub process enrolls the spoke after
// the worker is up; waiting here for enrolled would deadlock (the hub needs a
// Ready workspace, and Ready waits on Ensure). Phone reachability is visible
// on the hubspoke ledger and in hub logs.
func (a *Access) Ensure(context.Context, *api.ProjectWorkspace) error { return nil }

// Suspend leaves the spoke enrolled so Pause can resume without a new phone
// scan. The hub disconnects live sessions when the worker pod stops.
func (a *Access) Suspend(context.Context, *api.ProjectWorkspace) error { return nil }

// Remove waits until the hub has revoked the spoke enrollment.
func (a *Access) Remove(ctx context.Context, w *api.ProjectWorkspace) error {
	if !a.Enabled {
		return nil
	}
	if a.Client == nil || a.Namespace == "" {
		return errors.New("hubspoke ledger unavailable; removal waits")
	}
	rec, err := GetState(ctx, a.Client, a.Namespace, string(w.UID), a.Kind)
	if err != nil {
		return err
	}
	switch rec.State {
	case StateAbsent, StateRevoked:
		return nil
	case StatePending, StateEnrolled:
		return fmt.Errorf("hubspoke %s is %s; waiting for the hub to revoke it", a.Kind, rec.State)
	default:
		return fmt.Errorf("hubspoke %s has unknown state %q", a.Kind, rec.State)
	}
}
