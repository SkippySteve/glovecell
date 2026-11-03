package v1alpha1

import (
	"encoding/json"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// ForgeClass describes one Git forge the controller can grant workspaces
// access to. It is cluster-scoped and admin-only. A ProjectWorkspace names it
// in spec.forgeClassName; an empty name keeps the controller's configured
// forge. The credentials stay with the controller: workers receive only the
// per-workspace token the forge mints.
//
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:subresource:status
type ForgeClass struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ForgeClassSpec   `json:"spec"`
	Status            ForgeClassStatus `json:"status,omitempty"`
}

type ForgeClassSpec struct {
	// Driver is "forgejo". "github" and "gitlab" are reserved and refused as
	// unsupported for now. Unknown drivers fail closed.
	Driver string `json:"driver"`
	// BaseURL is the forge's HTTPS origin, such as https://git.example.com.
	BaseURL string `json:"baseURL"`
	// CredentialsSecretRef names the controller's administrative credential
	// for this forge. The Secret must be in the controller's namespace.
	CredentialsSecretRef SecretKeyReference `json:"credentialsSecretRef"`
	// Parameters are driver-specific (forgejo: owner). Unknown keys fail closed.
	Parameters map[string]string `json:"parameters,omitempty"`
}

// SecretKeyReference selects one key of a Secret. An empty namespace means
// the controller's namespace; any other namespace is refused.
type SecretKeyReference struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
	Key       string `json:"key"`
}

type ForgeClassStatus struct {
	ObservedGeneration int64              `json:"observedGeneration,omitempty"`
	Conditions         []metav1.Condition `json:"conditions,omitempty"`
}

type ForgeClassList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ForgeClass `json:"items"`
}

func (c *ForgeClass) DeepCopyObject() runtime.Object {
	out := new(ForgeClass)
	b, _ := json.Marshal(c)
	_ = json.Unmarshal(b, out)
	return out
}
func (c *ForgeClassList) DeepCopyObject() runtime.Object {
	out := new(ForgeClassList)
	b, _ := json.Marshal(c)
	_ = json.Unmarshal(b, out)
	return out
}

func init() { knownTypes = append(knownTypes, &ForgeClass{}, &ForgeClassList{}) }
