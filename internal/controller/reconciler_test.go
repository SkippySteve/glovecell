package controller

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/forgejo"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/network"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/registration"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type host struct {
	grantCalls, revokeCalls int
	failGrant, failRevoke   bool
}

func (h *host) Discover(context.Context) ([]forgejo.Repository, error) { return nil, nil }
func (h *host) Grant(context.Context, string, forgejo.Credentials) (string, error) {
	h.grantCalls++
	if h.failGrant {
		return "", errors.New("Forgejo unavailable")
	}
	return "fixture-token", nil
}
func (h *host) Revoke(context.Context, string, forgejo.Credentials) error {
	h.revokeCalls++
	if h.failRevoke {
		return errors.New("Forgejo unavailable")
	}
	return nil
}

type provider struct{ network.Cilium }

func (provider) Check(context.Context, client.Client) error { return nil }
func (provider) DNS(context.Context, client.Client, string) (*corev1.PodDNSConfig, error) {
	return &corev1.PodDNSConfig{Nameservers: []string{"10.96.0.10"}}, nil
}

// mutableDNS lets a test move only the CoreDNS addresses the provider reports.
type mutableDNS struct {
	network.Cilium
	servers []string
	fail    bool
}

func (m *mutableDNS) Check(context.Context, client.Client) error { return nil }
func (m *mutableDNS) DNS(context.Context, client.Client, string) (*corev1.PodDNSConfig, error) {
	if m.fail || len(m.servers) == 0 {
		return nil, errors.New("no ready inspected CoreDNS pod endpoints")
	}
	ndots := "1"
	return &corev1.PodDNSConfig{Nameservers: append([]string{}, m.servers...), Searches: []string{"svc.cluster.local"}, Options: []corev1.PodDNSConfigOption{{Name: "ndots", Value: &ndots}}}, nil
}
func fixture(t *testing.T) (*Reconciler, *api.ProjectWorkspace, *host) {
	t.Helper()
	s := runtime.NewScheme()
	_ = api.AddToScheme(s)
	_ = corev1.AddToScheme(s)
	_ = storagev1.AddToScheme(s)
	_ = appsv1.AddToScheme(s)
	sandbox := &unstructured.Unstructured{}
	sandbox.SetGroupVersionKind(sandboxGVK)
	s.AddKnownTypeWithName(sandboxGVK, sandbox)
	runtimeClass := &unstructured.Unstructured{}
	runtimeClass.SetGroupVersionKind(schema.GroupVersionKind{Group: "node.k8s.io", Version: "v1", Kind: "RuntimeClass"})
	runtimeClass.SetName("gvisor")
	_ = networkingv1.AddToScheme(s)
	_ = rbacv1.AddToScheme(s)
	policy := corev1.PersistentVolumeReclaimDelete
	w := &api.ProjectWorkspace{ObjectMeta: metav1.ObjectMeta{Name: "project", Namespace: "control", UID: types.UID("12345678-1234-1234-1234-123456789012"), Generation: 1, Finalizers: []string{api.Finalizer}}, Spec: api.WorkspaceSpec{Repository: "steve/project", DesiredState: "Running"}}
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(w).WithObjects(w, runtimeClass, &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: "delete"}, ReclaimPolicy: &policy}).Build()
	h := &host{}
	r := &Reconciler{Client: c, Host: h, Network: provider{}, Config: Config{Namespace: "control", Owner: "steve", ForgejoOrigin: "https://git.example.com", Image: "example.com/worker@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", RuntimeClass: "gvisor", StorageClass: "delete", StorageSize: "1Gi"}}
	return r, w, h
}
func TestRecoveryAndRemoval(t *testing.T) {
	ctx := context.Background()
	r, w, h := fixture(t)
	if e := r.ensure(ctx, w); e != nil {
		t.Fatal(e)
	}
	var secret corev1.Secret
	if e := r.Get(ctx, types.NamespacedName{Namespace: "control", Name: identity(w)}, &secret); e != nil {
		t.Fatal(e)
	}
	if len(secret.Data["password"]) == 0 {
		t.Fatal("recovery identity missing")
	}
	// Simulate a fresh controller after provisioning. All objects and credentials survive in the API.
	restarted := *r
	r = &restarted
	if e := r.ensure(ctx, w); e != nil {
		t.Fatal("retry failed", e)
	}
	h.failRevoke = true
	w.Spec.DesiredState = "Removed"
	w.Generation = 2
	if e := r.remove(ctx, w); e == nil {
		t.Fatal("revocation failure treated as cleanup success")
	}
	sandbox := &unstructured.Unstructured{}
	sandbox.SetGroupVersionKind(sandboxGVK)
	if e := r.Get(ctx, types.NamespacedName{Namespace: namespace(w), Name: "project"}, sandbox); e != nil {
		t.Fatal(e)
	}
	mode, _, _ := unstructured.NestedString(sandbox.Object, "spec", "operatingMode")
	if mode != "Suspended" {
		t.Fatal("workload not suspended during Forgejo outage")
	}
	h.failRevoke = false
	if e := r.remove(ctx, w); e == nil {
		t.Fatal("namespace deletion should require observation")
	}
	if e := r.remove(ctx, w); e != nil {
		t.Fatal(e)
	}
	if e := r.Get(ctx, types.NamespacedName{Namespace: "control", Name: identity(w)}, &secret); e != nil {
		t.Fatal(e)
	}
	if len(secret.Data["token"]) != 0 || len(secret.Data["password"]) != 0 || len(secret.Data["ca.key"]) != 0 {
		t.Fatal("control-plane credential material retained after cleanup")
	}
	grants := h.grantCalls
	if e := r.Update(ctx, w); e != nil {
		t.Fatal(e)
	}
	_, e := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "control", Name: w.Name}})
	if e != nil {
		t.Fatal(e)
	}
	if h.grantCalls != grants {
		t.Fatal("removed workspace regranted access")
	}
}
func TestRetainedVolumeBlocksCompletion(t *testing.T) {
	ctx := context.Background()
	r, w, _ := fixture(t)
	if e := r.ensure(ctx, w); e != nil {
		t.Fatal(e)
	}
	if e := r.Create(ctx, &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "remaining"}, Spec: corev1.PersistentVolumeSpec{ClaimRef: &corev1.ObjectReference{Namespace: namespace(w), Name: "workspace"}}}); e != nil {
		t.Fatal(e)
	}
	_ = r.remove(ctx, w)
	if e := r.remove(ctx, w); e == nil {
		t.Fatal("remaining PV treated as removed")
	}
}
func TestRefusesUnownedNamespace(t *testing.T) {
	ctx := context.Background()
	r, w, _ := fixture(t)
	if e := r.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace(w)}}); e != nil {
		t.Fatal(e)
	}
	if e := r.remove(ctx, w); e == nil {
		t.Fatal("unowned namespace deleted")
	}
	var ns corev1.Namespace
	if e := r.Get(ctx, types.NamespacedName{Name: namespace(w)}, &ns); e != nil {
		t.Fatal("unowned namespace disappeared")
	}
}
func TestRetainStorageRejectedBeforeGrant(t *testing.T) {
	ctx := context.Background()
	r, w, h := fixture(t)
	sc := &storagev1.StorageClass{}
	_ = r.Get(ctx, types.NamespacedName{Name: "delete"}, sc)
	retain := corev1.PersistentVolumeReclaimRetain
	sc.ReclaimPolicy = &retain
	_ = r.Update(ctx, sc)
	if e := r.validate(ctx, w); e == nil {
		t.Fatal("retained storage allowed")
	}
	if h.grantCalls != 0 {
		t.Fatal("access granted before validating storage")
	}
}

