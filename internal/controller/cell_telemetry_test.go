package controller

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/network"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

var testScraper = &network.Scraper{Namespace: "openobserve-collector", Selector: map[string]string{"app.kubernetes.io/instance": "gw"}}

func proxyDeployment(t *testing.T, r *Reconciler, ns string) appsv1.Deployment {
	t.Helper()
	var d appsv1.Deployment
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: "proxy"}, &d); err != nil {
		t.Fatal(err)
	}
	return d
}

func cnpSpec(t *testing.T, r *Reconciler, ns, name string) string {
	t.Helper()
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(schema.GroupVersionKind{Group: "cilium.io", Version: "v2", Kind: "CiliumNetworkPolicy"})
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: name}, u); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(u.Object["spec"])
	return string(b)
}

func workerEnvJSON(t *testing.T, r *Reconciler, ns string) string {
	t.Helper()
	sandbox := &unstructured.Unstructured{}
	sandbox.SetGroupVersionKind(sandboxGVK)
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: "project"}, sandbox); err != nil {
		t.Fatal(err)
	}
	containers, _, _ := unstructured.NestedSlice(sandbox.Object, "spec", "podTemplate", "spec", "containers")
	b, _ := json.Marshal(containers[0].(map[string]any)["env"])
	return string(b)
}

func relayOf(d appsv1.Deployment) bool {
	for _, c := range d.Spec.Template.Spec.Containers {
		if c.Name == relayContainer {
			return true
		}
	}
	return false
}

func TestNewCellGetsRelayRulesAndWorkerSettings(t *testing.T) {
	ctx := context.Background()
	r, w, _ := fixture(t)
	r.Network = provider{network.Cilium{Telemetry: testScraper}}
	if err := r.ensure(ctx, w); err != nil {
		t.Fatal(err)
	}
	ns := namespace(w)
	d := proxyDeployment(t, r, ns)
	if !relayOf(d) || d.Spec.Template.Annotations["prometheus.io/port"] != "9464" {
		t.Fatalf("relay or scrape annotations missing: %+v", d.Spec.Template)
	}
	for _, c := range d.Spec.Template.Spec.Containers {
		if c.SecurityContext == nil || c.SecurityContext.ReadOnlyRootFilesystem == nil || !*c.SecurityContext.ReadOnlyRootFilesystem {
			t.Fatalf("container %s lacks the proxy's security context", c.Name)
		}
	}
	env := workerEnvJSON(t, r, ns)
	if !strings.Contains(env, `"OTEL_EXPORTER_OTLP_ENDPOINT","value":"http://proxy:4318"`) || !strings.Contains(env, `"OTEL_TRACES_EXPORTER","value":"none"`) {
		t.Fatalf("worker telemetry settings missing: %s", env)
	}
	if spec := cnpSpec(t, r, ns, "proxy"); !strings.Contains(spec, `"port":"9464"`) {
		t.Fatalf("proxy policy lacks the scrape rule: %s", spec)
	}
	// Off by default: none of it.
	r2, w2, _ := fixture(t)
	if err := r2.ensure(ctx, w2); err != nil {
		t.Fatal(err)
	}
	if relayOf(proxyDeployment(t, r2, namespace(w2))) || strings.Contains(workerEnvJSON(t, r2, namespace(w2)), "OTEL_") || strings.Contains(cnpSpec(t, r2, namespace(w2), "worker"), "4318") {
		t.Fatal("cell telemetry present while switched off")
	}
}

