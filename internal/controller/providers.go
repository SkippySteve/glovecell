package controller

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"math"
	"slices"
	"strings"
	"time"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/githubpolicy"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/network"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/proxy"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Brokered providers (spec.providers) give a project's worker short-lived,
// access-only leases for subscription CLIs such as Grok and Cursor.
//
// A trusted provider broker, outside every project, renews each provider's
// login on an offline owner and writes one lease per provider to a single
// Secret (ProviderLeases). It has no rights in project namespaces. On every
// reconciliation the controller copies the lease of each provider a project
// opted into, and only those, into that project's own Secrets:
//   - model-access, key provider-access: mounted read-only in the worker at
//     /run/model for the stock launchers;
//   - proxy-ca, keys provider-access and brokered-providers: read by the
//     project's proxy on every provider request.
// It also writes CiliumNetworkPolicy "providers", which lets only the proxy
// reach the opted-in providers' hosts. Removing a provider from spec.providers
// withdraws all three while the project runs and restarts only its proxy, so
// an open provider connection cannot outlive the revocation.

// ProviderLeasesKey is the lease Secret key that holds the broker's grants.
const ProviderLeasesKey = "provider-access"

// BrokeredModelAccessLabel marks a model-access Secret the controller created
// only to carry brokered grants. The controller deletes it when the project
// opts out of every provider; operator-created Secrets keep their other keys.
const BrokeredModelAccessLabel = "projects.agentwork.dev/model-access"

// MaxProviders bounds spec.providers.
const MaxProviders = 2

// brokeredGrant is the access-only grant shape for Grok and Cursor.
type brokeredGrant struct {
	Enabled bool            `json:"enabled"`
	Access  *brokeredAccess `json:"access,omitempty"`
}
type brokeredAccess struct {
	Token   string  `json:"accessToken"`
	Expires float64 `json:"expiresAt"`
	Issuer  string  `json:"issuer,omitempty"`
}

// brokeredState is what one reconciliation decided for a project. objects()
// writes the same proxy-ca keys, so the two writers never disagree.
type brokeredState struct {
	managed bool
	names   []string
	// doc is the project's whole provider-access document after the change
	// (other providers' entries, such as an operator Codex grant, included).
	doc []byte
	// github is the project's GitHub keys (github.go); nil when off.
	github *githubState
}

// providerNames returns the opted-in providers in a stable order.
func providerNames(w *api.ProjectWorkspace) []string {
	var names []string
	for _, p := range w.Spec.Providers {
		names = append(names, p.Name)
	}
	slices.Sort(names)
	return names
}

// providerHosts lists the upstream hosts for names, in a stable order.
func providerHosts(names []string) []string {
	var hosts []string
	for _, name := range names {
		hosts = append(hosts, proxy.BrokeredProviders[name]...)
	}
	return hosts
}

// validateProviders is the static check run with the rest of the project
// policy. A refused opt-in blocks the project, like an invalid HTTPS rule.
func (r *Reconciler) validateProviders(w *api.ProjectWorkspace) error {
	if len(w.Spec.Providers) == 0 {
		return nil
	}
	if r.ProviderLeases == nil {
		return errors.New("brokered providers are not configured on this controller; remove spec.providers")
	}
	if _, ok := r.Network.(network.ProviderPolicy); !ok {
		return errors.New("the network provider cannot scope brokered provider hosts; remove spec.providers")
	}
	if len(w.Spec.Providers) > MaxProviders {
		return errors.New("too many brokered providers")
	}
	seen := map[string]bool{}
	for _, p := range w.Spec.Providers {
		if _, ok := proxy.BrokeredProviders[p.Name]; !ok || seen[p.Name] {
			return fmt.Errorf("unsupported or repeated brokered provider %q", p.Name)
		}
		seen[p.Name] = true
		if len(p.AddedBy) > 512 {
			return errors.New("brokered provider actor too long")
		}
	}
	return nil
}