func TestRemovalRevokesDespiteLocalOwnershipDrift(t *testing.T) {
	ctx := context.Background()
	r, w, h := fixture(t)
	if e := r.ensure(ctx, w); e != nil {
		t.Fatal(e)
	}
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(sandboxGVK)
	key := types.NamespacedName{Namespace: namespace(w), Name: "project"}
	if e := r.Get(ctx, key, u); e != nil {
		t.Fatal(e)
	}
	u.SetLabels(map[string]string{api.OwnerLabel: "someone-else"})
	if e := r.Update(ctx, u); e != nil {
		t.Fatal(e)
	}
	if e := r.remove(ctx, w); e == nil {
		t.Fatal("ownership drift allowed cleanup completion")
	}
	if h.revokeCalls != 1 {
		t.Fatal("local drift blocked external revocation")
	}
	if e := r.Get(ctx, key, u); e != nil {
		t.Fatal("unowned Sandbox deleted")
	}
}

func TestMissingLedgerAfterPriorGenerationGrantBlocksRemoval(t *testing.T) {
	ctx := context.Background()
	r, w, _ := fixture(t)
	if e := r.ensure(ctx, w); e != nil {
		t.Fatal(e)
	}
	s := &corev1.Secret{}
	if e := r.Get(ctx, types.NamespacedName{Namespace: "control", Name: identity(w)}, s); e != nil {
		t.Fatal(e)
	}
	if e := r.Delete(ctx, s); e != nil {
		t.Fatal(e)
	}
	w.Generation = 3
	if e := r.remove(ctx, w); e == nil {
		t.Fatal("missing prior-generation ledger accepted")
	}
	var ns corev1.Namespace
	if e := r.Get(ctx, types.NamespacedName{Name: namespace(w)}, &ns); e != nil {
		t.Fatal("storage deleted without verified revocation")
	}
}

func TestAddedNetworkPermissionRejected(t *testing.T) {
	ctx := context.Background()
	r, w, _ := fixture(t)
	if e := r.ensure(ctx, w); e != nil {
		t.Fatal(e)
	}
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(schema.GroupVersionKind{Group: "cilium.io", Version: "v2", Kind: "CiliumNetworkPolicy"})
	if e := r.Get(ctx, types.NamespacedName{Namespace: namespace(w), Name: "worker"}, u); e != nil {
		t.Fatal(e)
	}
	rules, _, _ := unstructured.NestedSlice(u.Object, "spec", "egress")
	rules[1].(map[string]any)["toEntities"] = []any{"world"}
	_ = unstructured.SetNestedSlice(u.Object, rules, "spec", "egress")
	if e := r.Update(ctx, u); e != nil {
		t.Fatal(e)
	}
	if e := r.ensure(ctx, w); e == nil {
		t.Fatal("added network permission accepted")
	}
}

