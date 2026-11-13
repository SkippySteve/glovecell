package v1alpha1

import (
	"encoding/json"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// OrchestratorControllerPrefix is the controllerName prefix this controller
// owns. Classes with another prefix belong to someone else and are ignored,
// the way a Gateway controller ignores GatewayClasses it does not own.
const OrchestratorControllerPrefix = "projects.agentwork.dev/"

// Built-in orchestrator controller names.
const (
	OrchestratorT3      = OrchestratorControllerPrefix + "t3"
	OrchestratorOrca    = OrchestratorControllerPrefix + "orca"
	OrchestratorCommand = OrchestratorControllerPrefix + "command"
	// OrchestratorHerdr is herdr-core in the worker, reached from a hub via
	// `agent-project-controller herdr-via` (docs/herdr.md). Not the Herdr GUI.
	OrchestratorHerdr = OrchestratorControllerPrefix + "herdr"
)

// OrchestratorClassAccepted is the condition type the owning controller sets
// on an OrchestratorClass: True when an adapter for spec.controllerName is
// installed and accepts the class parameters.
const OrchestratorClassAccepted = "Accepted"

// OrchestratorClass names an agent orchestrator (T3, Orca, a custom command
// client) and the controller adapter that implements it. It is
// cluster-scoped, like GatewayClass. A ProjectWorkspace selects one with
// spec.orchestratorClassName.
//
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:subresource:status
type OrchestratorClass struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              OrchestratorClassSpec   `json:"spec"`
	Status            OrchestratorClassStatus `json:"status,omitempty"`
}

type OrchestratorClassSpec struct {
	// ControllerName selects the adapter, for example
	// "projects.agentwork.dev/t3". Immutable.
	ControllerName string `json:"controllerName"`
	// ParametersRef points at adapter-specific configuration. Adapters that
	// take no parameters refuse a class that sets it (fail closed).
	ParametersRef *OrchestratorParametersReference `json:"parametersRef,omitempty"`
	// Parameters is inline adapter-specific configuration. Same rule as
	// ParametersRef; at most one of the two may be set.
	Parameters *apiextensionsv1.JSON `json:"parameters,omitempty"`
}

// OrchestratorParametersReference names an object holding class parameters.
type OrchestratorParametersReference struct {
	Group     string `json:"group"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
}

type OrchestratorClassStatus struct {
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

type OrchestratorClassList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []OrchestratorClass `json:"items"`
}

func (c *OrchestratorClass) DeepCopyObject() runtime.Object {
	out := new(OrchestratorClass)
	b, _ := json.Marshal(c)
	_ = json.Unmarshal(b, out)
	return out
}
func (c *OrchestratorClassList) DeepCopyObject() runtime.Object {
	out := new(OrchestratorClassList)
	b, _ := json.Marshal(c)
	_ = json.Unmarshal(b, out)
	return out
}

func init() {
	knownTypes = append(knownTypes, &OrchestratorClass{}, &OrchestratorClassList{})
}
