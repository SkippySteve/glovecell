package controller

import (
	"context"
	"strings"
	"testing"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/network"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func lanFixture(t *testing.T) (*Reconciler, *api.ProjectWorkspace) {
	t.Helper()
	r, w, _ := fixture(t)
	lan, err := network.ParseLANConfig("192.168.0.0/16", "192.168.105.0/24,192.168.50.0/28,192.168.110.1")
	if err != nil {
		t.Fatal(err)
	}
	r.LAN = lan
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-a"}, Status: corev1.NodeStatus{Addresses: []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "192.168.120.9"}, {Type: corev1.NodeHostName, Address: "node-a"}}}}
	if err := r.Create(context.Background(), node); err != nil {
		t.Fatal(err)
	}
	return r, w
}

func lanPolicy(t *testing.T, r *Reconciler, w *api.ProjectWorkspace) *unstructured.Unstructured {
	t.Helper()
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(ciliumPolicyGVK)
	err := r.Get(context.Background(), types.NamespacedName{Namespace: namespace(w), Name: network.LANPolicyName}, u)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func policySpec(t *testing.T, r *Reconciler, w *api.ProjectWorkspace, name string) any {
	t.Helper()
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(ciliumPolicyGVK)
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: namespace(w), Name: name}, u); err != nil {
		t.Fatal(err)
	}
	return u.Object["spec"]
}

func auditActions(t *testing.T, r *Reconciler, prefix string) []map[string]string {
	t.Helper()
	var list corev1.ConfigMapList
	if err := r.List(context.Background(), &list, client.InNamespace("control")); err != nil {
		t.Fatal(err)
	}
	var out []map[string]string
	for _, cm := range list.Items {
		if strings.HasPrefix(cm.Data["action"], prefix) {
			out = append(out, cm.Data)
		}
	}
	return out
}

func TestLANHostsCreateUpdateAndRemoveWhileRunning(t *testing.T) {
	ctx := context.Background()
	r, w := lanFixture(t)
	if err := r.ensure(ctx, w); err != nil {
		t.Fatal(err)
	}
	if lanPolicy(t, r, w) != nil {
		t.Fatal("policy created without grants")
	}
	workerBefore := policySpec(t, r, w, "worker")
	proxyBefore := policySpec(t, r, w, "proxy")

	w.Spec.LANHosts = []api.LANHost{{Address: "192.168.110.2", Ports: []int32{22}}}
	if err := r.ensure(ctx, w); err != nil {
		t.Fatal(err)
	}
	got := lanPolicy(t, r, w)
	want := (network.Cilium{}).LANObject(namespace(w), string(w.UID), w.Spec.LANHosts)
	if got == nil || !same(got.Object["spec"], want.Object["spec"]) || got.GetLabels()[api.OwnerLabel] != string(w.UID) || got.GetLabels()[network.LANPolicyLabel] != network.LANPolicyName {
		t.Fatalf("policy = %#v", got)
	}
	egress, _, _ := unstructured.NestedSlice(got.Object, "spec", "egress")
	if len(egress) != 1 || egress[0].(map[string]any)["toCIDR"].([]any)[0] != "192.168.110.2/32" {
		t.Fatalf("egress = %#v", egress)
	}
	if _, found := got.Object["spec"].(map[string]any)["ingress"]; found {
		t.Fatal("LAN grants must not add ingress")
	}
	// Grants never touch the worker or proxy policies: other egress stays exactly as restricted.
	if !same(policySpec(t, r, w, "worker"), workerBefore) || !same(policySpec(t, r, w, "proxy"), proxyBefore) {
		t.Fatal("worker or proxy policy changed")
	}
	// The project keeps running: grants need no pause.
	if w.Spec.DesiredState != "Running" {
		t.Fatal("fixture must stay running")
	}

	w.Spec.LANHosts = append(w.Spec.LANHosts, api.LANHost{Address: "192.168.120.40", Ports: []int32{8080, 443}})
	if err := r.ensure(ctx, w); err != nil {
		t.Fatal(err)
	}
	egress, _, _ = unstructured.NestedSlice(lanPolicy(t, r, w).Object, "spec", "egress")
	if len(egress) != 2 {
		t.Fatalf("update not applied: %#v", egress)
	}
	applied := auditActions(t, r, "lan-hosts-applied-")
	if len(applied) != 2 || !strings.Contains(applied[0]["lanHosts"]+applied[1]["lanHosts"], "192.168.120.40 tcp/8080,443") {
		t.Fatalf("applied audit = %#v", applied)
	}
	// Reconciling the same grants again writes nothing new.
	if err := r.ensure(ctx, w); err != nil {
		t.Fatal(err)
	}
	if n := len(auditActions(t, r, "lan-hosts-applied-")); n != 2 {
		t.Fatalf("idempotent reconcile wrote %d records", n)
	}

	w.Spec.LANHosts = nil
	if err := r.ensure(ctx, w); err != nil {
		t.Fatal(err)
	}
	if lanPolicy(t, r, w) != nil {
		t.Fatal("removing every grant must delete the policy")
	}
}