// ProviderLeaseStatus reports, per brokered provider, whether the broker's
// current lease is usable. It never returns token material.
func (r *Reconciler) ProviderLeaseStatus(ctx context.Context) (map[string]bool, error) {
	leases, err := r.leases(ctx)
	status := map[string]bool{}
	for name := range proxy.BrokeredProviders {
		_, status[name] = leases[name]
	}
	return status, err
}

// leases reads the broker's Secret and returns each provider's grant when it
// is enabled and valid for more than a minute. Anything malformed yields no
// leases: providers fail closed, the project keeps running.
func (r *Reconciler) leases(ctx context.Context) (map[string]brokeredGrant, error) {
	if r.ProviderLeases == nil {
		return nil, errors.New("brokered providers are not configured")
	}
	var secret corev1.Secret
	if err := r.Get(ctx, *r.ProviderLeases, &secret); err != nil {
		return nil, errors.New("provider leases unavailable")
	}
	raw := secret.Data[ProviderLeasesKey]
	if len(raw) == 0 {
		return nil, errors.New("provider leases unavailable")
	}
	if err := validateProviderAccess(raw); err != nil {
		return nil, errors.New("provider leases invalid")
	}
	var all map[string]brokeredGrant
	if err := json.Unmarshal(raw, &all); err != nil {
		return nil, errors.New("provider leases invalid")
	}
	now := float64(time.Now().Unix())
	out := map[string]brokeredGrant{}
	for name := range proxy.BrokeredProviders {
		g, ok := all[name]
		if !ok || !g.Enabled || g.Access == nil || math.IsNaN(g.Access.Expires) || g.Access.Expires <= now+60 {
			continue
		}
		out[name] = brokeredGrant{Enabled: true, Access: &brokeredAccess{Token: g.Access.Token, Expires: g.Access.Expires, Issuer: g.Access.Issuer}}
	}
	return out, nil
}

// applyProviders makes the project's grants, proxy keys and provider policy
// match spec.providers. It runs while the project runs. Without a configured
// lease source it changes nothing, so existing operator grants keep working.
func (r *Reconciler) applyProviders(ctx context.Context, w *api.ProjectWorkspace, names []string, github *api.GitHubAccess) (*brokeredState, error) {
	if r.ProviderLeases == nil && r.GitHubLease == nil {
		return &brokeredState{}, nil
	}
	state := &brokeredState{}
	var hosts []string
	narrowedKeys := false
	if r.ProviderLeases != nil {
		var leases map[string]brokeredGrant
		if len(names) > 0 {
			var err error
			if leases, err = r.leases(ctx); err != nil {
				slog.WarnContext(ctx, "brokered provider leases unavailable; opted-in providers stay disabled", "workspace_uid", string(w.UID), "status", err.Error())
			}
		}
		entries := map[string]json.RawMessage{}
		for _, name := range names {
			grant, ok := leases[name]
			if !ok {
				grant = brokeredGrant{Enabled: false}
			}
			entries[name], _ = json.Marshal(grant)
		}
		doc, err := r.syncModelAccess(ctx, w, entries)
		if err != nil {
			return nil, err
		}
		state = &brokeredState{managed: true, names: names, doc: doc}
		if narrowedKeys, err = r.syncProxyProviders(ctx, w, state); err != nil {
			return nil, err
		}
		hosts = providerHosts(names)
	}
	summary := "none"
	if len(state.names) > 0 {
		summary = strings.Join(state.names, ", ")
	}
	if r.GitHubLease != nil {
		gh, narrowed, err := r.applyGitHub(ctx, w, github)
		if err != nil {
			return nil, err
		}
		state.github = gh
		narrowedKeys = narrowedKeys || narrowed
		if gh.policy != nil {
			hosts = append(hosts, githubpolicy.Hosts...)
			summary += "; GitHub: " + strings.Join(github.Repositories, ", ")
		}
	}
	changed, narrowedPolicy, err := r.syncProviderPolicy(ctx, w, hosts)
	if err != nil {
		return nil, err
	}
	if narrowedKeys || narrowedPolicy {
		// Close open provider connections: the replacement proxy reads the
		// revoked grant at start, and the policy no longer admits the host.
		if err = r.restartRole(ctx, w, "proxy"); err != nil {
			return nil, err
		}
	}
	if changed {
		sum := sha256.Sum256([]byte(summary))
		slog.InfoContext(ctx, "brokered providers applied", "workspace_uid", string(w.UID), "generation", w.Generation, "providers", summary)
		if err = r.auditDetails(ctx, w, "providers-applied-"+hex.EncodeToString(sum[:6]), map[string]string{"providers": summary, "message": "Brokered providers now: " + summary}); err != nil {
			return nil, err
		}
	}
	return state, nil
}

