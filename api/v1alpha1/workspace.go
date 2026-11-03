package v1alpha1

import (
	"encoding/json"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var GroupVersion = schema.GroupVersion{Group: "projects.agentwork.dev", Version: "v1alpha1"}

const Finalizer = "projects.agentwork.dev/cleanup"
const OwnerLabel = "projects.agentwork.dev/workspace-uid"

type HTTPSRule struct {
	Host    string   `json:"host"`
	Methods []string `json:"methods"`
	Paths   []string `json:"paths"` // Literal prefixes, not regular expressions.
	// Source labels a rule from the deployment's reference list (README
	// "Reference rules"), such as "git:reference-sources.yaml#cilium". Only
	// the controller sets it: the CRD has no such field, and a project's own
	// rules with a source are refused. Labelled rules are read only, may use
	// a "*.domain" host, and do not count toward the 64 project rules.
	Source string `json:"source,omitempty"`
}
type WorkspaceSpec struct {
	Repository   string              `json:"repository"`
	DesiredState string              `json:"desiredState"`
	HTTPS        []HTTPSRule         `json:"https,omitempty"`
	Repositories []RepositoryProject `json:"repositories,omitempty"`
	// LANHosts grants the project's worker direct TCP to single IPv4 hosts on the
	// operator's LAN. Unlike HTTPS rules they change while the project runs.
	LANHosts []LANHost `json:"lanHosts,omitempty"`
	// Providers opts the project into brokered subscription providers (Grok,
	// Cursor). Off by default. Like LAN hosts they change while the project
	// runs: removing an entry revokes that provider for this project only.
	// The controller needs a configured provider lease source.
	Providers []ProviderGrant `json:"providers,omitempty"`
	// AccessProfileName names the cluster-scoped AccessProfile (accessprofile_types.go).
	AccessProfileName string `json:"accessProfileName,omitempty"`
	// OrchestratorClassName names the OrchestratorClass whose adapter manages
	// agent orchestrator access. Empty means none (the deployment default).
	// Immutable once set.
	OrchestratorClassName string `json:"orchestratorClassName,omitempty"`
	// ForgeClassName names the cluster-scoped ForgeClass that hosts
	// Repository (forgeclass_types.go). Empty keeps the controller's configured
	// forge. Immutable: revocation must reach the forge that granted access.
	ForgeClassName string `json:"forgeClassName,omitempty"`
	// GitHub opts the project into brokered GitHub access: the project's proxy
	// attaches a GitHub credential, held outside the project, to requests the
	// GitHub policy allows. Off by default; editable while running. Removing
	// it revokes access for this project. The controller needs a GitHub lease
	// source (GITHUB_LEASE_SECRET).
	GitHub *GitHubAccess `json:"github,omitempty"`
}

// GitHubAccess is a project's GitHub policy. See docs/design/github-brokering.md.
type GitHubAccess struct {
	// Repositories are "owner/name" or "owner/*" (every repository of owner
	// the credential reaches).
	Repositories []string `json:"repositories"`
	// ProtectedBranches no push may create, update or delete: names or
	// "prefix/*". Each repository's default branch is always protected.
	ProtectedBranches []string `json:"protectedBranches,omitempty"`
	// PushBranches, when set, are the only branches pushes may touch, for
	// example ["agent/*"].
	PushBranches []string `json:"pushBranches,omitempty"`
	// AllowMerge lets agents merge pull requests. Off by default.
	AllowMerge bool `json:"allowMerge,omitempty"`
	// AllowReleases lets agents create releases and push new tags. Off by default.
	AllowReleases bool `json:"allowReleases,omitempty"`
	// Approvals are operator approvals for repository creation and forks,
	// each bound to exact arguments, usable once and until it expires.
	Approvals []GitHubApproval `json:"approvals,omitempty"`
	AddedBy   string           `json:"addedBy,omitempty"`
	AddedAt   *metav1.Time     `json:"addedAt,omitempty"`
}

// GitHubApproval approves one repository creation or fork.
type GitHubApproval struct {
	// Action is create-repository or fork-repository.
	Action string `json:"action"`
	// Name is the new repository's (or fork's) name.
	Name string `json:"name"`
	// Visibility is private or public for create-repository; empty for forks.
	Visibility string `json:"visibility,omitempty"`
	// Source is the owner/name being forked.
	Source     string      `json:"source,omitempty"`
	ExpiresAt  metav1.Time `json:"expiresAt"`
	ApprovedBy string      `json:"approvedBy,omitempty"`
}

// ProviderGrant is one operator opt-in to a brokered provider. The worker then
// receives the provider's short-lived access lease, and the project proxy may
// reach the provider's reviewed hosts. Refresh material never leaves its owner.
type ProviderGrant struct {
	Name string `json:"name"`
	// ProviderClassName names the cluster-scoped ProviderClass that supplies
	// the lease (providerclass_types.go). Empty resolves to the default class
	// for Name, which matches the behaviour before classes existed.
	ProviderClassName string       `json:"providerClassName,omitempty"`
	AddedBy           string       `json:"addedBy,omitempty"`
	AddedAt           *metav1.Time `json:"addedAt,omitempty"`
}

// LANHost is one operator grant: outbound TCP from the worker to Address on Ports.
// The controller checks Address against its configured LAN ranges and denylist.
type LANHost struct {
	Address string       `json:"address"`
	Ports   []int32      `json:"ports"`
	Note    string       `json:"note,omitempty"`
	AddedBy string       `json:"addedBy,omitempty"`
	AddedAt *metav1.Time `json:"addedAt,omitempty"`
}

// RepositoryProject enrolls another checkout in the existing workspace volume.
// Public GitHub projects use anonymous read-only Git transport.
type RepositoryProject struct {
	Name       string `json:"name"`
	Host       string `json:"host"`
	Repository string `json:"repository"`
	Public     bool   `json:"public,omitempty"`
}

// AuditEvent is one entry of the deprecated status.audit list.
//
// Deprecated: recent lifecycle transitions are Kubernetes Events
// (events.k8s.io/v1) regarding the workspace (`kubectl events --for
// projectworkspace/<name>`); the durable history is the console's History.
type AuditEvent struct {
	Time       metav1.Time `json:"time"`
	Action     string      `json:"action"`
	Generation int64       `json:"generation"`
}
type WorkspaceStatus struct {
	Phase              string `json:"phase,omitempty"`
	Namespace          string `json:"namespace,omitempty"`
	ObservedGeneration int64  `json:"observedGeneration,omitempty"`
	// Conditions carry the current state; each records the generation it
	// was computed from (observedGeneration).
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// Audit is the old in-object phase history.
	//
	// Deprecated: the controller no longer appends to it. Existing entries
	// stay readable for one release. Use Events for recent transitions and
	// the console's History for the durable record.
	Audit []AuditEvent `json:"audit,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
type ProjectWorkspace struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              WorkspaceSpec   `json:"spec"`
	Status            WorkspaceStatus `json:"status,omitempty"`
}
type ProjectWorkspaceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ProjectWorkspace `json:"items"`
}

func (w *ProjectWorkspace) DeepCopyObject() runtime.Object {
	out := new(ProjectWorkspace)
	b, _ := json.Marshal(w)
	_ = json.Unmarshal(b, out)
	return out
}
func (w *ProjectWorkspaceList) DeepCopyObject() runtime.Object {
	out := new(ProjectWorkspaceList)
	b, _ := json.Marshal(w)
	_ = json.Unmarshal(b, out)
	return out
}

// knownTypes collects the kinds other files in this package define, so a new
// kind registers itself without editing AddToScheme.
var knownTypes []runtime.Object

func AddToScheme(s *runtime.Scheme) error {
	s.AddKnownTypes(GroupVersion, &ProjectWorkspace{}, &ProjectWorkspaceList{})
	s.AddKnownTypes(GroupVersion, knownTypes...)
	metav1.AddToGroupVersion(s, GroupVersion)
	return nil
}