func TestLANHostsOverwriteTamperingAndRefuseForeignPolicy(t *testing.T) {
	ctx := context.Background()
	r, w := lanFixture(t)
	w.Spec.LANHosts = []api.LANHost{{Address: "192.168.110.2", Ports: []int32{22}}}
	if err := r.ensure(ctx, w); err != nil {
		t.Fatal(err)
	}
	// Someone widens the policy in place; the controller restores the granted rule.
	p := lanPolicy(t, r, w)
	_ = unstructured.SetNestedSlice(p.Object, []any{map[string]any{"toEntities": []any{"world"}}}, "spec", "egress")
	if err := r.Update(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := r.ensure(ctx, w); err != nil {
		t.Fatal(err)
	}
	want := (network.Cilium{}).LANObject(namespace(w), string(w.UID), w.Spec.LANHosts)
	if !same(lanPolicy(t, r, w).Object["spec"], want.Object["spec"]) {
		t.Fatal("tampered policy kept")
	}
	// A policy of the same name that this project does not own is never adopted or deleted.
	p = lanPolicy(t, r, w)
	p.SetLabels(map[string]string{api.OwnerLabel: "someone-else"})
	if err := r.Update(ctx, p); err != nil {
		t.Fatal(err)
	}
	w.Spec.LANHosts = nil
	if err := r.ensure(ctx, w); err == nil || !strings.Contains(err.Error(), "ownership") {
		t.Fatalf("foreign policy adopted: %v", err)
	}
	if lanPolicy(t, r, w) == nil {
		t.Fatal("foreign policy deleted")
	}
}

func TestLANHostsRefusedGrantBlocksProjectAndWithdrawsPolicy(t *testing.T) {
	ctx := context.Background()
	r, w := lanFixture(t)
	w.Spec.LANHosts = []api.LANHost{{Address: "192.168.110.2", Ports: []int32{22}}}
	if err := r.Update(ctx, w); err != nil {
		t.Fatal(err)
	}
	if err := r.ensure(ctx, w); err != nil {
		t.Fatal(err)
	}
	if lanPolicy(t, r, w) == nil {
		t.Fatal("policy missing")
	}
	// The operator narrows the LAN ranges: the existing grant is now refused.
	narrowed, err := network.ParseLANConfig("192.168.120.0/24", "192.168.105.0/24")
	if err != nil {
		t.Fatal(err)
	}
	r.LAN = narrowed
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "control", Name: w.Name}}); err != nil {
		t.Fatal(err)
	}
	var got api.ProjectWorkspace
	if err := r.Get(ctx, client.ObjectKeyFromObject(w), &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Phase != "Blocked" || !strings.Contains(got.Status.Conditions[0].Message, "outside the allowed LAN ranges") {
		t.Fatalf("status = %#v", got.Status)
	}
	if lanPolicy(t, r, w) != nil {
		t.Fatal("refused grant still enforced")
	}
	if len(auditActions(t, r, "lan-hosts-withdrawn")) != 1 {
		t.Fatal("withdrawal not audited")
	}
	// Disabling the feature refuses every remaining grant the same way.
	r.LAN = network.LANConfig{}
	if err := r.validate(ctx, &got); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("disabled feature accepted grants: %v", err)
	}
}

func TestLANHostsRefuseNodeAddressesAndUnsupportedProviders(t *testing.T) {
	ctx := context.Background()
	r, w := lanFixture(t)
	// 192.168.120.9 is inside the allowed range and missing from the denylist, but it is a node.
	w.Spec.LANHosts = []api.LANHost{{Address: "192.168.120.9", Ports: []int32{22}}}
	if err := r.validate(ctx, w); err != nil {
		t.Fatal("static check cannot know node addresses:", err)
	}
	if err := r.ensure(ctx, w); err == nil || !strings.Contains(err.Error(), "node address") {
		t.Fatalf("node address granted: %v", err)
	}
	if lanPolicy(t, r, w) != nil {
		t.Fatal("node address policy created")
	}
	useServiceProxy(r)
	w.Spec.LANHosts = []api.LANHost{{Address: "192.168.110.2", Ports: []int32{22}}}
	if err := r.validate(ctx, w); err == nil || !strings.Contains(err.Error(), "cannot enforce") {
		t.Fatalf("unsupported provider accepted grants: %v", err)
	}
}

// A provider without LAN support never touches the Cilium API, which may not exist.
func TestLANHostsSkipCiliumAPIForOtherProviders(t *testing.T) {
	ctx := context.Background()
	r, w := lanFixture(t)
	r.Network = noCilium{}
	r.Client = interceptor.NewClient(r.Client.(client.WithWatch), interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
		if obj.GetObjectKind().GroupVersionKind().Group == "cilium.io" {
			t.Fatal("Cilium API used by another provider")
		}
		return c.Get(ctx, key, obj, opts...)
	}})
	if err := r.applyLANHosts(ctx, w); err != nil {
		t.Fatal(err)
	}
	if err := r.removeLANPolicy(ctx, w); err != nil {
		t.Fatal(err)
	}
	w.Spec.LANHosts = []api.LANHost{{Address: "192.168.110.2", Ports: []int32{22}}}
	if err := r.applyLANHosts(ctx, w); err == nil {
		t.Fatal("grant accepted by a provider that cannot enforce it")
	}
}

type noCilium struct{ serviceProxy }

func (noCilium) Objects(string, string, []string) []*unstructured.Unstructured { return nil }
