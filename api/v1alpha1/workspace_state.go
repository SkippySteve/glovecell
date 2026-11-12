package v1alpha1

// DesiredState is what the operator asks of a workspace (spec.desiredState).
// The CRD allows the same three values and makes Removed final.
type DesiredState string

const (
	DesiredRunning DesiredState = "Running"
	DesiredPaused  DesiredState = "Paused"
	DesiredRemoved DesiredState = "Removed"
)

// Valid reports whether s is one of the three desired states.
func (s DesiredState) Valid() bool {
	switch s {
	case DesiredRunning, DesiredPaused, DesiredRemoved:
		return true
	}
	return false
}

// CanMoveTo reports whether an operator may ask for next while the desired
// state is s: Running and Paused swap freely (or stay), either may become
// Removed, and nothing leaves Removed, not even a repeat request. The CRD
// enforces the last rule too. Resuming a pause also needs the pause to have
// settled (Settled), which the console checks.
func (s DesiredState) CanMoveTo(next DesiredState) bool {
	return next.Valid() && s != DesiredRemoved
}

// Phase is the controller's view of a workspace (status.phase). The
// controller recomputes it on every reconcile from the desired state and
// what it observes; Phases lists every value it writes.
type Phase string

const (
	PhasePreparing Phase = "Preparing"
	PhaseReady     Phase = "Ready"
	PhasePausing   Phase = "Pausing"
	PhasePaused    Phase = "Paused"
	PhaseBlocked   Phase = "Blocked"
	PhaseRemoving  Phase = "Removing"
	PhaseRemoved   Phase = "Removed"
)

// Phases is every phase the controller writes. An object with an empty phase
// has not been reconciled yet.
var Phases = []Phase{PhasePreparing, PhaseReady, PhasePausing, PhasePaused, PhaseBlocked, PhaseRemoving, PhaseRemoved}

// Known reports whether p is one of Phases.
func (p Phase) Known() bool {
	for _, known := range Phases {
		if p == known {
			return true
		}
	}
	return false
}

// Leaving reports whether a workspace in phase p is on its way out of the
// cluster.
func (p Phase) Leaving() bool {
	return p == PhaseRemoving || p == PhaseRemoved
}

// Settled reports whether the controller has reconciled the workspace's
// current generation and found it in phase p.
func (w *ProjectWorkspace) Settled(p Phase) bool {
	return w.Status.ObservedGeneration == w.Generation && w.Status.Phase == p
}
