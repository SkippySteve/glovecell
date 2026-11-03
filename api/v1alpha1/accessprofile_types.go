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
// cell's own spec.https, which changes only while the cell is paused. Rules
// use Agent Substrate's EgressPolicy shape, so their hosts and ports carry
// over to a Substrate policy unchanged; methods and paths are our extension.
type AccessProfileSpec struct {
	// Level is the step this profile implements on the access scale.
	Level AccessLevel `json:"level"`
	// Description says in plain words what the profile allows.
	Description string `json:"description,omitempty"`
	// Rules every cell with this profile gets (EgressRule).
	Rules []EgressRule `json:"rules,omitempty"`
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

// EgressRule is one allow rule in Agent Substrate's EgressPolicy shape: a
// union by protocol, where the protocol fixes what the proxy matches on.
// Rules are a set of allows; traffic no rule allows is refused. The project
// proxy inspects every request, so https is the only member today. Substrate's
// http and tls_passthrough members would join as http and tlsPassthrough.
type EgressRule struct {
	HTTPS *HTTPSEgressRule `json:"https,omitempty"`
}

// HTTPSEgressRule allows inspected HTTPS to the named hosts. Hostnames and
// ports follow Substrate's HttpsRule. Methods and paths are our extension:
// the proxy authorizes each request inside the connection on them.
type HTTPSEgressRule struct {
	// Hostnames are DNS names, or a DNS name with "*" as its whole leftmost
	// label. "*.example.com" matches exactly one label, such as
	// "api.example.com", and neither "example.com" nor "a.b.example.com".
	Hostnames []string `json:"hostnames"`
	// Ports of the destination. Omitted means 443, the only port the project
	// proxy serves.
	Ports *EgressPorts `json:"ports,omitempty"`
	// Methods allowed on these hosts.
	Methods []string `json:"methods"`
	// Paths are literal path prefixes.
	Paths []string `json:"paths"`
}

// EgressPorts selects destination ports, as Substrate's Ports.numbers.
type EgressPorts struct {
	Numbers []int32 `json:"numbers,omitempty"`
}

// HTTPSRules expands the rules into one proxy rule per hostname.
func (s AccessProfileSpec) HTTPSRules() []HTTPSRule {
	var out []HTTPSRule
	for _, r := range s.Rules {
		if r.HTTPS == nil {
			continue
		}
		for _, host := range r.HTTPS.Hostnames {
			out = append(out, HTTPSRule{Host: host, Methods: append([]string(nil), r.HTTPS.Methods...), Paths: append([]string(nil), r.HTTPS.Paths...)})
		}
	}
	return out
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