func TestAddedPodPrivilegesRejected(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*corev1.PodSpec)
	}{
		{"host-network", func(p *corev1.PodSpec) { p.HostNetwork = true }},
		{"host-pid", func(p *corev1.PodSpec) { p.HostPID = true }},
		{"host-ipc", func(p *corev1.PodSpec) { p.HostIPC = true }},
		{"capability", func(p *corev1.PodSpec) {
			p.Containers[0].SecurityContext.Capabilities.Add = []corev1.Capability{"NET_ADMIN"}
		}},
		{"unmasked-proc", func(p *corev1.PodSpec) { v := corev1.UnmaskedProcMount; p.Containers[0].SecurityContext.ProcMount = &v }},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			r, w, _ := fixture(t)
			if e := r.ensure(ctx, w); e != nil {
				t.Fatal(e)
			}
			var deployment appsv1.Deployment
			if e := r.Get(ctx, types.NamespacedName{Namespace: namespace(w), Name: "proxy"}, &deployment); e != nil {
				t.Fatal(e)
			}
			test.change(&deployment.Spec.Template.Spec)
			if e := r.Update(ctx, &deployment); e != nil {
				t.Fatal(e)
			}
			if e := r.ensure(ctx, w); e == nil {
				t.Fatal("added proxy privilege accepted")
			}
		})
	}
}

func TestAddedSandboxHostNetworkingRejected(t *testing.T) {
	ctx := context.Background()
	r, w, _ := fixture(t)
	if e := r.ensure(ctx, w); e != nil {
		t.Fatal(e)
	}
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(sandboxGVK)
	if e := r.Get(ctx, types.NamespacedName{Namespace: namespace(w), Name: "project"}, u); e != nil {
		t.Fatal(e)
	}
	if e := unstructured.SetNestedField(u.Object, true, "spec", "podTemplate", "spec", "hostNetwork"); e != nil {
		t.Fatal(e)
	}
	if e := r.Update(ctx, u); e != nil {
		t.Fatal(e)
	}
	if e := r.ensure(ctx, w); e == nil {
		t.Fatal("Sandbox gained host network access")
	}
}

func TestGrantOutagePreservesLedger(t *testing.T) {
	ctx := context.Background()
	r, w, h := fixture(t)
	h.failGrant = true
	if e := r.ensure(ctx, w); e == nil {
		t.Fatal("grant outage treated as success")
	}
	var secret corev1.Secret
	key := types.NamespacedName{Namespace: "control", Name: identity(w)}
	if e := r.Get(ctx, key, &secret); e != nil {
		t.Fatal(e)
	}
	username := string(secret.Data["username"])
	if username == "" || len(secret.Data["password"]) == 0 || len(secret.Data["token"]) != 0 {
		t.Fatal("ledger missing its password or stored a token during the outage")
	}
	h.failGrant = false
	if e := r.ensure(ctx, w); e != nil {
		t.Fatal(e)
	}
	if e := r.Get(ctx, key, &secret); e != nil {
		t.Fatal(e)
	}
	if string(secret.Data["username"]) != username || len(secret.Data["token"]) == 0 || len(secret.Data["password"]) == 0 {
		t.Fatal("retry replaced the workspace identity")
	}
}

