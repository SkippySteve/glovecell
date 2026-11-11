package controller

import (
	"strconv"

	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/cellrelay"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/network"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/proxy"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// Cell telemetry (decision GT A): a relay container in the proxy pod takes
// the worker agents' OTLP and serves reduced counters for the collector to
// scrape. Nothing in a cell pushes telemetry out.
const relayContainer = "telemetry-relay"

var scrapeAnnotations = map[string]string{
	"prometheus.io/scrape": "true",
	"prometheus.io/port":   strconv.Itoa(network.TelemetryMetricsPort),
	"prometheus.io/path":   "/metrics",
}

// telemetryEnv points the worker's coding agents at the relay. Only metrics
// and events are exported; traces are off, and the relay drops them anyway.
// NO_PROXY keeps that one plain-HTTP call off the inspecting proxy port.
var telemetryEnv = []corev1.EnvVar{
	{Name: "NO_PROXY", Value: "proxy"},
	{Name: "no_proxy", Value: "proxy"},
	{Name: "OTEL_EXPORTER_OTLP_ENDPOINT", Value: "http://proxy:" + strconv.Itoa(network.TelemetryIntakePort)},
	{Name: "OTEL_EXPORTER_OTLP_PROTOCOL", Value: "http/protobuf"},
	{Name: "OTEL_METRICS_EXPORTER", Value: "otlp"},
	{Name: "OTEL_LOGS_EXPORTER", Value: "otlp"},
	{Name: "OTEL_TRACES_EXPORTER", Value: "none"},
	{Name: "OTEL_EXPORTER_OTLP_METRICS_TEMPORALITY_PREFERENCE", Value: "delta"},
	{Name: "CLAUDE_CODE_ENABLE_TELEMETRY", Value: "1"},
}

func isTelemetryEnv(name string) bool {
	for _, e := range telemetryEnv {
		if e.Name == name {
			return true
		}
	}
	return false
}

// addCellTelemetry adds the relay container, the proxy's loopback metrics
// listener and the scrape annotations to a proxy Deployment.
func addCellTelemetry(d *appsv1.Deployment, image string, security *corev1.SecurityContext) {
	pod := &d.Spec.Template
	for i := range pod.Spec.Containers {
		if pod.Spec.Containers[i].Name == "proxy" {
			pod.Spec.Containers[i].Env = append(pod.Spec.Containers[i].Env, corev1.EnvVar{Name: proxy.MetricsAddressEnv, Value: cellrelay.ProxyMetricsAddress})
		}
	}
	resources := corev1.ResourceRequirements{
		Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("10m"), corev1.ResourceMemory: resource.MustParse("32Mi")},
		Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("200m"), corev1.ResourceMemory: resource.MustParse("64Mi")},
	}
	pod.Spec.Containers = append(pod.Spec.Containers, corev1.Container{
		Name: relayContainer, Image: image, Args: []string{"telemetry-relay"}, SecurityContext: security.DeepCopy(), Resources: resources,
		Ports:          []corev1.ContainerPort{{Name: "otlp", ContainerPort: network.TelemetryIntakePort}, {Name: "metrics", ContainerPort: network.TelemetryMetricsPort}},
		ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{Port: fromInt(network.TelemetryIntakePort)}}},
	})
	if pod.Annotations == nil {
		pod.Annotations = map[string]string{}
	}
	for k, v := range scrapeAnnotations {
		pod.Annotations[k] = v
	}
}

// stripCellTelemetry removes every telemetry part from a proxy Deployment spec,
// so drift checks compare only the rest.
func stripCellTelemetry(spec *appsv1.DeploymentSpec) {
	pod := &spec.Template
	kept := []corev1.Container{}
	for _, c := range pod.Spec.Containers {
		if c.Name == relayContainer {
			continue
		}
		env := []corev1.EnvVar{}
		for _, e := range c.Env {
			if !(c.Name == "proxy" && e.Name == proxy.MetricsAddressEnv) {
				env = append(env, e)
			}
		}
		if len(env) == 0 {
			env = nil
		}
		c.Env = env
		kept = append(kept, c)
	}
	pod.Spec.Containers = kept
	for k := range scrapeAnnotations {
		delete(pod.Annotations, k)
	}
	if len(pod.Annotations) == 0 {
		pod.Annotations = nil
	}
}

// stripTelemetryEnv removes the worker telemetry settings from an unstructured
// Sandbox spec's workspace container.
func stripTelemetryEnv(spec map[string]any) {
	pod, _ := spec["podTemplate"].(map[string]any)
	body, _ := pod["spec"].(map[string]any)
	items, _ := body["containers"].([]any)
	for i, raw := range items {
		c, ok := raw.(map[string]any)
		if !ok || c["name"] != "workspace" {
			continue
		}
		env, _ := c["env"].([]any)
		kept := []any{}
		for _, e := range env {
			m, _ := e.(map[string]any)
			if name, _ := m["name"].(string); isTelemetryEnv(name) {
				continue
			}
			kept = append(kept, e)
		}
		c["env"] = kept
		items[i] = c
	}
}

// telemetryPartsEqual compares only the telemetry parts of two proxy pod
// templates: the relay container, the proxy's metrics env and the annotations.
func telemetryPartsEqual(got, want corev1.PodTemplateSpec) bool {
	part := func(t corev1.PodTemplateSpec) (bool, bool, map[string]string) {
		relay, metrics := false, false
		for _, c := range t.Spec.Containers {
			if c.Name == relayContainer {
				relay = true
			}
			for _, e := range c.Env {
				if c.Name == "proxy" && e.Name == proxy.MetricsAddressEnv {
					metrics = true
				}
			}
		}
		ann := map[string]string{}
		for k := range scrapeAnnotations {
			if v, ok := t.Annotations[k]; ok {
				ann[k] = v
			}
		}
		return relay, metrics, ann
	}
	gr, gm, ga := part(got)
	wr, wm, wa := part(want)
	if gr != wr || gm != wm || len(ga) != len(wa) {
		return false
	}
	for k, v := range wa {
		if ga[k] != v {
			return false
		}
	}
	return true
}

// relayMatches reports whether the relay container, when both templates have
// one, differs at most in its image (which follows the proxy image). Any
// other change to it is drift, as for the proxy container.
func relayMatches(got, want corev1.PodTemplateSpec) bool {
	find := func(t corev1.PodTemplateSpec) *corev1.Container {
		for i := range t.Spec.Containers {
			if t.Spec.Containers[i].Name == relayContainer {
				c := t.Spec.Containers[i].DeepCopy()
				c.Image = "normalized"
				return c
			}
		}
		return nil
	}
	g, w := find(got), find(want)
	if g == nil || w == nil {
		return true
	}
	return contains(*g, *w)
}

// withScrapeAnnotations returns current's annotations with only the scrape
// keys set as in want; other annotations (such as a restart stamp) stay.
func withScrapeAnnotations(current, want map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range current {
		out[k] = v
	}
	for k := range scrapeAnnotations {
		if v, ok := want[k]; ok {
			out[k] = v
		} else {
			delete(out, k)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
