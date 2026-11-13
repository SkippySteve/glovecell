// Package orchestrator connects agent orchestrators (T3, Orca, custom
// command clients and later others) to project workspaces.
//
// An OrchestratorClass (cluster-scoped, like GatewayClass) names an adapter by
// spec.controllerName. A ProjectWorkspace selects a class with
// spec.orchestratorClassName. The controller looks the adapter up in a
// Registry and drives it through Access: Ensure while the project is ready,
// Suspend when it is not, Remove before its storage is deleted.
//
// Resolution fails closed. A missing class, a controllerName without an
// installed adapter, or parameters the adapter does not accept all return a
// ClassError, and the controller grants nothing for that workspace.
package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Access owns externally registered orchestrator access for one workspace.
// It is the hook the controller has always called for T3 Connect
// (controller.ProjectAccess is an alias of it).
//
//   - Ensure runs while the project sandbox is ready. An error keeps the
//     workspace out of Ready and is retried.
//   - Suspend runs whenever the project is not ready, paused or refused. It
//     must stop access the orchestrator holds and be cheap when there is none.
//   - Remove runs before the project namespace and storage are deleted. It
//     must return an error until external revocation is verified; the
//     controller keeps the finalizer until it returns nil.
type Access interface {
	Ensure(context.Context, *api.ProjectWorkspace) error
	Suspend(context.Context, *api.ProjectWorkspace) error
	Remove(context.Context, *api.ProjectWorkspace) error
}

// Adapter is one orchestrator implementation. Adding an orchestrator is a new
// Adapter registered under its own controllerName plus an OrchestratorClass
// that names it; the controller core does not change.
type Adapter interface {
	Access
	// ControllerName is the OrchestratorClass spec.controllerName this
	// adapter serves, for example "projects.agentwork.dev/t3".
	ControllerName() string
	// RegistrationKind is the control-host registrar client kind
	// (internal/registration) the orchestrator registers checkouts with:
	// "t3", "orca" or a custom command kind. Empty when it uses none.
	RegistrationKind() string
	// ValidateClass reports whether the adapter accepts the class, mainly its
	// parameters. An error leaves the class not Accepted and denies every
	// workspace that uses it.
	ValidateClass(*api.OrchestratorClass) error
}

// Condition reasons for OrchestratorClass Accepted and for workspaces whose
// class cannot be used.
const (
	ReasonAccepted              = "Accepted"
	ReasonClassNotFound         = "OrchestratorClassNotFound"
	ReasonUnsupportedController = "UnsupportedController"
	ReasonInvalidParameters     = "InvalidParameters"
	ReasonClassUnavailable      = "OrchestratorClassUnavailable"
)

// ClassError explains why a workspace's orchestrator class cannot be used.
// Reason is a condition reason; the error text is the condition message.
type ClassError struct {
	Class   string
	Reason  string
	Message string
}

func (e *ClassError) Error() string { return e.Message }

var controllerNamePattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*/[A-Za-z0-9/\-._~%!$&'()*+,;=:]+$`)

// ValidControllerName applies the GatewayClass controllerName rule: a domain
// prefix, a slash and a path, at most 253 characters.
func ValidControllerName(name string) bool {
	return len(name) <= 253 && controllerNamePattern.MatchString(name)
}

// Owned reports whether this controller owns the class's controllerName.
func Owned(controllerName string) bool {
	return strings.HasPrefix(controllerName, api.OrchestratorControllerPrefix)
}

// Registry holds the adapters this controller process has installed, keyed
// by controllerName.
type Registry struct {
	mu       sync.RWMutex
	adapters map[string]Adapter
}

// NewRegistry returns a registry holding adapters.
func NewRegistry(adapters ...Adapter) (*Registry, error) {
	r := &Registry{}
	for _, a := range adapters {
		if err := r.Register(a); err != nil {
			return nil, err
		}
	}
	return r, nil
}

// Register installs an adapter. Names must be valid and unique: a second
// adapter for one controllerName is refused rather than replacing the first.
func (r *Registry) Register(a Adapter) error {
	if a == nil {
		return errors.New("orchestrator adapter required")
	}
	name := a.ControllerName()
	if !ValidControllerName(name) {
		return fmt.Errorf("invalid orchestrator controllerName %q", name)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.adapters == nil {
		r.adapters = map[string]Adapter{}
	}
	if _, ok := r.adapters[name]; ok {
		return fmt.Errorf("orchestrator controllerName %q already registered", name)
	}
	r.adapters[name] = a
	return nil
}

// Lookup returns the adapter for controllerName.
func (r *Registry) Lookup(controllerName string) (Adapter, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.adapters[controllerName]
	return a, ok
}

// Adapters returns every installed adapter, ordered by controllerName.
func (r *Registry) Adapters() []Adapter {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Adapter, 0, len(r.adapters))
	for _, a := range r.adapters {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ControllerName() < out[j].ControllerName() })
	return out
}

