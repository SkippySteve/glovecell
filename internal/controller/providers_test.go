package controller

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/network"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/proxy"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
)

var leaseKey = types.NamespacedName{Namespace: "broker", Name: "provider-leases"}

func leaseDoc(t *testing.T, expires time.Time) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"grok":   map[string]any{"enabled": true, "access": map[string]any{"accessToken": "grok-lease", "expiresAt": expires.Unix(), "issuer": "https://auth.x.ai"}},
		"cursor": map[string]any{"enabled": true, "access": map[string]any{"accessToken": "cursor-lease", "expiresAt": expires.Unix()}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func providerFixture(t *testing.T, names ...string) (*Reconciler, *api.ProjectWorkspace) {
	t.Helper()
	r, w, _ := fixture(t)
	r.ProviderLeases = &leaseKey
	leases := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: leaseKey.Name, Namespace: leaseKey.Namespace}, Data: map[string][]byte{ProviderLeasesKey: leaseDoc(t, time.Now().Add(5*time.Minute))}}
	if err := r.Create(context.Background(), leases); err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		w.Spec.Providers = append(w.Spec.Providers, api.ProviderGrant{Name: name})
	}
	return r, w
}

func secretData(t *testing.T, r *Reconciler, w *api.ProjectWorkspace, name string) map[string][]byte {
	t.Helper()
	var s corev1.Secret
	err := r.Get(context.Background(), types.NamespacedName{Namespace: namespace(w), Name: name}, &s)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return s.Data
}

func grantsOf(t *testing.T, raw []byte) map[string]brokeredGrant {
	t.Helper()
	if len(raw) == 0 {
		return nil
	}
	var grants map[string]brokeredGrant
	if err := json.Unmarshal(raw, &grants); err != nil {
		t.Fatal(err)
	}
	return grants
}

func providerHostsOf(t *testing.T, r *Reconciler, w *api.ProjectWorkspace) []string {
	t.Helper()
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(ciliumPolicyGVK)
	err := r.Get(context.Background(), types.NamespacedName{Namespace: namespace(w), Name: network.ProviderPolicyName}, u)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	selector, _, _ := unstructured.NestedStringMap(u.Object, "spec", "endpointSelector", "matchLabels")
	if selector["projects.agentwork.dev/role"] != "proxy" || selector[api.OwnerLabel] != string(w.UID) {
		t.Fatalf("provider policy selects %v, not only this project's proxy", selector)
	}
	var hosts []string
	egress, _, _ := unstructured.NestedSlice(u.Object, "spec", "egress")
	for _, rule := range egress {
		fqdns, _, _ := unstructured.NestedSlice(rule.(map[string]any), "toFQDNs")
		for _, f := range fqdns {
			hosts = append(hosts, f.(map[string]any)["matchName"].(string))
		}
	}
	return hosts
}

func proxyPod(t *testing.T, r *Reconciler, w *api.ProjectWorkspace) {
	t.Helper()
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "proxy-1", Namespace: namespace(w), Labels: map[string]string{api.OwnerLabel: string(w.UID), "projects.agentwork.dev/role": "proxy"}}}
	if err := r.Create(context.Background(), pod); err != nil {
		t.Fatal(err)
	}
}

func proxyPodExists(t *testing.T, r *Reconciler, w *api.ProjectWorkspace) bool {
	t.Helper()
	var pod corev1.Pod
	err := r.Get(context.Background(), types.NamespacedName{Namespace: namespace(w), Name: "proxy-1"}, &pod)
	if apierrors.IsNotFound(err) {
		return false
	}
	if err != nil {
		t.Fatal(err)
	}
	return true
}

func TestBrokeredProvidersOffWithoutLeaseSource(t *testing.T) {
	ctx := context.Background()
	r, w, _ := fixture(t)
	if err := r.ensure(ctx, w); err != nil {
		t.Fatal(err)
	}
	if secretData(t, r, w, "model-access") != nil || providerHostsOf(t, r, w) != nil {
		t.Fatal("a controller without a lease source granted provider access")
	}
	w.Spec.Providers = []api.ProviderGrant{{Name: "grok"}}
	if err := r.validate(ctx, w); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatal("opt-in without a lease source was not refused", err)
	}
}