func TestDNSEndpointMigration(t *testing.T) {
	ctx := context.Background()
	r, w, _ := fixture(t)
	dns := &mutableDNS{servers: []string{"10.96.0.10"}}
	r.Network = dns
	if e := r.ensure(ctx, w); e != nil {
		t.Fatal(e)
	}
	ns := namespace(w)
	worker := func(name string, servers []string) *corev1.Pod {
		return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: map[string]string{api.OwnerLabel: string(w.UID), "projects.agentwork.dev/role": "worker"}}, Spec: corev1.PodSpec{DNSPolicy: corev1.DNSNone, DNSConfig: &corev1.PodDNSConfig{Nameservers: servers}, Containers: []corev1.Container{{Name: "workspace", Image: "example.com/worker:test"}}}}
	}
	if e := r.Create(ctx, worker("worker-old", []string{"10.96.0.10"})); e != nil {
		t.Fatal(e)
	}
	if e := r.Create(ctx, worker("worker-current", []string{"10.1.2.3"})); e != nil {
		t.Fatal(e)
	}
	if e := r.Create(ctx, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "proxy-pod", Namespace: ns, Labels: labels(w)}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "proxy", Image: "example.com/worker:test"}}}}); e != nil {
		t.Fatal(e)
	}
	dns.servers = []string{"10.1.2.3"}
	if e := r.ensure(ctx, w); e != nil {
		t.Fatal(e)
	}
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(sandboxGVK)
	if e := r.Get(ctx, types.NamespacedName{Namespace: ns, Name: "project"}, u); e != nil {
		t.Fatal(e)
	}
	got, _, e := unstructured.NestedStringSlice(u.Object, "spec", "podTemplate", "spec", "dnsConfig", "nameservers")
	if e != nil || len(got) != 1 || got[0] != "10.1.2.3" {
		t.Fatalf("sandbox nameservers = %v (%v)", got, e)
	}
	var deployment appsv1.Deployment
	if e := r.Get(ctx, types.NamespacedName{Namespace: ns, Name: "proxy"}, &deployment); e != nil {
		t.Fatal(e)
	}
	if servers := deployment.Spec.Template.Spec.DNSConfig.Nameservers; len(servers) != 1 || servers[0] != "10.1.2.3" {
		t.Fatalf("proxy nameservers = %v", servers)
	}
	var stale corev1.Pod
	if e := r.Get(ctx, types.NamespacedName{Namespace: ns, Name: "worker-old"}, &stale); !apierrors.IsNotFound(e) {
		t.Fatal("stale worker was kept", e)
	}
	if e := r.Get(ctx, types.NamespacedName{Namespace: ns, Name: "worker-current"}, &stale); e != nil {
		t.Fatal("worker that already had the new resolver was deleted", e)
	}
	if e := r.Get(ctx, types.NamespacedName{Namespace: ns, Name: "proxy-pod"}, &stale); e != nil {
		t.Fatal("non-worker pod deleted", e)
	}
	var pvc corev1.PersistentVolumeClaim
	if e := r.Get(ctx, types.NamespacedName{Namespace: ns, Name: "workspace"}, &pvc); e != nil {
		t.Fatal("project volume deleted", e)
	}
	var gitIdentity, ledger corev1.Secret
	if e := r.Get(ctx, types.NamespacedName{Namespace: ns, Name: "git-identity"}, &gitIdentity); e != nil {
		t.Fatal("git identity deleted", e)
	}
	if e := r.Get(ctx, types.NamespacedName{Namespace: "control", Name: identity(w)}, &ledger); e != nil || len(ledger.Data["password"]) == 0 {
		t.Fatal("control ledger deleted", e)
	}
	volumes, _, e := unstructured.NestedSlice(u.Object, "spec", "podTemplate", "spec", "volumes")
	if e != nil {
		t.Fatal(e)
	}
	var model map[string]any
	for _, raw := range volumes {
		volume, _ := raw.(map[string]any)
		if volume["name"] == "model" {
			model = volume
		}
	}
	secret, _ := model["secret"].(map[string]any)
	if secret["secretName"] != "model-access" || secret["optional"] != true {
		t.Fatalf("model volume = %#v", model)
	}
	foundMount := false
	containerMounts, _, e := unstructured.NestedSlice(u.Object, "spec", "podTemplate", "spec", "containers")
	if e != nil || len(containerMounts) == 0 {
		t.Fatal(e)
	}
	container, _ := containerMounts[0].(map[string]any)
	for _, raw := range container["volumeMounts"].([]any) {
		mount, _ := raw.(map[string]any)
		if mount["name"] == "model" && mount["mountPath"] == "/run/model" && mount["readOnly"] == true {
			foundMount = true
		}
	}
	if !foundMount {
		t.Fatal("model credentials are not mounted read-only at /run/model")
	}
	sum := sha256.Sum256([]byte("10.1.2.3"))
	auditName := fmt.Sprintf("audit-%s-%d-dns-%s", strings.ReplaceAll(string(w.UID), "-", ""), w.Generation, hex.EncodeToString(sum[:6]))
	if e := r.Get(ctx, types.NamespacedName{Namespace: "control", Name: auditName}, &corev1.ConfigMap{}); e != nil {
		t.Fatal("dns migration was not audited", e)
	}
	dns.fail = true
	if e := r.Create(ctx, worker("worker-old", []string{"10.96.0.10"})); e != nil {
		t.Fatal(e)
	}
	if e := r.ensure(ctx, w); e == nil {
		t.Fatal("missing CoreDNS endpoints were ignored")
	}
	if e := r.Get(ctx, types.NamespacedName{Namespace: ns, Name: "worker-old"}, &stale); e != nil {
		t.Fatal("endpoint outage deleted the worker", e)
	}
	if e := r.Get(ctx, types.NamespacedName{Namespace: ns, Name: "project"}, u); e != nil {
		t.Fatal(e)
	}
	got, _, _ = unstructured.NestedStringSlice(u.Object, "spec", "podTemplate", "spec", "dnsConfig", "nameservers")
	if len(got) != 1 || got[0] != "10.1.2.3" {
		t.Fatalf("endpoint outage changed nameservers to %v", got)
	}
}