// Accept checks a class against the installed adapters. It returns the
// adapter, or a ClassError with the reason the class is not usable.
func (r *Registry) Accept(class *api.OrchestratorClass) (Adapter, error) {
	name := class.Spec.ControllerName
	if class.Spec.Parameters != nil && class.Spec.ParametersRef != nil {
		return nil, &ClassError{Class: class.Name, Reason: ReasonInvalidParameters, Message: fmt.Sprintf("OrchestratorClass %q sets both parameters and parametersRef", class.Name)}
	}
	a, ok := r.Lookup(name)
	if !ok {
		return nil, &ClassError{Class: class.Name, Reason: ReasonUnsupportedController, Message: fmt.Sprintf("OrchestratorClass %q names controller %q, which this controller does not run; no orchestrator access is granted", class.Name, name)}
	}
	if err := a.ValidateClass(class); err != nil {
		return nil, &ClassError{Class: class.Name, Reason: ReasonInvalidParameters, Message: fmt.Sprintf("OrchestratorClass %q: %v; no orchestrator access is granted", class.Name, err)}
	}
	return a, nil
}

// AcceptedCondition is the Accepted condition for a class this controller owns.
func (r *Registry) AcceptedCondition(class *api.OrchestratorClass) metav1.Condition {
	c := metav1.Condition{Type: api.OrchestratorClassAccepted, Status: metav1.ConditionTrue, Reason: ReasonAccepted, Message: "Adapter installed and class parameters accepted", ObservedGeneration: class.Generation}
	if _, err := r.Accept(class); err != nil {
		var ce *ClassError
		errors.As(err, &ce)
		c.Status, c.Reason, c.Message = metav1.ConditionFalse, ce.Reason, ce.Message
	}
	return c
}

// Resolve returns the Access for a workspace.
//
//   - An empty spec.orchestratorClassName means the deployment default, def.
//     That is how existing objects keep working: with T3 Connect enabled the
//     default is the T3 adapter (the default "t3" class by translation); with
//     it disabled the default is nil, meaning no orchestrator.
//   - A named class must exist, name an installed adapter and pass its
//     validation. Anything else is a ClassError and no access is granted.
func Resolve(ctx context.Context, c client.Reader, r *Registry, def Access, w *api.ProjectWorkspace) (Access, error) {
	name := w.Spec.OrchestratorClassName
	if name == "" {
		return def, nil
	}
	class := &api.OrchestratorClass{}
	if err := c.Get(ctx, client.ObjectKey{Name: name}, class); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, &ClassError{Class: name, Reason: ReasonClassNotFound, Message: fmt.Sprintf("OrchestratorClass %q not found; no orchestrator access is granted", name)}
		}
		return nil, &ClassError{Class: name, Reason: ReasonClassUnavailable, Message: fmt.Sprintf("OrchestratorClass %q could not be read: %v", name, err)}
	}
	return r.Accept(class)
}

// NoParameters is ValidateClass for adapters that take no parameters.
func NoParameters(class *api.OrchestratorClass) error {
	if class.Spec.Parameters != nil || class.Spec.ParametersRef != nil {
		return errors.New("this orchestrator takes no parameters")
	}
	return nil
}

// Wording returns the Ready condition reason and message the controller uses
// while an orchestrator is pending and once it is ready. T3 keeps the wording
// it always had.
func Wording(a Access) (pendingReason, readyMessage string) {
	if named, ok := a.(interface{ ControllerName() string }); ok && named.ControllerName() != api.OrchestratorT3 {
		return "OrchestratorPending", "Project sandbox and orchestrator are ready"
	}
	return "T3ConnectPending", "Project sandbox and T3 Connect environment are ready"
}