func TestBrokeredProviderValidation(t *testing.T) {
	for _, providers := range [][]api.ProviderGrant{{{Name: "codex"}}, {{Name: "grok"}, {Name: "grok"}}, {{Name: "muse"}}, {{Name: "grok"}, {Name: "cursor"}, {Name: "cursor"}}} {
		r, w := providerFixture(t)
		w.Spec.Providers = providers
		if r.validateProviders(w) == nil {
			t.Fatal("accepted", providers)
		}
	}
}

func TestBrokeredProviderGrantAndRevokeWhileRunning(t *testing.T) {
	ctx := context.Background()
	r, w := providerFixture(t, "grok")
	if err := r.validate(ctx, w); err != nil {
		t.Fatal(err)
	}
	if err := r.ensure(ctx, w); err != nil {
		t.Fatal(err)
	}
	model := secretData(t, r, w, "model-access")
	if len(model) != 1 {
		t.Fatalf("model-access carries more than provider grants: %v", model)
	}
	grants := grantsOf(t, model["provider-access"])
	if len(grants) != 1 || !grants["grok"].Enabled || grants["grok"].Access.Token != "grok-lease" || grants["grok"].Access.Issuer != "https://auth.x.ai" {
		t.Fatalf("worker grants = %#v", grants)
	}
	if err := RejectRefreshMaterial(model); err != nil {
		t.Fatal("controller wrote a grant its own validation refuses", err)
	}
	proxyCA := secretData(t, r, w, "proxy-ca")
	if string(proxyCA[proxy.BrokeredProviderKey]) != "grok" || string(proxyCA["provider-access"]) != string(model["provider-access"]) {
		t.Fatalf("proxy keys = %q / %q", proxyCA[proxy.BrokeredProviderKey], proxyCA["provider-access"])
	}
	if hosts := providerHostsOf(t, r, w); !slices.Equal(hosts, []string{"cli-chat-proxy.grok.com"}) {
		t.Fatal("provider policy hosts", hosts)
	}
	sandbox := &unstructured.Unstructured{}
	sandbox.SetGroupVersionKind(sandboxGVK)
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace(w), Name: "project"}, sandbox); err != nil {
		t.Fatal(err)
	}
	if mode, _, _ := unstructured.NestedString(sandbox.Object, "spec", "operatingMode"); mode == "Suspended" {
		t.Fatal("opt-in suspended the running project")
	}
	if len(auditActions(t, r, "providers-applied-")) != 1 {
		t.Fatal("opt-in was not audited")
	}

	// Adding a provider widens access without restarting the proxy.
	proxyPod(t, r, w)
	w.Spec.Providers = append(w.Spec.Providers, api.ProviderGrant{Name: "cursor"})
	if err := r.ensure(ctx, w); err != nil {
		t.Fatal(err)
	}
	if !proxyPodExists(t, r, w) {
		t.Fatal("widening restarted the proxy")
	}
	if g := grantsOf(t, secretData(t, r, w, "model-access")["provider-access"]); !g["grok"].Enabled || !g["cursor"].Enabled || g["cursor"].Access.Token != "cursor-lease" {
		t.Fatalf("grants = %#v", g)
	}
	if hosts := providerHostsOf(t, r, w); !slices.Equal(hosts, []string{"api2.cursor.sh", "agentn.global.api5.cursor.sh", "cli-chat-proxy.grok.com"}) {
		t.Fatal("provider policy hosts", hosts)
	}

	// Revoking one provider withdraws it everywhere and restarts the proxy.
	w.Spec.Providers = []api.ProviderGrant{{Name: "cursor"}}
	if err := r.ensure(ctx, w); err != nil {
		t.Fatal(err)
	}
	if proxyPodExists(t, r, w) {
		t.Fatal("revocation left the proxy running with open provider connections")
	}
	g := grantsOf(t, secretData(t, r, w, "model-access")["provider-access"])
	if _, ok := g["grok"]; ok || !g["cursor"].Enabled {
		t.Fatalf("grants after revocation = %#v", g)
	}
	proxyCA = secretData(t, r, w, "proxy-ca")
	if string(proxyCA[proxy.BrokeredProviderKey]) != "cursor" || strings.Contains(string(proxyCA["provider-access"]), "grok") {
		t.Fatal("proxy kept the revoked grant")
	}
	if hosts := providerHostsOf(t, r, w); slices.Contains(hosts, "cli-chat-proxy.grok.com") {
		t.Fatal("provider policy kept the revoked host")
	}

	// Opting out entirely removes the controller-created Secret and policy.
	w.Spec.Providers = nil
	if err := r.ensure(ctx, w); err != nil {
		t.Fatal(err)
	}
	if secretData(t, r, w, "model-access") != nil || providerHostsOf(t, r, w) != nil {
		t.Fatal("opt-out left provider access behind")
	}
	if proxyCA = secretData(t, r, w, "proxy-ca"); proxyCA[proxy.BrokeredProviderKey] != nil || proxyCA["provider-access"] != nil {
		t.Fatal("opt-out left proxy grant keys")
	}
}

