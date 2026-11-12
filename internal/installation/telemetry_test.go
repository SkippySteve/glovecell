package installation

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func telemetryValues(telemetry map[string]any) map[string]any {
	values := databaseValues()
	values["forgeAgent"] = map[string]any{"enabled": true}
	values["voice"] = map[string]any{"enabled": true, "image": "registry.example.com/voice@sha256:" + strings.Repeat("c", 64), "homeserver": "https://matrix.example.com", "speech": map[string]any{"baseURL": "http://speech.example.com"}}
	values["telemetry"] = telemetry
	return values
}

func containerEnv(t *testing.T, o map[string]any) map[string]any {
	t.Helper()
	env := map[string]any{}
	for _, e := range dig(o, "spec", "template", "spec", "containers").([]any)[0].(map[string]any)["env"].([]any) {
		m := e.(map[string]any)
		if v, ok := m["value"]; ok {
			env[m["name"].(string)] = v
		} else {
			env[m["name"].(string)] = m["valueFrom"]
		}
	}
	return env
}

func fullTelemetry() map[string]any {
	return map[string]any{
		"enabled":    true,
		"otlp":       map[string]any{"endpoint": "http://otel-collector.monitoring.svc:4318", "headersSecret": "otlp-auth"},
		"collector":  map[string]any{"namespace": "monitoring", "podLabels": map[string]any{"app": "otel-collector"}},
		"cells":      map[string]any{"enabled": true},
		"prometheus": map[string]any{"podMonitors": true, "labels": map[string]any{"release": "kps"}},
		"dashboards": map[string]any{"enabled": true},
	}
}

