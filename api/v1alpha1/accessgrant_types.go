package v1alpha1

import (
	"encoding/json"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// AccessGrantType discriminates the AccessGrant union.
type AccessGrantType string

const (
	// AccessGrantInternet is a time-limited window to the public web (Open).
	AccessGrantInternet AccessGrantType = "internet"
	// AccessGrantLAN is direct TCP to one LAN host on listed ports.
	AccessGrantLAN AccessGrantType = "lan"
	// AccessGrantPackage approves one package (and optionally one version).
	AccessGrantPackage AccessGrantType = "package"
)

// CellLabel names the cell (ProjectWorkspace) a grant belongs to, so the
// controller and console can list a cell's grants with a label selector.
const CellLabel = "projects.agentwork.dev/cell"

// InternetGrant opens the public web for the grant's lifetime. Private
// networks, cluster services and cloud metadata stay blocked.
type InternetGrant struct {
	// Learn records each request made during the window as an Approval.
	Learn bool `json:"learn,omitempty"`
}

// LANGrant allows outbound TCP from the cell's worker to one IPv4 host.
type LANGrant struct {
	Address string  `json:"address"`
	Ports   []int32 `json:"ports"`
	Note    string  `json:"note,omitempty"`
}

// PackageGrant approves downloads of one package from a reviewed registry.
type PackageGrant struct {
	Ecosystem string `json:"ecosystem"`
	Name      string `json:"name"`
	// Version limits the grant to one version; empty allows every version.
	Version string `json:"version,omitempty"`
}

// AccessGrantSpec is one time-limited widening of a cell's access, on top of
// its AccessProfile. Exactly the member named by Type is set.
type AccessGrantSpec struct {
	// Cell is the ProjectWorkspace name in the same namespace. Immutable.
	Cell     string          `json:"cell"`
	Type     AccessGrantType `json:"type"`
	Internet *InternetGrant  `json:"internet,omitempty"`
	LAN      *LANGrant       `json:"lan,omitempty"`
	Package  *PackageGrant   `json:"package,omitempty"`
	// ExpiresAt ends the grant. The controller stops enforcing it then and
	// deletes the object.
	ExpiresAt metav1.Time `json:"expiresAt"`
	// Reason and RequestedBy are attribution for history; the console sets
	// RequestedBy to the signed-in operator.
	Reason      string `json:"reason,omitempty"`
	RequestedBy string `json:"requestedBy,omitempty"`
}

// AccessGrantState is the controller's view of a grant.
type AccessGrantState string

const (
	AccessGrantActive  AccessGrantState = "Active"
	AccessGrantRefused AccessGrantState = "Refused"
	AccessGrantExpired AccessGrantState = "Expired"
)

type AccessGrantStatus struct {
	State   AccessGrantState `json:"state,omitempty"`
	Message string           `json:"message,omitempty"`
	// ObservedGeneration is the generation the state describes.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
type AccessGrant struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              AccessGrantSpec   `json:"spec"`
	Status            AccessGrantStatus `json:"status,omitempty"`
}

type AccessGrantList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AccessGrant `json:"items"`
}

// Live reports whether the grant is unexpired at now.
func (g *AccessGrant) Live(now time.Time) bool {
	return g.DeletionTimestamp.IsZero() && now.Before(g.Spec.ExpiresAt.Time)
}

func (g *AccessGrant) DeepCopyObject() runtime.Object {
	out := new(AccessGrant)
	b, _ := json.Marshal(g)
	_ = json.Unmarshal(b, out)
	return out
}
func (g *AccessGrantList) DeepCopyObject() runtime.Object {
	out := new(AccessGrantList)
	b, _ := json.Marshal(g)
	_ = json.Unmarshal(b, out)
	return out
}

func init() { knownTypes = append(knownTypes, &AccessGrant{}, &AccessGrantList{}) }
