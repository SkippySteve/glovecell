package controller

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/proxy"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const referenceDoc = `{"version":1,"digest":"0123456789ab","projects":[
{"name":"cilium","source":"git:reference.yaml#cilium","repos":[{"github":"cilium/cilium"}],"docs":[{"domain":"cilium.io","wildcard":true}]}]}`

func proxyPolicy(t *testing.T, r *Reconciler, w *api.ProjectWorkspace) (string, proxy.Config) {
	t.Helper()
	var cm corev1.ConfigMap
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: namespace(w), Name: "proxy-policy"}, &cm); err != nil {
		t.Fatal(err)
	}
	var cfg proxy.Config
	if err := json.Unmarshal([]byte(cm.Data["policy.json"]), &cfg); err != nil {
		t.Fatal(err)
	}
	return cm.Data["policy.json"], cfg
}

func proxyNetworkPolicy(t *testing.T, r *Reconciler, w *api.ProjectWorkspace) *unstructured.Unstructured {
	t.Helper()
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(ciliumPolicyGVK)
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: namespace(w), Name: "proxy"}, u); err != nil {
		t.Fatal(err)
	}
	return u
}

func runningProxyPod(t *testing.T, r *Reconciler, w *api.ProjectWorkspace) *corev1.Pod {
	t.Helper()
	l := labels(w)
	l["projects.agentwork.dev/role"] = "proxy"
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "proxy-1", Namespace: namespace(w), Labels: l}}
	if err := r.Create(context.Background(), pod); err != nil {
		t.Fatal(err)
	}
	return pod
}

func TestReferenceRulesJoinRunningProjects(t *testing.T) {
	ctx := context.Background()
	r, w, _ := fixture(t)
	if err := r.ensure(ctx, w); err != nil {
		t.Fatal(err)
	}
	beforeRaw, _ := proxyPolicy(t, r, w)
	beforeNet := proxyNetworkPolicy(t, r, w)
	pod := runningProxyPod(t, r, w)

	// The controller restarts with a reference list: the running project
	// gets it without a pause, as labelled rules after its own.
	rules, digest, err := proxy.ReferenceRules([]byte(referenceDoc))
	if err != nil {
		t.Fatal(err)
	}
	r.Config.ReferenceRules, r.Config.ReferenceDigest = rules, digest
	if err = r.ensure(ctx, w); err != nil {
		t.Fatal("reference rules refused for a running project", err)
	}
	raw, cfg := proxyPolicy(t, r, w)
	if !strings.Contains(raw, `"source":"git:reference.yaml#cilium"`) {
		t.Fatal("reference rules not appended after the project's rules", raw)
	}
	labelled := 0
	for i, rule := range cfg.Rules {
		if rule.Source != "" {
			labelled++
		} else if labelled > 0 {
			t.Fatal("a project rule follows a reference rule", i)
		}
	}
	if labelled != len(rules) || !proxy.Allowed(cfg, "docs.cilium.io", "GET", "/en/stable/") || proxy.Allowed(cfg, "docs.cilium.io", "POST", "/") {
		t.Fatal("reference rules not in force", labelled, len(rules))
	}
	netRaw, _ := json.Marshal(proxyNetworkPolicy(t, r, w).Object["spec"])
	for _, want := range []string{`{"matchPattern":"*.cilium.io"}`, `{"matchName":"cilium.io"}`, `{"matchName":"api.github.com"}`} {
		if !strings.Contains(string(netRaw), want) {
			t.Fatal("network policy does not admit", want)
		}
	}
	// Once in the DNS rule and once in toFQDNs, however many projects use it.
	if strings.Count(string(netRaw), `{"matchName":"github.com"}`) != 2 {
		t.Fatal("reference hosts repeated in the network policy")
	}
	if err = r.Get(ctx, client.ObjectKeyFromObject(pod), pod); err == nil {
		t.Fatal("proxy not restarted to read the new rules")
	}
	var audit corev1.ConfigMap
	if err = r.Get(ctx, types.NamespacedName{Namespace: "control", Name: "audit-12345678123412341234123456789012-1-reference-rules-0123456789ab"}, &audit); err != nil {
		t.Fatal("no audit record", err)
	}

	// Unchanged list: nothing changes, nothing restarts.
	pod = runningProxyPod(t, r, w)
	if err = r.ensure(ctx, w); err != nil {
		t.Fatal(err)
	}
	if err = r.Get(ctx, client.ObjectKeyFromObject(pod), pod); err != nil {
		t.Fatal("proxy restarted for an unchanged list")
	}

	// A paused access change still works with the list in place.
	w.Spec.DesiredState = "Paused"
	w.Generation++
	if err = r.Update(ctx, w); err != nil {
		t.Fatal(err)
	}
	if err = r.Delete(ctx, pod); err != nil {
		t.Fatal(err)
	}
	if err = r.ensure(ctx, w); err != nil {
		t.Fatal(err)
	}
	w.Spec.HTTPS = []api.HTTPSRule{{Host: "api.example.com", Methods: []string{"GET"}, Paths: []string{"/v1"}}}
	w.Generation++
	if err = r.Update(ctx, w); err != nil {
		t.Fatal(err)
	}
	if err = r.ensure(ctx, w); err != nil {
		t.Fatal("paused access change with reference rules", err)
	}
	_, cfg = proxyPolicy(t, r, w)
	if !proxy.Allowed(cfg, "api.example.com", "GET", "/v1/x") || !proxy.Allowed(cfg, "docs.cilium.io", "GET", "/") {
		t.Fatal("project and reference rules not both in force")
	}

	// Removing the list removes exactly its rules.
	w.Spec.HTTPS = nil
	w.Generation++
	if err = r.Update(ctx, w); err != nil {
		t.Fatal(err)
	}
	if err = r.ensure(ctx, w); err != nil {
		t.Fatal(err)
	}
	r.Config.ReferenceRules, r.Config.ReferenceDigest = nil, ""
	if err = r.ensure(ctx, w); err != nil {
		t.Fatal(err)
	}
	if raw, _ = proxyPolicy(t, r, w); raw != beforeRaw {
		t.Fatal("policy not back to the project's own rules", raw)
	}
	if !same(proxyNetworkPolicy(t, r, w).Object["spec"], beforeNet.Object["spec"]) {
		t.Fatal("network policy not back to the project's own hosts")
	}
}

func TestReferenceRulesRefuseDriftAndProjectLabels(t *testing.T) {
	ctx := context.Background()
	r, w, _ := fixture(t)
	if err := r.ensure(ctx, w); err != nil {
		t.Fatal(err)
	}
	// A tampered network policy is not adopted by a reference update.
	u := proxyNetworkPolicy(t, r, w)
	u.Object["spec"].(map[string]any)["egress"] = []any{map[string]any{"toCIDR": []any{"0.0.0.0/0"}}}
	if err := r.Update(ctx, u); err != nil {
		t.Fatal(err)
	}
	rules, digest, _ := proxy.ReferenceRules([]byte(referenceDoc))
	r.Config.ReferenceRules, r.Config.ReferenceDigest = rules, digest
	if err := r.ensure(ctx, w); err == nil || !strings.Contains(err.Error(), "drift") {
		t.Fatal("reference update adopted a tampered network policy", err)
	}
	// A project's own rules cannot carry a source label.
	w.Spec.HTTPS = []api.HTTPSRule{{Host: "docs.example.com", Methods: []string{"GET"}, Paths: []string{"/"}, Source: "git:x#y"}}
	if _, err := r.policyConfiguration(context.Background(), w, w.Spec.HTTPS); err == nil {
		t.Fatal("project rule with a source label accepted")
	}
}