func TestBrokeredLeasesAreRenewedOnReconcile(t *testing.T) {
	ctx := context.Background()
	r, w := providerFixture(t, "grok")
	if err := r.ensure(ctx, w); err != nil {
		t.Fatal(err)
	}
	var leases corev1.Secret
	if err := r.Get(ctx, leaseKey, &leases); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(4 * time.Minute).Truncate(time.Second)
	leases.Data[ProviderLeasesKey] = leaseDoc(t, later)
	if err := r.Update(ctx, &leases); err != nil {
		t.Fatal(err)
	}
	if err := r.ensure(ctx, w); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"model-access", "proxy-ca"} {
		if g := grantsOf(t, secretData(t, r, w, name)["provider-access"]); g["grok"].Access == nil || g["grok"].Access.Expires != float64(later.Unix()) {
			t.Fatalf("%s did not carry the renewed lease: %#v", name, g)
		}
	}
}

func TestBrokeredLeaseFaultsFailClosed(t *testing.T) {
	ctx := context.Background()
	for name, mutate := range map[string]func(*corev1.Secret){
		"missing": nil,
		"expired": func(s *corev1.Secret) { s.Data[ProviderLeasesKey] = leaseDoc(t, time.Now().Add(30*time.Second)) },
		"refresh material": func(s *corev1.Secret) {
			s.Data[ProviderLeasesKey] = []byte(`{"grok":{"enabled":true,"access":{"accessToken":"a","expiresAt":9999999999,"issuer":"https://auth.x.ai","refreshToken":"never"}}}`)
		},
		"unknown provider": func(s *corev1.Secret) { s.Data[ProviderLeasesKey] = []byte(`{"other":{"enabled":false}}`) },
	} {
		t.Run(name, func(t *testing.T) {
			r, w := providerFixture(t, "grok")
			var leases corev1.Secret
			if err := r.Get(ctx, leaseKey, &leases); err != nil {
				t.Fatal(err)
			}
			if mutate == nil {
				if err := r.Delete(ctx, &leases); err != nil {
					t.Fatal(err)
				}
			} else {
				mutate(&leases)
				if err := r.Update(ctx, &leases); err != nil {
					t.Fatal(err)
				}
			}
			if err := r.ensure(ctx, w); err != nil {
				t.Fatal("a lease fault stopped the project", err)
			}
			for _, secret := range []string{"model-access", "proxy-ca"} {
				raw := secretData(t, r, w, secret)["provider-access"]
				if g := grantsOf(t, raw); g["grok"].Enabled || g["grok"].Access != nil || strings.Contains(string(raw), "never") {
					t.Fatalf("%s granted access from a faulty lease: %s", secret, raw)
				}
			}
		})
	}
}