func TestSandboxImageDriftRejected(t *testing.T) {
	ctx := context.Background()
	r, w, _ := fixture(t)
	if e := r.ensure(ctx, w); e != nil {
		t.Fatal(e)
	}
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(sandboxGVK)
	if e := r.Get(ctx, types.NamespacedName{Namespace: namespace(w), Name: "project"}, u); e != nil {
		t.Fatal(e)
	}
	containers, _, e := unstructured.NestedSlice(u.Object, "spec", "podTemplate", "spec", "containers")
	if e != nil || len(containers) == 0 {
		t.Fatal(e)
	}
	container := containers[0].(map[string]any)
	container["image"] = "evil.example/worker:latest"
	containers[0] = container
	if e = unstructured.SetNestedSlice(u.Object, containers, "spec", "podTemplate", "spec", "containers"); e != nil {
		t.Fatal(e)
	}
	if e = r.Update(ctx, u); e != nil {
		t.Fatal(e)
	}
	if e = r.ensure(ctx, w); e == nil {
		t.Fatal("sandbox image drift accepted")
	}
}

func TestCARotationUpdatesTrustAndRestartsPods(t *testing.T) {
	ctx := context.Background()
	r, w, _ := fixture(t)
	if e := r.ensure(ctx, w); e != nil {
		t.Fatal(e)
	}
	var secret corev1.Secret
	if e := r.Get(ctx, types.NamespacedName{Namespace: "control", Name: identity(w)}, &secret); e != nil {
		t.Fatal(e)
	}
	if string(secret.Data["ca-generation"]) != "1" {
		t.Fatalf("generation %s", secret.Data["ca-generation"])
	}
	first := append([]byte(nil), secret.Data["ca.crt"]...)
	ns := namespace(w)
	worker := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "project", Namespace: ns, Labels: map[string]string{api.OwnerLabel: string(w.UID), "projects.agentwork.dev/role": "worker"}}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "workspace", Image: "example"}}}}
	proxyPod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "proxy", Namespace: ns, Labels: map[string]string{api.OwnerLabel: string(w.UID), "projects.agentwork.dev/role": "proxy"}}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "proxy", Image: "example"}}}}
	if e := r.Create(ctx, worker); e != nil {
		t.Fatal(e)
	}
	if e := r.Create(ctx, proxyPod); e != nil {
		t.Fatal(e)
	}
	if w.Annotations == nil {
		w.Annotations = map[string]string{}
	}
	w.Annotations["projects.agentwork.dev/ca-generation"] = "2"
	if e := r.ensure(ctx, w); e != nil {
		t.Fatal(e)
	}
	if e := r.Get(ctx, types.NamespacedName{Namespace: "control", Name: identity(w)}, &secret); e != nil {
		t.Fatal(e)
	}
	if string(secret.Data["ca-generation"]) != "2" || bytes.Equal(first, secret.Data["ca.crt"]) || len(secret.Data["ca.key"]) == 0 {
		t.Fatal("CA was not rotated")
	}
	var trust corev1.ConfigMap
	if e := r.Get(ctx, types.NamespacedName{Namespace: ns, Name: "trust"}, &trust); e != nil {
		t.Fatal(e)
	}
	if trust.Data["ca.crt"] != string(secret.Data["ca.crt"]) {
		t.Fatal("worker trust was not updated")
	}
	if e := r.Get(ctx, types.NamespacedName{Namespace: ns, Name: "project"}, &corev1.Pod{}); !apierrors.IsNotFound(e) {
		t.Fatal("worker was not restarted", e)
	}
	if e := r.Get(ctx, types.NamespacedName{Namespace: ns, Name: "proxy"}, &corev1.Pod{}); !apierrors.IsNotFound(e) {
		t.Fatal("proxy was not restarted", e)
	}
	rotated := append([]byte(nil), secret.Data["ca.crt"]...)
	if e := r.ensure(ctx, w); e != nil {
		t.Fatal(e)
	}
	if e := r.Get(ctx, types.NamespacedName{Namespace: "control", Name: identity(w)}, &secret); e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(rotated, secret.Data["ca.crt"]) {
		t.Fatal("CA rotated again without a new generation")
	}
	w.Annotations["projects.agentwork.dev/ca-generation"] = "4"
	if e := r.ensure(ctx, w); e == nil || !strings.Contains(e.Error(), "increase by one") {
		t.Fatal(e)
	}
}

func TestModelRefreshTokenSuspendsWorkspace(t *testing.T) {
	ctx := context.Background()
	r, w, _ := fixture(t)
	if e := r.ensure(ctx, w); e != nil {
		t.Fatal(e)
	}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "model-access", Namespace: namespace(w), Labels: labels(w)}, Data: map[string][]byte{"refresh-token": []byte("do-not-copy")}}
	if e := r.Create(ctx, secret); e != nil {
		t.Fatal(e)
	}
	if e := r.ensure(ctx, w); e == nil || !strings.Contains(e.Error(), "refresh token") {
		t.Fatal(e)
	}
	secret.Data = validModelAccess()
	if e := r.Update(ctx, secret); e != nil {
		t.Fatal(e)
	}
	if e := r.ensure(ctx, w); e != nil {
		t.Fatal(e)
	}
}

