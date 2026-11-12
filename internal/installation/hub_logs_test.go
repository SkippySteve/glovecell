package installation

import (
	"strings"
	"testing"
)

// The hub's log read is off by default; with hubspoke.hub.logsPerProject the
// chart adds a ClusterRole that only the controller may bind (per project
// namespace) and never a ClusterRoleBinding for it.
func TestChartHubLogsPerProject(t *testing.T) {
	requireHelm(t)
	const role = "test-control-qualification-hub-logs"
	_, off, err := renderChart(t, databaseValues())
	if err != nil {
		t.Fatal(err)
	}
	if find(off, "ClusterRole", role) != nil {
		t.Fatal("hub logs ClusterRole rendered while off")
	}
	if dig(controllerConfig(t, off), "data", "HUBSPOKE_HUB_LOGS_CLUSTER_ROLE") != nil {
		t.Fatal("hub logs role configured while off")
	}

	values := databaseValues()
	values["hubspoke"] = map[string]any{
		"orca": map[string]any{"enabled": true, "runtimeImage": "registry.example.com/orca@sha256:" + strings.Repeat("c", 64)},
		// The chart requires a wss:// hub address whenever hubspoke is on (APC #131).
		"hub": map[string]any{"logsPerProject": true, "publicAddress": "wss://orca-hub.example"},
	}
	_, on, err := renderChart(t, values)
	if err != nil {
		t.Fatal(err)
	}
	cr := find(on, "ClusterRole", role)
	if cr == nil {
		t.Fatal("hub logs ClusterRole missing")
	}
	rules := dig(cr, "rules").([]any)
	if len(rules) != 2 {
		t.Fatalf("hub logs rules %v", rules)
	}
	for _, r := range rules {
		res := dig(r, "resources").([]any)
		verbs := dig(r, "verbs").([]any)
		switch {
		case len(res) == 1 && res[0] == "pods" && len(verbs) == 3:
		case len(res) == 1 && res[0] == "pods/log" && len(verbs) == 1 && verbs[0] == "get":
		default:
			t.Fatalf("unexpected hub logs rule %v", r)
		}
	}
	for _, o := range on {
		if o["kind"] == "ClusterRoleBinding" && dig(o, "roleRef", "name") == role {
			t.Fatal("hub logs ClusterRole bound cluster-wide")
		}
	}
	bind := false
	for _, o := range on {
		if o["kind"] != "ClusterRole" {
			continue
		}
		for _, r := range dig(o, "rules").([]any) {
			names, _ := dig(r, "resourceNames").([]any)
			if len(names) == 1 && names[0] == role {
				bind = true
			}
		}
	}
	if !bind {
		t.Fatal("controller cannot bind the hub logs ClusterRole")
	}
	cfg := controllerConfig(t, on)
	if dig(cfg, "data", "HUBSPOKE_HUB_LOGS_CLUSTER_ROLE") != role || dig(cfg, "data", "HUBSPOKE_HUB_SERVICE_ACCOUNT") != "orca-hub" {
		t.Fatalf("controller config %v", dig(cfg, "data"))
	}
}

func controllerConfig(t *testing.T, objs []map[string]any) map[string]any {
	t.Helper()
	for _, o := range objs {
		if o["kind"] == "ConfigMap" && dig(o, "data", "CONTROL_NAMESPACE") != nil {
			return o
		}
	}
	t.Fatal("controller ConfigMap missing")
	return nil
}