// Off: nothing is sent or scraped and voice gets no OTLP settings. On: one
// section wires every process, the cell scraper, the network paths, the
// PodMonitors and the dashboard.
func TestChartTelemetry(t *testing.T) {
	requireHelm(t)
	_, off, err := renderChart(t, telemetryValues(map[string]any{"enabled": false}))
	if err != nil {
		t.Fatal(err)
	}
	data := dig(controllerConfig(t, off), "data").(map[string]any)
	if data["OTEL_ENABLED"] != "false" || data["CELL_TELEMETRY_SCRAPER_JSON"] != "" {
		t.Fatalf("telemetry on by default: %v %v", data["OTEL_ENABLED"], data["CELL_TELEMETRY_SCRAPER_JSON"])
	}
	for _, kind := range []string{"PodMonitor"} {
		if find(off, kind, "") != nil {
			t.Fatalf("%s rendered while off", kind)
		}
	}
	if find(off, "ConfigMap", "glovecell-dashboard") != nil {
		t.Fatal("dashboard rendered while off")
	}
	if env := containerEnv(t, find(off, "Deployment", "glovecell-voice")); env["OTEL_ENABLED"] != nil {
		t.Fatalf("voice telemetry while off: %v", env)
	}

	_, on, err := renderChart(t, telemetryValues(fullTelemetry()))
	if err != nil {
		t.Fatal(err)
	}
	data = dig(controllerConfig(t, on), "data").(map[string]any)
	if data["OTEL_ENABLED"] != "true" || data["OTEL_EXPORTER_OTLP_ENDPOINT"] != "http://otel-collector.monitoring.svc:4318" {
		t.Fatalf("controller OTLP %v", data)
	}
	var scraper map[string]any
	if err := json.Unmarshal([]byte(data["CELL_TELEMETRY_SCRAPER_JSON"].(string)), &scraper); err != nil || scraper["namespace"] != "monitoring" || dig(scraper, "selector", "app") != "otel-collector" {
		t.Fatalf("cell scraper %v %v", data["CELL_TELEMETRY_SCRAPER_JSON"], err)
	}
	headers := func(name string, optional bool) {
		t.Helper()
		env := containerEnv(t, find(on, "Deployment", name))
		ref := dig(env["OTEL_EXPORTER_OTLP_HEADERS"], "secretKeyRef")
		if dig(ref, "name") != "otlp-auth" || dig(ref, "key") != "headers" || (dig(ref, "optional") == true) != optional {
			t.Fatalf("%s collector auth %v", name, env["OTEL_EXPORTER_OTLP_HEADERS"])
		}
		if name != "controller" && (env["OTEL_ENABLED"] != "true" || env["OTEL_EXPORTER_OTLP_ENDPOINT"] != "http://otel-collector.monitoring.svc:4318") {
			t.Fatalf("%s OTLP env %v", name, env)
		}
	}
	headers("controller", false)
	headers("forge-agent", true) // its own namespace: a copy of the Secret there is optional
	headers("glovecell-voice", false)

	np := find(on, "NetworkPolicy", "glovecell-voice")
	peer := func(rules any, port float64) bool {
		for _, r := range rules.([]any) {
			for _, p := range dig(r, "ports").([]any) {
				if dig(p, "port") != port {
					continue
				}
				for _, side := range []string{"from", "to"} {
					for _, x := range func() []any { v, _ := dig(r, side).([]any); return v }() {
						if dig(x, "namespaceSelector", "matchLabels", "kubernetes.io/metadata.name") == "monitoring" && dig(x, "podSelector", "matchLabels", "app") == "otel-collector" {
							return true
						}
					}
				}
			}
		}
		return false
	}
	if !peer(dig(np, "spec", "ingress"), 8081) || !peer(dig(np, "spec", "egress"), 4318) {
		t.Fatalf("voice network paths to the collector missing: %v", dig(np, "spec"))
	}
	cells := find(on, "PodMonitor", "glovecell-cells")
	if cells == nil || dig(cells, "spec", "selector", "matchLabels", "projects.agentwork.dev/role") != "proxy" || dig(cells, "metadata", "labels", "release") != "kps" {
		t.Fatalf("cell PodMonitor %v", cells)
	}
	if find(on, "PodMonitor", "glovecell-voice") == nil {
		t.Fatal("voice PodMonitor missing")
	}
	if dig(find(on, "Cluster", ""), "spec", "monitoring", "enablePodMonitor") != true {
		t.Fatal("database PodMonitor not switched on")
	}
	dash := find(on, "ConfigMap", "glovecell-dashboard")
	if dash == nil || dig(dash, "metadata", "labels", "grafana_dashboard") != "1" {
		t.Fatalf("dashboard ConfigMap %v", dash)
	}
	var board map[string]any
	if err := json.Unmarshal([]byte(dig(dash, "data", "glovecell.json").(string)), &board); err != nil || board["uid"] != "glovecell" {
		t.Fatalf("dashboard JSON: %v", err)
	}

	// https endpoints default to port 443 for the voice egress rule.
	tv := fullTelemetry()
	tv["otlp"] = map[string]any{"endpoint": "https://collector.monitoring.svc/otlp"}
	_, https, err := renderChart(t, telemetryValues(tv))
	if err != nil {
		t.Fatal(err)
	}
	if !peer(dig(find(https, "NetworkPolicy", "glovecell-voice"), "spec", "egress"), 443) {
		t.Fatal("https endpoint did not open 443")
	}

	for name, change := range map[string]func(tv map[string]any, cfg map[string]any){
		"non-http endpoint":       func(tv, _ map[string]any) { tv["otlp"] = map[string]any{"endpoint": "grpc://collector:4317"} },
		"headers without push":    func(tv, _ map[string]any) { tv["otlp"] = map[string]any{"headersSecret": "otlp-auth"} },
		"namespace without pods":  func(tv, _ map[string]any) { tv["collector"] = map[string]any{"namespace": "monitoring"} },
		"cells without collector": func(tv, _ map[string]any) { tv["collector"] = map[string]any{} },
		"nothing selected": func(tv, _ map[string]any) {
			tv["otlp"], tv["cells"], tv["prometheus"], tv["dashboards"], tv["collector"] = map[string]any{}, map[string]any{"enabled": false}, map[string]any{"podMonitors": false}, map[string]any{"enabled": false}, map[string]any{}
		},
		"legacy switch too":   func(_, cfg map[string]any) { cfg["OTEL_ENABLED"] = "true" },
		"legacy endpoint too": func(_, cfg map[string]any) { cfg["OTEL_EXPORTER_OTLP_ENDPOINT"] = "http://elsewhere:4318" },
		"legacy scraper too": func(_, cfg map[string]any) {
			cfg["CELL_TELEMETRY_SCRAPER_JSON"] = `{"namespace":"x","selector":{"a":"b"}}`
		},
	} {
		tv := fullTelemetry()
		values := telemetryValues(tv)
		change(tv, values["config"].(map[string]any))
		if _, _, err := renderChart(t, values); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// The legacy config.OTEL_* keys keep working for installs that use them, and
// voice still gets no OTLP settings from them (as before this section).
func TestChartTelemetryLegacyConfig(t *testing.T) {
	requireHelm(t)
	values := telemetryValues(map[string]any{"enabled": false})
	values["config"].(map[string]any)["OTEL_ENABLED"] = "true"
	values["config"].(map[string]any)["OTEL_EXPORTER_OTLP_ENDPOINT"] = "http://collector:4318"
	_, objs, err := renderChart(t, values)
	if err != nil {
		t.Fatal(err)
	}
	if env := containerEnv(t, find(objs, "Deployment", "forge-agent")); env["OTEL_ENABLED"] != "true" || env["OTEL_EXPORTER_OTLP_ENDPOINT"] != "http://collector:4318" {
		t.Fatalf("forge agent legacy env %v", env)
	}
	if env := containerEnv(t, find(objs, "Deployment", "glovecell-voice")); env["OTEL_ENABLED"] != nil {
		t.Fatalf("voice gained legacy telemetry %v", env)
	}
}

// The shipped dashboard carries nothing from one particular installation.
func TestChartDashboardIsGeneric(t *testing.T) {
	raw, err := os.ReadFile("../../charts/agent-project-controller/files/dashboards/glovecell.json")
	if err != nil {
		t.Fatal(err)
	}
	var board map[string]any
	if err := json.Unmarshal(raw, &board); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{`"uid": "metrics"`, `"uid": "logs"`, `"uid": "traces"`, "service_namespace", "agent-provider-broker", "agent-project-controller\"", "o2c", "OpenObserve"} {
		if strings.Contains(string(raw), s) {
			t.Errorf("dashboard contains installation-specific %q", s)
		}
	}
	if !strings.Contains(string(raw), `"uid": "${datasource}"`) {
		t.Error("dashboard panels do not use the datasource variable")
	}
}
