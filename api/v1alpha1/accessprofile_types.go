package v1alpha1

import (
	"encoding/json"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// AccessLevel names a step on the console's access scale, from Locked to Open.
type AccessLevel string

const (
	AccessLevelLocked   AccessLevel = "locked"
	AccessLevelAsk      AccessLevel = "ask"
	AccessLevelStandard AccessLevel = "standard"
	AccessLevelOpen     AccessLevel = "open"
	AccessLevelCustom   AccessLevel = "custom"
)

// DefaultAccessProfileAnnotation marks the cluster default profile, like the
// default StorageClass. The console stamps it on new cells that name none;
// a cell without a profile name keeps only its own rules, so an upgrade never
// widens an existing cell.
const DefaultAccessProfileAnnotation = "accessprofile.projects.agentwork.dev/is-default"

// AccessProfileSpec is a named, cluster-wide preset of what a cell may reach.
// Profile rules are read only (GET and HEAD, plus POST to git-upload-pack),
// like the deployment's reference rules: anything that writes belongs in the
// cell's own spec.https, which changes only while the cell is paused.
type AccessProfileSpec struct {
	// Level is the step this profile implements on the access scale.
	Level AccessLevel `json:"level"`
	// Description says in plain words what the profile allows.
	Description string `json:"description,omitempty"`
	// HTTPS rules every cell with this profile gets.
	HTTPS []HTTPSRule `json:"https,omitempty"`
	// Registries expand to the reviewed package registry hosts (go, npm, python, rust).
	Registries []string `json:"registries,omitempty"`
	// AllowedGrants lists the AccessGrant types cells with this profile may
	// hold. Empty allows none: a locked cell gets no windows or grants.
	AllowedGrants []AccessGrantType `json:"allowedGrants,omitempty"`
	// MaxGrantDuration caps how far ahead a grant's expiresAt may be, per type.
	MaxGrantDuration map[AccessGrantType]metav1.Duration `json:"maxGrantDuration,omitempty"`
	// ApproveRequests lists blocked requests for an operator to approve one by
	// one (Approval objects). It allows nothing by itself.
	ApproveRequests bool `json:"approveRequests,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
type AccessProfile struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              AccessProfileSpec `json:"spec"`
}

type AccessProfileList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AccessProfile `json:"items"`
}

// Allows reports whether the profile permits grants of type t.
func (p *AccessProfile) Allows(t AccessGrantType) bool {
	for _, allowed := range p.Spec.AllowedGrants {
		if allowed == t {
			return true
		}
	}
	return false
}

func (p *AccessProfile) DeepCopyObject() runtime.Object {
	out := new(AccessProfile)
	b, _ := json.Marshal(p)
	_ = json.Unmarshal(b, out)
	return out
}
func (p *AccessProfileList) DeepCopyObject() runtime.Object {
	out := new(AccessProfileList)
	b, _ := json.Marshal(p)
	_ = json.Unmarshal(b, out)
	return out
}

func init() { knownTypes = append(knownTypes, &AccessProfile{}, &AccessProfileList{}) }
