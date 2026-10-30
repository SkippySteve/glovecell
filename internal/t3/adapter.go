package t3

import (
	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/orchestrator"
)

// Managed is the T3 orchestrator adapter. Ensure, Suspend and Remove are the
// T3 Connect hooks the controller has always called; these methods only name
// the adapter for the orchestrator registry.
var _ orchestrator.Adapter = (*Managed)(nil)

// ControllerName is the OrchestratorClass controllerName for T3.
func (m *Managed) ControllerName() string { return api.OrchestratorT3 }

// RegistrationKind is the registrar client kind T3 desktop clients use.
func (m *Managed) RegistrationKind() string { return "t3" }

// ValidateClass accepts T3 classes without parameters. T3 Connect is
// configured on the controller (T3_CONNECT_*), not per class.
func (m *Managed) ValidateClass(class *api.OrchestratorClass) error {
	return orchestrator.NoParameters(class)
}