func TestProxyImageRestoredToPin(t *testing.T) {
	ctx := context.Background()
	r, w, _ := fixture(t)
	if e := r.ensure(ctx, w); e != nil {
		t.Fatal(e)
	}
	pinned := "example.com/proxy@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	r.Config.ProxyImage = pinned
	var deploy appsv1.Deployment
	key := types.NamespacedName{Namespace: namespace(w), Name: "proxy"}
	if e := r.Get(ctx, key, &deploy); e != nil {
		t.Fatal(e)
	}
	deploy.Spec.Template.Spec.Containers[0].Image = "evil.example/worker:latest"
	if e := r.Update(ctx, &deploy); e != nil {
		t.Fatal(e)
	}
	if e := r.ensure(ctx, w); e != nil {
		t.Fatal(e)
	}
	if e := r.Get(ctx, key, &deploy); e != nil {
		t.Fatal(e)
	}
	if deploy.Spec.Template.Spec.Containers[0].Image != pinned {
		t.Fatalf("proxy image = %s", deploy.Spec.Template.Spec.Containers[0].Image)
	}
	deploy.Spec.Template.Spec.Containers[0].Args = []string{"shell"}
	if e := r.Update(ctx, &deploy); e != nil {
		t.Fatal(e)
	}
	if e := r.ensure(ctx, w); e == nil || !strings.Contains(e.Error(), "proxy deployment drift") {
		t.Fatal(e)
	}
}

func TestModelGrantExpiryAndRevocationStopAndRecover(t *testing.T) {
	ctx := context.Background()
	r, w, _ := fixture(t)
	if err := r.ensure(ctx, w); err != nil {
		t.Fatal(err)
	}
	grant := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "model-access", Namespace: namespace(w), Labels: labels(w)}, Data: validModelAccess()}
	if err := r.Create(ctx, grant); err != nil {
		t.Fatal(err)
	}
	if err := r.ensure(ctx, w); err != nil {
		t.Fatal(err)
	}
	// The required-grant marker survives a controller restart and Secret deletion.
	restarted := *r
	if err := r.Delete(ctx, grant); err != nil {
		t.Fatal(err)
	}
	request := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: w.Namespace, Name: w.Name}}
	if _, err := restarted.Reconcile(ctx, request); err == nil {
		t.Fatal("revoked grant did not block running workspace")
	}
	sandbox := &unstructured.Unstructured{}
	sandbox.SetGroupVersionKind(sandboxGVK)
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace(w), Name: "project"}, sandbox); err != nil {
		t.Fatal(err)
	}
	replicas, _, _ := unstructured.NestedInt64(sandbox.Object, "spec", "replicas")
	if replicas != 0 {
		t.Fatal("revocation left the worker running")
	}
	grant.ResourceVersion = ""
	grant.Data = validModelAccess()
	if err := r.Create(ctx, grant); err != nil {
		t.Fatal(err)
	}
	if err := restarted.ensure(ctx, w); err != nil {
		t.Fatal("valid renewal did not recover", err)
	}
	grant.Data["expires-at"] = []byte("1")
	if err := r.Update(ctx, grant); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.Reconcile(ctx, request); err == nil {
		t.Fatal("expired grant did not stop workspace")
	}
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace(w), Name: "project"}, sandbox); err != nil {
		t.Fatal(err)
	}
	replicas, _, _ = unstructured.NestedInt64(sandbox.Object, "spec", "replicas")
	if replicas != 0 {
		t.Fatal("expired grant left worker running")
	}
	grant.Data = validModelAccess()
	if err := r.Update(ctx, grant); err != nil {
		t.Fatal(err)
	}
	if err := restarted.ensure(ctx, w); err != nil {
		t.Fatal("expiry renewal did not recover", err)
	}
}

func TestIndependentCodexLedgerSurvivesMissingProjectionAndControllerRestart(t *testing.T) {
	ctx := context.Background()
	r, w, _ := fixture(t)
	r.IndependentCodexWorkspaceUID = string(w.UID)
	if err := r.ensure(ctx, w); err != nil {
		t.Fatal(err)
	}
	data := validModelAccess()
	data["provider-access"] = []byte(`{"codex":{"enabled":false},"cursor":{"enabled":false}}`)
	data["expires-at"] = []byte("1")
	grant := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "model-access", Namespace: namespace(w), Labels: labels(w)}, Data: data}
	if err := r.Create(ctx, grant); err != nil {
		t.Fatal(err)
	}
	if err := r.ensure(ctx, w); err != nil {
		t.Fatal("independent expiry paused workbench", err)
	}
	if err := r.Delete(ctx, grant); err != nil {
		t.Fatal(err)
	}
	restarted := *r
	if err := restarted.ensure(ctx, w); err != nil {
		t.Fatal("independent projection removal paused other providers", err)
	}
	ledger, err := restarted.state(ctx, w)
	if err != nil || string(ledger.Data["independent-model-access"]) != "true" {
		t.Fatal("lost independent lifecycle marker")
	}
	cfg, err := restarted.policyConfiguration(context.Background(), w, w.Spec.HTTPS)
	if err != nil || !cfg.IndependentCodex || !cfg.BrokeredProviders {
		t.Fatal("missing projection disabled inspection denial")
	}
}

