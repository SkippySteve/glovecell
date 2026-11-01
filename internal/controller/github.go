package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/githubpolicy"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/network"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/proxy"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Brokered GitHub access (spec.github).
//
// The GitHub broker, outside every project, holds a GitHub App user
// authorization and writes one short access lease to its lease Secret
// (GitHubLease). On every reconciliation the controller copies that lease,
// and the project's GitHub policy, into the proxy-ca Secret of each project
// with spec.github, and only those. The worker never mounts proxy-ca and has
// no Kubernetes token, so the credential never enters the project's sandbox.
// CiliumNetworkPolicy "providers" lets only the proxy reach github.com and
// api.github.com. Removing spec.github withdraws all of it and restarts the
// project's proxy, so no open GitHub connection outlives the revocation.

// githubState is the proxy-ca content one reconciliation decided.
type githubState struct {
	policy []byte // nil: GitHub access off
	access []byte
}

// githubPolicy is the proxy's view of spec.github.
func githubPolicy(w *api.ProjectWorkspace, spec *api.GitHubAccess) *githubpolicy.Policy {
	p := &githubpolicy.Policy{Project: w.Name, ProjectUID: string(w.UID), Repositories: spec.Repositories, ProtectedBranches: spec.ProtectedBranches, PushBranches: spec.PushBranches, AllowMerge: spec.AllowMerge, AllowReleases: spec.AllowReleases}
	for _, a := range spec.Approvals {
		p.Approvals = append(p.Approvals, githubpolicy.Approval{Action: a.Action, Name: a.Name, Visibility: a.Visibility, Source: a.Source, ExpiresAt: a.ExpiresAt.Time.UTC(), ApprovedBy: a.ApprovedBy})
	}
	return p
}

// validateGitHub is the static check run with the rest of the project policy.
// A refused opt-in blocks the project and withdraws every brokered grant.
func (r *Reconciler) validateGitHub(w *api.ProjectWorkspace) error {
	if w.Spec.GitHub == nil {
		return nil
	}
	if r.GitHubLease == nil {
		return errors.New("brokered GitHub access is not configured on this controller; remove spec.github")
	}
	if _, ok := r.Network.(network.ProviderPolicy); !ok {
		return errors.New("the network provider cannot scope GitHub hosts; remove spec.github")
	}
	if len(w.Spec.GitHub.AddedBy) > 512 {
		return errors.New("GitHub access actor too long")
	}
	return githubPolicy(w, w.Spec.GitHub).Validate()
}

// githubLeaseDoc returns the lease to copy, or a disabled lease when the
// broker's lease is missing, malformed or about to expire.
func (r *Reconciler) githubLeaseDoc(ctx context.Context) []byte {
	disabled, _ := json.Marshal(proxy.GitHubLease{Enabled: false})
	var s corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Namespace: r.GitHubLease.Namespace, Name: r.GitHubLease.Name}, &s); err != nil {
		return disabled
	}
	lease, err := proxy.ParseGitHubLease(s.Data[proxy.GitHubAccessKey])
	if err != nil || !lease.Live(time.Now()) {
		return disabled
	}
	raw, _ := json.Marshal(lease)
	return raw
}

// applyGitHub decides the project's GitHub keys and writes them to an
// existing proxy-ca. It reports whether access narrowed (the proxy must
// restart). spec nil withdraws access.
func (r *Reconciler) applyGitHub(ctx context.Context, w *api.ProjectWorkspace, spec *api.GitHubAccess) (*githubState, bool, error) {
	state := &githubState{}
	if spec != nil {
		state.policy, _ = json.Marshal(githubPolicy(w, spec))
		state.access = r.githubLeaseDoc(ctx)
	}
	s := &corev1.Secret{}
	err := r.Get(ctx, types.NamespacedName{Namespace: namespace(w), Name: "proxy-ca"}, s)
	if apierrors.IsNotFound(err) {
		return state, false, nil // objects() creates it with the same content.
	}
	if err != nil {
		return nil, false, err
	}
	if err = owner(w, s); err != nil {
		return nil, false, err
	}
	narrowed := githubNarrowed(s.Data[proxy.GitHubPolicyKey], state.policy)
	if bytes.Equal(s.Data[proxy.GitHubPolicyKey], state.policy) && bytes.Equal(s.Data[proxy.GitHubAccessKey], state.access) {
		return state, narrowed, nil
	}
	old := s.DeepCopy()
	for key, value := range map[string][]byte{proxy.GitHubPolicyKey: state.policy, proxy.GitHubAccessKey: state.access} {
		if value == nil {
			delete(s.Data, key)
		} else {
			s.Data[key] = value
		}
	}
	return state, narrowed, r.Patch(ctx, s, client.MergeFrom(old))
}

// githubNarrowed reports a change that must close open connections: access
// removed, or any change other than added or removed approvals. Approvals
// apply on the next request without a restart.
func githubNarrowed(before, after []byte) bool {
	if len(before) == 0 {
		return false
	}
	if len(after) == 0 {
		return true
	}
	var b, a githubpolicy.Policy
	if json.Unmarshal(before, &b) != nil || json.Unmarshal(after, &a) != nil {
		return true
	}
	b.Approvals, a.Approvals = nil, nil
	return !reflect.DeepEqual(b, a)
}

// githubProxyData is the proxy-ca content for GitHub brokering.
func githubProxyData(state *githubState) map[string][]byte {
	out := map[string][]byte{}
	if state != nil && state.policy != nil {
		out[proxy.GitHubPolicyKey] = state.policy
		out[proxy.GitHubAccessKey] = state.access
	}
	return out
}
