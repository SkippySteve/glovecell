package proxy

import (
	"testing"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
)

func TestVendorTelemetryRoutesAndOffSwitch(t *testing.T) {
	for _, tc := range []struct{ host, path string }{
		{"ab.chatgpt.com", "/otlp/v1/metrics"},
		{"ab.chatgpt.com", "/v1/rgstr"},
		{"api.anthropic.com", "/api/event_logging/v2/batch"},
		{"http-intake.logs.us5.datadoghq.com", "/api/v2/logs"},
		{"browser-intake-us5-datadoghq.com", "/api/v2/logs"},
	} {
		t.Run(tc.host+tc.path, func(t *testing.T) {
			// A broad operator rule cannot bypass the telemetry off switch.
			cfg := Config{Rules: []api.HTTPSRule{{Host: tc.host, Methods: []string{"POST"}, Paths: []string{"/"}}}}
			if Allowed(cfg, tc.host, "POST", tc.path) {
				t.Fatal("telemetry allowed while disabled")
			}
			cfg.Rules = nil
			cfg.VendorTelemetry = true
			if !Allowed(cfg, tc.host, "POST", tc.path) {
				t.Fatal("reviewed telemetry refused")
			}
			for _, method := range []string{"GET", "PUT", "DELETE"} {
				if Allowed(cfg, tc.host, method, tc.path) {
					t.Fatal("unexpected method allowed", method)
				}
			}
		})
	}
	allowed := Config{VendorTelemetry: true}
	for _, tc := range []struct{ host, path string }{
		{"api.anthropic.com", "/v1/messages"},
		{"api.anthropic.com", "/api/event_logging-elsewhere"},
		{"api.anthropic.com", "/api/event_logging/../oauth"},
		{"chatgpt.com", "/backend-api/codex/responses"},
		{"attacker.chatgpt.com", "/v1/rgstr"},
		{"example.com", "/api/v2/logs"},
		{"other.datadoghq.com", "/api/v2/logs"},
		{"http-intake.logs.us5.datadoghq.com", "/api/v2/other"},
	} {
		if Allowed(allowed, tc.host, "POST", tc.path) {
			t.Fatal("telemetry widened unrelated access", tc)
		}
	}
}

func TestVendorTelemetryConnectAdmission(t *testing.T) {
	s := &Server{Config: Config{VendorTelemetry: true}}
	for _, host := range VendorTelemetryHosts() {
		if !s.inRules(host) {
			t.Fatal("collection host cannot CONNECT", host)
		}
	}
	if s.inRules("other.datadoghq.com") {
		t.Fatal("unlisted host can CONNECT")
	}
	s.Config.VendorTelemetry = false
	for _, host := range VendorTelemetryHosts() {
		if s.inRules(host) {
			t.Fatal("telemetry-only CONNECT survives disable", host)
		}
	}
}