func TestRegistrarAccessIsNamespaceScopedAndResetsExtraRules(t *testing.T) {
	ctx := context.Background()
	r, w, _ := fixture(t)
	r.Config.RegistrarNamespace = "control-t3"
	if err := r.ensureRegistrarAccess(ctx, w); err != nil {
		t.Fatal(err)
	}
	role := &rbacv1.Role{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace(w), Name: registrarAccessName}, role); err != nil {
		t.Fatal(err)
	}
	if role.Labels[api.OwnerLabel] != string(w.UID) {
		t.Fatal("role missing owner")
	}
	for _, rule := range role.Rules {
		for _, resource := range rule.Resources {
			if resource == "secrets" || resource == "*" {
				t.Fatal("registrar role can read secrets")
			}
		}
	}
	role.Rules = append(role.Rules, rbacv1.PolicyRule{APIGroups: []string{""}, Resources: []string{"secrets"}, Verbs: []string{"get"}})
	if err := r.Update(ctx, role); err != nil {
		t.Fatal(err)
	}
	if err := r.ensureRegistrarAccess(ctx, w); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace(w), Name: registrarAccessName}, role); err != nil {
		t.Fatal(err)
	}
	for _, rule := range role.Rules {
		for _, resource := range rule.Resources {
			if resource == "secrets" {
				t.Fatal("widened registrar role was kept")
			}
		}
	}
	binding := &rbacv1.RoleBinding{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace(w), Name: registrarAccessName}, binding); err != nil {
		t.Fatal(err)
	}
	if len(binding.Subjects) != 1 || binding.Subjects[0].Name != registrarServiceAccount || binding.Subjects[0].Namespace != "control-t3" {
		t.Fatalf("%+v", binding.Subjects)
	}
}

func TestRegistrarAccessRejectsUnownedRole(t *testing.T) {
	ctx := context.Background()
	r, w, _ := fixture(t)
	r.Config.RegistrarNamespace = "control-t3"
	role := &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: registrarAccessName, Namespace: namespace(w)}, Rules: registrarPodRules()}
	if err := r.Create(ctx, role); err != nil {
		t.Fatal(err)
	}
	if err := r.ensureRegistrarAccess(ctx, w); err == nil {
		t.Fatal("unowned registrar role was adopted")
	}
}

func TestRegistrarNamespaceMustStayOutOfControlNamespace(t *testing.T) {
	cfg := Config{Namespace: "control", Owner: "steve", Image: "example.com/worker@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", RuntimeClass: "gvisor", StorageClass: "delete", StorageSize: "1Gi", RegistrarNamespace: "control"}
	if err := cfg.Validate(); err == nil {
		t.Fatal("registrar namespace matched the control namespace")
	}
}

func TestClientLedgerBlocksRemovalUntilRevoked(t *testing.T) {
	ctx := context.Background()
	r, w, _ := fixture(t)
	if e := r.ensure(ctx, w); e != nil {
		t.Fatal(e)
	}
	ledger := registration.NewLedger()
	_ = ledger.SetClient("t3", registration.StateRegistered, "7dc24d95-2241-4e16-92f4-b8eb0f6004ce", "/mnt/demo")
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      registration.ConfigMapName(string(w.UID)),
			Namespace: "control",
			Labels:    map[string]string{api.OwnerLabel: string(w.UID)},
		},
		Data: registration.EncodeConfigMapData(ledger),
	}
	if e := r.Create(ctx, cm); e != nil {
		t.Fatal(e)
	}
	w.Spec.DesiredState = "Removed"
	w.Generation = 2
	if e := r.remove(ctx, w); e == nil {
		t.Fatal("registered client allowed cleanup")
	}
	var ns corev1.Namespace
	if e := r.Get(ctx, types.NamespacedName{Name: namespace(w)}, &ns); e != nil {
		t.Fatal("namespace deleted while clients registered")
	}
	_ = ledger.SetClient("t3", registration.StateRevoked, "7dc24d95-2241-4e16-92f4-b8eb0f6004ce", "/mnt/demo")
	cm.Data = registration.EncodeConfigMapData(ledger)
	if e := r.Update(ctx, cm); e != nil {
		t.Fatal(e)
	}
	if e := r.remove(ctx, w); e == nil {
		t.Fatal("namespace deletion should require observation")
	}
	if e := r.remove(ctx, w); e != nil {
		t.Fatal(e)
	}
}

func TestMissingClientLedgerDoesNotBlockRemoval(t *testing.T) {
	ctx := context.Background()
	r, w, _ := fixture(t)
	if e := r.ensure(ctx, w); e != nil {
		t.Fatal(e)
	}
	w.Spec.DesiredState = "Removed"
	w.Generation = 2
	if e := r.remove(ctx, w); e == nil {
		t.Fatal("namespace deletion should require observation")
	}
	if e := r.remove(ctx, w); e != nil {
		t.Fatal(e)
	}
}

