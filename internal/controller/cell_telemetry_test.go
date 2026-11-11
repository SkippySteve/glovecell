package controller

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

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

func runningWorker(t *testing.T, r *Reconciler, w *api.ProjectWorkspace) *corev1.Pod {
	t.Helper()
	// The cell's resolver, so the DNS migration does not restart it.
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "project", Namespace: namespace(w), Labels: map[string]string{api.OwnerLabel: string(w.UID), "projects.agentwork.dev/role": "worker"}},
		Spec: corev1.PodSpec{DNSConfig: &corev1.PodDNSConfig{Nameservers: []string{"10.96.0.10"}}}}
	if err := r.Create(context.Background(), pod); err != nil {
		t.Fatal(err)
	}
	return pod
}

func TestSwitchingTelemetryMigratesExistingCellsWithoutWorkerRestart(t *testing.T) {
	ctx := context.Background()
	r, w, _ := fixture(t)
	if err := r.ensure(ctx, w); err != nil {
		t.Fatal(err)
	}
	ns := namespace(w)
	envBefore := workerEnvJSON(t, r, ns)
	worker := runningWorker(t, r, w)

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
	// Pause: the worker stops, and the settings arrive without an image change.
	if err := r.Delete(ctx, worker); err != nil {
		t.Fatal(err)
	}
	if err := r.ensure(ctx, w); err != nil {
		t.Fatal(err)
	}
	if env := workerEnvJSON(t, r, ns); !strings.Contains(env, "OTEL_EXPORTER_OTLP_ENDPOINT") {
		t.Fatalf("telemetry settings not applied while the worker was stopped: %s", env)
	}

	worker = runningWorker(t, r, w)
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
	if !strings.Contains(workerEnvJSON(t, r, ns), "OTEL_") {
		t.Fatal("switching off rewrote a running worker's template")
	}
	_ = r.Delete(ctx, worker)
	if err := r.ensure(ctx, w); err != nil {
		t.Fatal(err)
	}
	if workerEnvJSON(t, r, ns) != envBefore {
		t.Fatal("telemetry settings not removed while the worker was stopped")
	}
}

func TestTamperedWorkerTelemetrySettingsAreDrift(t *testing.T) {
	ctx := context.Background()
	r, w, _ := fixture(t)
	r.Network = provider{network.Cilium{Telemetry: testScraper}}
	if err := r.ensure(ctx, w); err != nil {
		t.Fatal(err)
	}
	sandbox := &unstructured.Unstructured{}
	sandbox.SetGroupVersionKind(sandboxGVK)
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace(w), Name: "project"}, sandbox); err != nil {
		t.Fatal(err)
	}
	containers, _, _ := unstructured.NestedSlice(sandbox.Object, "spec", "podTemplate", "spec", "containers")
	c := containers[0].(map[string]any)
	for _, e := range c["env"].([]any) {
		if m := e.(map[string]any); m["name"] == "OTEL_EXPORTER_OTLP_ENDPOINT" {
			m["value"] = "http://evil.example:4318"
		}
	}
	_ = unstructured.SetNestedSlice(sandbox.Object, containers, "spec", "podTemplate", "spec", "containers")
	if err := r.Update(ctx, sandbox); err != nil {
		t.Fatal(err)
	}
	if err := r.ensure(ctx, w); err == nil || !strings.Contains(err.Error(), "telemetry settings drift") {
		t.Fatalf("tampered worker telemetry settings accepted: %v", err)
	}
}

func TestRelayAndMetricsAddressTampering(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name   string
		mutate func(*appsv1.Deployment)
		repair bool // true: restored; false: refused as drift
	}{
		{"args", func(d *appsv1.Deployment) { relay(d).Args = []string{"proxy"} }, false},
		{"added command", func(d *appsv1.Deployment) { relay(d).Command = []string{"/bin/sh", "-c", "id"} }, false},
		{"added env", func(d *appsv1.Deployment) {
			relay(d).Env = append(relay(d).Env, corev1.EnvVar{Name: "CELL_TELEMETRY_PROXY_METRICS", Value: "off"})
		}, false},
		{"added mount", func(d *appsv1.Deployment) {
			relay(d).VolumeMounts = append(relay(d).VolumeMounts, corev1.VolumeMount{Name: "tls", MountPath: "/tls"})
		}, false},
		{"image only", func(d *appsv1.Deployment) { relay(d).Image = "evil.example/relay@sha256:" + strings.Repeat("b", 64) }, true},
		{"metrics address emptied", func(d *appsv1.Deployment) { setProxyEnv(d, "APC_PROXY_METRICS_ADDR", "") }, true},
		{"metrics address on the control port", func(d *appsv1.Deployment) { setProxyEnv(d, "APC_PROXY_METRICS_ADDR", "127.0.0.1:3129") }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, w, _ := fixture(t)
			r.Network = provider{network.Cilium{Telemetry: testScraper}}
			if err := r.ensure(ctx, w); err != nil {
				t.Fatal(err)
			}
			want := proxyDeployment(t, r, namespace(w))
			d := want.DeepCopy()
			tc.mutate(d)
			if err := r.Update(ctx, d); err != nil {
				t.Fatal(err)
			}
			err := r.ensure(ctx, w)
			if !tc.repair {
				if err == nil || !strings.Contains(err.Error(), "proxy deployment drift") {
					t.Fatalf("tampering accepted: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got := proxyDeployment(t, r, namespace(w))
			if !reflect.DeepEqual(got.Spec.Template.Spec.Containers, want.Spec.Template.Spec.Containers) {
				t.Fatalf("not restored:\n got %+v\nwant %+v", got.Spec.Template.Spec.Containers, want.Spec.Template.Spec.Containers)
			}
		})
	}
}

func relay(d *appsv1.Deployment) *corev1.Container {
	for i := range d.Spec.Template.Spec.Containers {
		if d.Spec.Template.Spec.Containers[i].Name == relayContainer {
			return &d.Spec.Template.Spec.Containers[i]
		}
	}
	panic("no relay")
}

func setProxyEnv(d *appsv1.Deployment, name, value string) {
	for i := range d.Spec.Template.Spec.Containers {
		c := &d.Spec.Template.Spec.Containers[i]
		if c.Name != "proxy" {
			continue
		}
		for j := range c.Env {
			if c.Env[j].Name == name {
				c.Env[j].Value = value
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