func TestBrokeredGrantsStayInTheirProject(t *testing.T) {
	ctx := context.Background()
	r, a := providerFixture(t, "grok", "cursor")
	b := &api.ProjectWorkspace{ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "control", UID: types.UID("abcdefab-1234-1234-1234-123456789012"), Generation: 1, Finalizers: []string{api.Finalizer}}, Spec: api.WorkspaceSpec{Repository: "steve/other", DesiredState: "Running"}}
	if err := r.Create(ctx, b); err != nil {
		t.Fatal(err)
	}
	for _, w := range []*api.ProjectWorkspace{a, b} {
		if err := r.ensure(ctx, w); err != nil {
			t.Fatal(err)
		}
	}
	if secretData(t, r, b, "model-access") != nil || providerHostsOf(t, r, b) != nil {
		t.Fatal("a project that did not opt in received provider access")
	}
	if proxyCA := secretData(t, r, b, "proxy-ca"); proxyCA["provider-access"] != nil || proxyCA[proxy.BrokeredProviderKey] != nil {
		t.Fatal("another project's proxy received grants")
	}
	// Revoking one project leaves the other untouched.
	b.Spec.Providers = []api.ProviderGrant{{Name: "grok"}}
	if err := r.ensure(ctx, b); err != nil {
		t.Fatal(err)
	}
	a.Spec.Providers = nil
	if err := r.ensure(ctx, a); err != nil {
		t.Fatal(err)
	}
	if g := grantsOf(t, secretData(t, r, b, "model-access")["provider-access"]); !g["grok"].Enabled {
		t.Fatal("revoking one project revoked another")
	}
}

func TestBrokeredGrantsKeepOperatorCodexGrant(t *testing.T) {
	ctx := context.Background()
	r, w := providerFixture(t)
	if err := r.ensure(ctx, w); err != nil {
		t.Fatal(err)
	}
	operator := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "model-access", Namespace: namespace(w), Labels: labels(w)}, Data: map[string][]byte{"access-token": []byte("codex-access"), "chatgpt-account-id": []byte("account"), "expires-at": []byte("9999999999")}}
	if err := r.Create(ctx, operator); err != nil {
		t.Fatal(err)
	}
	w.Spec.Providers = []api.ProviderGrant{{Name: "grok"}}
	if err := r.ensure(ctx, w); err != nil {
		t.Fatal(err)
	}
	model := secretData(t, r, w, "model-access")
	if string(model["access-token"]) != "codex-access" || !grantsOf(t, model["provider-access"])["grok"].Enabled {
		t.Fatal("brokered grant replaced the operator grant", model)
	}
	w.Spec.Providers = nil
	if err := r.ensure(ctx, w); err != nil {
		t.Fatal(err)
	}
	model = secretData(t, r, w, "model-access")
	if string(model["access-token"]) != "codex-access" || model["provider-access"] != nil {
		t.Fatal("opt-out deleted the operator grant or kept the brokered one", model)
	}
}

func TestRefusedPolicyWithdrawsBrokeredProviders(t *testing.T) {
	ctx := context.Background()
	r, w := providerFixture(t, "grok")
	if err := r.ensure(ctx, w); err != nil {
		t.Fatal(err)
	}
	var current api.ProjectWorkspace
	if err := r.Get(ctx, types.NamespacedName{Namespace: "control", Name: w.Name}, &current); err != nil {
		t.Fatal(err)
	}
	current.Spec.Providers = []api.ProviderGrant{{Name: "grok"}, {Name: "muse"}}
	if err := r.Update(ctx, &current); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "control", Name: w.Name}}); err != nil {
		t.Fatal(err)
	}
	if providerHostsOf(t, r, w) != nil || secretData(t, r, w, "model-access") != nil {
		t.Fatal("a refused policy kept provider access")
	}
}

func TestBrokeredOnlyModelAccess(t *testing.T) {
	ok := map[string][]byte{"provider-access": []byte(`{"grok":{"enabled":false}}`)}
	if err := RejectRefreshMaterial(ok); err != nil {
		t.Fatal(err)
	}
	for _, data := range []map[string][]byte{
		{"provider-access": []byte(`{"codex":{"enabled":false}}`)},
		{"provider-access": []byte(`{"grok":{"enabled":true,"access":{"accessToken":"a","expiresAt":1,"issuer":"https://auth.x.ai","refreshToken":"r"}}}`)},
		{"provider-access": []byte(`{"grok":{"enabled":false}}`), "refresh_token": []byte("r")},
	} {
		if RejectRefreshMaterial(data) == nil {
			t.Fatal("accepted", data)
		}
	}
}