// syncModelAccess sets the brokered entries of the project's provider-access
// document and keeps every other entry and key. It returns the document.
func (r *Reconciler) syncModelAccess(ctx context.Context, w *api.ProjectWorkspace, entries map[string]json.RawMessage) ([]byte, error) {
	s := &corev1.Secret{}
	err := r.Get(ctx, types.NamespacedName{Namespace: namespace(w), Name: "model-access"}, s)
	if apierrors.IsNotFound(err) {
		if len(entries) == 0 {
			return nil, nil
		}
		raw, _ := json.Marshal(entries)
		l := labels(w)
		l[BrokeredModelAccessLabel] = "brokered"
		s = &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "model-access", Namespace: namespace(w), Labels: l}, Data: map[string][]byte{"provider-access": raw}}
		return raw, r.Create(ctx, s)
	}
	if err != nil {
		return nil, err
	}
	if err = owner(w, s); err != nil {
		return nil, err
	}
	current := map[string]json.RawMessage{}
	if raw := s.Data["provider-access"]; len(raw) > 0 {
		if json.Unmarshal(raw, &current) != nil || current == nil {
			return nil, errors.New("model access provider grants are invalid; refusing to merge brokered grants")
		}
	}
	next := maps.Clone(current)
	for name := range proxy.BrokeredProviders {
		delete(next, name)
	}
	maps.Copy(next, entries)
	var raw []byte
	if len(next) > 0 {
		raw, _ = json.Marshal(next)
	}
	if bytes.Equal(raw, s.Data["provider-access"]) {
		return raw, nil
	}
	if len(next) == 0 && s.Labels[BrokeredModelAccessLabel] == "brokered" && len(s.Data) <= 1 {
		if err = r.Delete(ctx, s); err != nil && !apierrors.IsNotFound(err) {
			return nil, err
		}
		return nil, nil
	}
	before := s.DeepCopy()
	if s.Data == nil {
		s.Data = map[string][]byte{}
	}
	if raw == nil {
		delete(s.Data, "provider-access")
	} else {
		s.Data["provider-access"] = raw
	}
	return raw, r.Patch(ctx, s, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{}))
}

// proxyProviderData is the proxy-ca content for brokered providers. Legacy
// operator grants keep the previous rule: copied only for a brokered policy.
func proxyProviderData(state *brokeredState, legacy bool, legacyDoc []byte) map[string][]byte {
	out := map[string][]byte{}
	if state != nil && state.managed {
		if (legacy || len(state.names) > 0) && len(state.doc) > 0 {
			out["provider-access"] = state.doc
		}
		if len(state.names) > 0 {
			out[proxy.BrokeredProviderKey] = []byte(strings.Join(state.names, ","))
		}
		return out
	}
	if legacy && len(legacyDoc) > 0 {
		out["provider-access"] = legacyDoc
	}
	return out
}

