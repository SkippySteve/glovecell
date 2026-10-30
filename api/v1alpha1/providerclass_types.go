package v1alpha1

import (
	"encoding/json"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// DefaultClassAnnotation marks a ProviderClass as the default for its
// spec.provider: a ProviderGrant without providerClassName resolves to it.
// More than one default for a provider fails closed.
const DefaultClassAnnotation = "projects.agentwork.dev/is-default-class"

// ProviderClass describes one way to obtain short-lived access for a model
// provider, like a StorageClass describes storage. It is cluster-scoped and
// admin-only. Projects reference it by name from spec.providers[].
// The controller resolves it on the trusted side; credentials never reach a
// project's worker.
//
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:subresource:status
type ProviderClass struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ProviderClassSpec   `json:"spec"`
	Status            ProviderClassStatus `json:"status,omitempty"`
}

type ProviderClassSpec struct {
	// Driver obtains the lease: "provider-broker" (the trusted provider
	// broker's lease Secret). "openbao-broker" and "api-key-secret" are
	// reserved and refused until implemented. Unknown drivers fail closed.
	Driver string `json:"driver"`
	// Provider is the provider this class serves, such as grok or cursor. It
	// must equal the name of every ProviderGrant that references the class.
	Provider string `json:"provider"`
	// Lease bounds the access leases the controller hands to projects.
	Lease *ProviderLeaseBounds `json:"lease,omitempty"`
	// Parameters are driver-specific. Unknown keys fail closed.
	Parameters map[string]string `json:"parameters,omitempty"`
}

// ProviderLeaseBounds limits leases copied into a project.
type ProviderLeaseBounds struct {
	// MinDuration is the remaining lifetime a lease needs to be handed out.
	// Default and minimum 1m (the project proxy refuses shorter leases).
	MinDuration *metav1.Duration `json:"minDuration,omitempty"`
	// MaxDuration caps the expiry written into a project, counted from the
	// reconciliation. Empty keeps the broker's expiry.
	MaxDuration *metav1.Duration `json:"maxDuration,omitempty"`
}

type ProviderClassStatus struct {
	ObservedGeneration int64              `json:"observedGeneration,omitempty"`
	Conditions         []metav1.Condition `json:"conditions,omitempty"`
}

type ProviderClassList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ProviderClass `json:"items"`
}

func (c *ProviderClass) DeepCopyObject() runtime.Object {
	out := new(ProviderClass)
	b, _ := json.Marshal(c)
	_ = json.Unmarshal(b, out)
	return out
}
func (c *ProviderClassList) DeepCopyObject() runtime.Object {
	out := new(ProviderClassList)
	b, _ := json.Marshal(c)
	_ = json.Unmarshal(b, out)
	return out
}

func init() { knownTypes = append(knownTypes, &ProviderClass{}, &ProviderClassList{}) }
