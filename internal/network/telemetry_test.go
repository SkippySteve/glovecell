package network

import (
	"encoding/json"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

var collector = &Scraper{Namespace: "openobserve-collector", Selector: map[string]string{"app.kubernetes.io/instance": "openobserve-collector.o2c-openobserve-collector-gateway"}}

func specs(objects []*unstructured.Unstructured) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, o := range objects {
		spec, _, _ := unstructured.NestedMap(o.Object, "spec")
		out[o.GetName()] = spec
	}
	return out
}

func encoded(v any) string { b, _ := json.Marshal(v); return string(b) }

func TestCellTelemetryRulesOnlyWhenSwitchedOn(t *testing.T) {
	off := specs(Cilium{}.Objects("apc-x", "uid", []string{"git.example.com"}))
	on := specs(Cilium{Telemetry: collector}.Objects("apc-x", "uid", []string{"git.example.com"}))
	for _, name := range []string{"worker", "proxy"} {
		if strings.Contains(encoded(off[name]), "4318") || strings.Contains(encoded(off[name]), "9464") {
			t.Fatalf("%s policy opens telemetry ports while switched off", name)
		}
		if encoded(StripTelemetry(on[name])) != encoded(StripTelemetry(off[name])) {
			t.Fatalf("%s: switching telemetry on changes more than telemetry rules", name)
		}
	}
	// Worker: one new egress, to its own proxy pod on 4318 only. Never 9464.
	wEgress := on["worker"]["egress"].([]any)
	last := encoded(wEgress[len(wEgress)-1])
	if !strings.Contains(last, `"port":"4318"`) || !strings.Contains(last, `"projects.agentwork.dev/role":"proxy"`) || strings.Contains(encoded(on["worker"]), "9464") {
		t.Fatalf("worker telemetry egress wrong: %s", last)
	}
	// Proxy: no new egress at all.
	if encoded(on["proxy"]["egress"]) != encoded(off["proxy"]["egress"]) {
		t.Fatal("cell telemetry gave the proxy pod new egress")
	}
	// Proxy ingress: worker on 4318; only the collector on 9464, GET /metrics only.
	var intake, scrape string
	for _, rule := range on["proxy"]["ingress"].([]any) {
		e := encoded(rule)
		switch {
		case strings.Contains(e, `"port":"4318"`):
			intake = e
		case strings.Contains(e, `"port":"9464"`):
			scrape = e
		}
	}
	if !strings.Contains(intake, `"projects.agentwork.dev/role":"worker"`) || strings.Contains(intake, "openobserve") {
		t.Fatalf("intake rule wrong: %s", intake)
	}
	if !strings.Contains(scrape, `"k8s:io.kubernetes.pod.namespace":"openobserve-collector"`) || strings.Contains(scrape, `"role":"worker"`) ||
		!strings.Contains(scrape, `"method":"GET"`) || !strings.Contains(scrape, `"path":"^/metrics$"`) {
		t.Fatalf("scrape rule wrong: %s", scrape)
	}
}

func TestStripTelemetryKeepsMixedRules(t *testing.T) {
	spec := map[string]any{"ingress": []any{
		map[string]any{"toPorts": []any{map[string]any{"ports": []any{map[string]any{"port": "3128"}, map[string]any{"port": "9464"}}}}},
		map[string]any{"ports": []any{map[string]any{"port": float64(4318)}}},
		map[string]any{"fromEntities": []any{"all"}},
	}}
	got := StripTelemetry(spec)["ingress"].([]any)
	if len(got) != 2 {
		t.Fatalf("a rule that also opens a non-telemetry port, or has no ports, must stay: %v", got)
	}
}

func TestCellTelemetryConfiguration(t *testing.T) {
	t.Setenv("NETWORK_PROVIDER", "cilium")
	t.Setenv("CELL_TELEMETRY_SCRAPER_JSON", `{"namespace":"openobserve-collector","selector":{"app.kubernetes.io/instance":"gw"}}`)
	p, err := FromEnvironment()
	if err != nil || CellTelemetry(p) == nil || CellTelemetry(p).Namespace != "openobserve-collector" {
		t.Fatalf("scraper not configured: %v %v", p, err)
	}
	for _, bad := range []string{`{"namespace":"","selector":{"a":"b"}}`, `{"namespace":"x","selector":{}}`, `{"namespace":"x","selector":{"io.kubernetes.pod.namespace":"y"}}`, `{"namespace":"x","selector":{"a":"b"},"port":1}`, `not json`} {
		t.Setenv("CELL_TELEMETRY_SCRAPER_JSON", bad)
		if _, err := FromEnvironment(); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
	t.Setenv("CELL_TELEMETRY_SCRAPER_JSON", "")
	p, err = FromEnvironment()
	if err != nil || CellTelemetry(p) != nil {
		t.Fatal("cell telemetry must be off by default")
	}
	if err := (NetworkPolicy{Telemetry: collector, EnforcementAcknowledged: true, ClusterCIDRs: []string{"10.0.0.0/8"}}).Validate(); err == nil {
		t.Fatal("NetworkPolicy provider accepted cell telemetry")
	}
}