func TestUnknownClientStateFailsClosed(t *testing.T) {
	ctx := context.Background()
	r, w, _ := fixture(t)
	if e := r.ensure(ctx, w); e != nil {
		t.Fatal(e)
	}
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      registration.ConfigMapName(string(w.UID)),
			Namespace: "control",
			Labels:    map[string]string{api.OwnerLabel: string(w.UID)},
		},
		Data: map[string]string{"purpose": "client-registration", "t3.state": "weird"},
	}
	if e := r.Create(ctx, cm); e != nil {
		t.Fatal(e)
	}
	w.Spec.DesiredState = "Removed"
	w.Generation = 2
	if e := r.remove(ctx, w); e == nil {
		t.Fatal("unknown client state allowed cleanup")
	}
}

func TestDisabledT3ManagerRemovalRequiresRecordedRevocation(t *testing.T) {
	const uid = "12345678-1234-1234-1234-123456789012"
	ledger := func(state string, annotations map[string]string) *corev1.Secret {
		return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "t3-connect-12345678123412341234123456789012", Namespace: "control", Labels: map[string]string{api.OwnerLabel: uid}, Annotations: annotations}, Data: map[string][]byte{"workspace": []byte("project"), "account-id": []byte("account"), "environment-id": []byte(uid), "state": []byte(state)}}
	}
	intent := func(annotations map[string]string) *corev1.ConfigMap {
		return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "t3-connect-12345678123412341234123456789012-intent", Namespace: "control", Labels: map[string]string{api.OwnerLabel: uid}, Annotations: annotations}, Data: map[string]string{"action": "t3-link-requested", "environment-id": uid, "account-id": "account"}}
	}
	revoked := map[string]string{t3RevokedAnnotation: uid}
	unowned := ledger("revoked", nil)
	unowned.Labels = map[string]string{api.OwnerLabel: "someone-else"}
	blank := ledger("linked", map[string]string{t3RevokedAnnotation: ""})
	blank.Data["environment-id"] = nil
	for _, test := range []struct {
		name    string
		objects []client.Object
		blocks  bool
	}{
		{"linked", []client.Object{ledger("linked", nil), intent(nil)}, true},
		{"linking", []client.Object{ledger("linking", nil), intent(nil)}, true},
		{"revoked", []client.Object{ledger("revoked", nil), intent(nil)}, false},
		{"annotation-matching", []client.Object{ledger("linked", revoked), intent(nil)}, false},
		{"annotation-mismatching", []client.Object{ledger("linked", map[string]string{t3RevokedAnnotation: "87654321-4321-4321-4321-210987654321"}), intent(nil)}, true},
		{"annotation-without-environment", []client.Object{blank, intent(nil)}, true},
		{"unowned-ledger", []client.Object{unowned, intent(nil)}, true},
		{"intent-only", []client.Object{intent(nil)}, true},
		{"intent-only-annotated", []client.Object{intent(revoked)}, false},
		{"intent-annotation-mismatching", []client.Object{intent(map[string]string{t3RevokedAnnotation: "other"})}, true},
		// An interrupted cleanup-verified step deletes the intent first and leaves a revoked ledger.
		{"revoked-ledger-after-interrupted-cleanup", []client.Object{ledger("revoked", nil)}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			r, w, _ := fixture(t)
			if e := r.ensure(ctx, w); e != nil {
				t.Fatal(e)
			}
			for _, o := range test.objects {
				if e := r.Create(ctx, o); e != nil {
					t.Fatal(e)
				}
			}
			w.Spec.DesiredState = "Removed"
			w.Generation = 2
			first := r.remove(ctx, w)
			if test.blocks {
				if first == nil || r.remove(ctx, w) == nil {
					t.Fatal("unrevoked T3 registration allowed cleanup")
				}
				if !strings.Contains(first.Error(), t3RevokedAnnotation) && !strings.Contains(first.Error(), "ownership mismatch") {
					t.Fatalf("error does not name the recovery annotation: %v", first)
				}
				var ns corev1.Namespace
				if e := r.Get(ctx, types.NamespacedName{Name: namespace(w)}, &ns); e != nil {
					t.Fatal("namespace deleted before T3 revocation was recorded")
				}
				for _, o := range test.objects {
					if e := r.Get(ctx, client.ObjectKeyFromObject(o), o); e != nil {
						t.Fatalf("T3 history %s deleted while blocked: %v", o.GetName(), e)
					}
				}
				return
			}
			if first == nil || !strings.Contains(first.Error(), "waiting for namespace") {
				t.Fatalf("expected namespace observation, got %v", first)
			}
			for _, o := range test.objects {
				if e := r.Get(ctx, client.ObjectKeyFromObject(o), o); e != nil {
					t.Fatalf("T3 history %s deleted before cleanup-verified: %v", o.GetName(), e)
				}
			}
			if e := r.remove(ctx, w); e != nil {
				t.Fatal(e)
			}
			for _, o := range test.objects {
				if e := r.Get(ctx, client.ObjectKeyFromObject(o), o); !apierrors.IsNotFound(e) {
					t.Fatalf("revoked T3 history %s retained after cleanup-verified: %v", o.GetName(), e)
				}
			}
			if e := r.remove(ctx, w); e != nil {
				t.Fatal("retry after cleanup-verified failed", e)
			}
		})
	}
}