// syncProxyProviders updates the brokered keys of an existing proxy-ca. It
// reports whether a provider lost access (so the proxy must restart).
func (r *Reconciler) syncProxyProviders(ctx context.Context, w *api.ProjectWorkspace, state *brokeredState) (bool, error) {
	s := &corev1.Secret{}
	err := r.Get(ctx, types.NamespacedName{Namespace: namespace(w), Name: "proxy-ca"}, s)
	if apierrors.IsNotFound(err) {
		return false, nil // objects() creates it with the same content.
	}
	if err != nil {
		return false, err
	}
	if err = owner(w, s); err != nil {
		return false, err
	}
	cfg, err := r.policyConfiguration(w, w.Spec.HTTPS)
	if err != nil {
		return false, err
	}
	want := proxyProviderData(state, cfg.BrokeredProviders, nil)
	before, _ := proxy.ParseBrokeredProviders(s.Data[proxy.BrokeredProviderKey])
	narrowed := false
	for _, name := range before {
		if !slices.Contains(state.names, name) {
			narrowed = true
		}
	}
	if bytes.Equal(s.Data["provider-access"], want["provider-access"]) && bytes.Equal(s.Data[proxy.BrokeredProviderKey], want[proxy.BrokeredProviderKey]) {
		return narrowed, nil
	}
	old := s.DeepCopy()
	for _, key := range []string{"provider-access", proxy.BrokeredProviderKey} {
		if value, ok := want[key]; ok {
			s.Data[key] = value
		} else {
			delete(s.Data, key)
		}
	}
	return narrowed, r.Patch(ctx, s, client.MergeFrom(old))
}

// syncProviderPolicy makes CiliumNetworkPolicy "providers" allow exactly
// hosts. It reports whether the policy changed and whether it lost a host.
func (r *Reconciler) syncProviderPolicy(ctx context.Context, w *api.ProjectWorkspace, hosts []string) (bool, bool, error) {
	ns := namespace(w)
	policy, ok := r.Network.(network.ProviderPolicy)
	if !ok {
		if len(hosts) > 0 {
			return false, false, errors.New("the network provider cannot scope brokered provider hosts")
		}
		return false, false, nil
	}
	current := &unstructured.Unstructured{}
	current.SetGroupVersionKind(ciliumPolicyGVK)
	err := r.Get(ctx, types.NamespacedName{Namespace: ns, Name: network.ProviderPolicyName}, current)
	if apierrors.IsNotFound(err) {
		if len(hosts) == 0 {
			return false, false, nil
		}
		return true, false, r.Create(ctx, policy.ProviderObject(ns, string(w.UID), hosts))
	}
	if err != nil {
		return false, false, err
	}
	if err = owner(w, current); err != nil {
		return false, false, err
	}
	var had []string
	egress, _, _ := unstructured.NestedSlice(current.Object, "spec", "egress")
	for _, rule := range egress {
		fqdns, _, _ := unstructured.NestedSlice(rule.(map[string]any), "toFQDNs")
		for _, f := range fqdns {
			if name, ok := f.(map[string]any)["matchName"].(string); ok {
				had = append(had, name)
			}
		}
	}
	narrowed := slices.ContainsFunc(had, func(h string) bool { return !slices.Contains(hosts, h) })
	if len(hosts) == 0 {
		if err = r.Delete(ctx, current); err != nil && !apierrors.IsNotFound(err) {
			return false, false, err
		}
		return true, narrowed, nil
	}
	want := policy.ProviderObject(ns, string(w.UID), hosts)
	if same(current.Object["spec"], want.Object["spec"]) && current.GetLabels()[network.ProviderPolicyLabel] == network.ProviderPolicyName {
		return false, false, nil
	}
	merged := maps.Clone(current.GetLabels())
	if merged == nil {
		merged = map[string]string{}
	}
	maps.Copy(merged, want.GetLabels())
	current.SetLabels(merged)
	current.Object["spec"] = want.Object["spec"]
	return true, narrowed, r.Update(ctx, current)
}

// restartRole deletes the project's pods with one role; their controllers
// replace them. Volume, Git identity and namespace stay.
func (r *Reconciler) restartRole(ctx context.Context, w *api.ProjectWorkspace, role string) error {
	var pods corev1.PodList
	if err := r.List(ctx, &pods, client.InNamespace(namespace(w)), client.MatchingLabels{api.OwnerLabel: string(w.UID), "projects.agentwork.dev/role": role}); err != nil {
		return err
	}
	for i := range pods.Items {
		if err := r.Delete(ctx, &pods.Items[i]); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	return nil
}