func TestSwitchingTelemetryMigratesExistingCellsWithoutWorkerRestart(t *testing.T) {
	ctx := context.Background()
	r, w, _ := fixture(t)
	if err := r.ensure(ctx, w); err != nil {
		t.Fatal(err)
	}
	ns := namespace(w)
	envBefore := workerEnvJSON(t, r, ns)

	r.Network = provider{network.Cilium{Telemetry: testScraper}}
	if err := r.ensure(ctx, w); err != nil {
		t.Fatalf("switching on failed for an existing cell: %v", err)
	}
	if !relayOf(proxyDeployment(t, r, ns)) || !strings.Contains(cnpSpec(t, r, ns, "worker"), "4318") || !strings.Contains(cnpSpec(t, r, ns, "proxy"), "9464") {
		t.Fatal("existing cell not migrated to cell telemetry")
	}
	if workerEnvJSON(t, r, ns) != envBefore {
		t.Fatal("switching on rewrote a running worker's template")
	}
	if err := r.ensure(ctx, w); err != nil {
		t.Fatalf("second reconcile after migration: %v", err)
	}

	r.Network = provider{}
	if err := r.ensure(ctx, w); err != nil {
		t.Fatalf("switching off failed: %v", err)
	}
	d := proxyDeployment(t, r, ns)
	if relayOf(d) || d.Spec.Template.Annotations["prometheus.io/scrape"] != "" || strings.Contains(cnpSpec(t, r, ns, "proxy"), "9464") || strings.Contains(cnpSpec(t, r, ns, "worker"), "4318") {
		t.Fatal("switching off left telemetry behind")
	}
	for _, c := range d.Spec.Template.Spec.Containers {
		for _, e := range c.Env {
			if e.Name == "APC_PROXY_METRICS_ADDR" {
				t.Fatal("proxy metrics listener left on")
			}
		}
	}
}

func TestTamperedRelayIsDrift(t *testing.T) {
	ctx := context.Background()
	r, w, _ := fixture(t)
	r.Network = provider{network.Cilium{Telemetry: testScraper}}
	if err := r.ensure(ctx, w); err != nil {
		t.Fatal(err)
	}
	d := proxyDeployment(t, r, namespace(w))
	for i := range d.Spec.Template.Spec.Containers {
		if d.Spec.Template.Spec.Containers[i].Name == relayContainer {
			d.Spec.Template.Spec.Containers[i].Args = []string{"proxy"}
		}
	}
	if err := r.Update(ctx, &d); err != nil {
		t.Fatal(err)
	}
	if err := r.ensure(ctx, w); err == nil || !strings.Contains(err.Error(), "proxy deployment drift") {
		t.Fatalf("tampered relay accepted: %v", err)
	}
}

func TestNonTelemetryPolicyDriftStillRefused(t *testing.T) {
	ctx := context.Background()
	r, w, _ := fixture(t)
	r.Network = provider{network.Cilium{Telemetry: testScraper}}
	if err := r.ensure(ctx, w); err != nil {
		t.Fatal(err)
	}
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(schema.GroupVersionKind{Group: "cilium.io", Version: "v2", Kind: "CiliumNetworkPolicy"})
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace(w), Name: "worker"}, u); err != nil {
		t.Fatal(err)
	}
	egress, _, _ := unstructured.NestedSlice(u.Object, "spec", "egress")
	egress = append(egress, map[string]any{"toEntities": []any{"world"}, "toPorts": []any{map[string]any{"ports": []any{map[string]any{"port": "4318", "protocol": "TCP"}}}}})
	_ = unstructured.SetNestedSlice(u.Object, egress, "spec", "egress")
	if err := r.Update(ctx, u); err != nil {
		t.Fatal(err)
	}
	// A world rule on a telemetry port is stripped like a telemetry rule, so
	// the strip comparison passes; the patch must still restore the exact spec.
	if err := r.ensure(ctx, w); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(cnpSpec(t, r, namespace(w), "worker"), "world") {
		t.Fatal("extra world egress on a telemetry port survived reconcile")
	}
	// Any other drift is still an error.
	_ = r.Get(ctx, types.NamespacedName{Namespace: namespace(w), Name: "worker"}, u)
	egress, _, _ = unstructured.NestedSlice(u.Object, "spec", "egress")
	egress = append(egress, map[string]any{"toEntities": []any{"world"}})
	_ = unstructured.SetNestedSlice(u.Object, egress, "spec", "egress")
	_ = r.Update(ctx, u)
	if err := r.ensure(ctx, w); err == nil || !strings.Contains(err.Error(), "network policy drift") {
		t.Fatalf("non-telemetry policy drift accepted: %v", err)
	}
}
