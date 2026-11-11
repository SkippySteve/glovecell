package network

import (
	"errors"
	"fmt"

	"k8s.io/apimachinery/pkg/util/validation"
)

// Cell telemetry ports (decision GT A). The worker reaches the relay in its
// proxy pod on TelemetryIntakePort; only the Scraper reaches TelemetryMetricsPort.
const (
	TelemetryIntakePort  = 4318
	TelemetryMetricsPort = 9464
)

// Scraper is the cluster collector allowed to read each cell's GET /metrics.
// Nil means cell telemetry is off: no relay, no rules, no worker settings.
type Scraper struct {
	Namespace string            `json:"namespace"`
	Selector  map[string]string `json:"selector"`
}

func (s *Scraper) Validate() error {
	if s == nil {
		return nil
	}
	if len(validation.IsDNS1123Label(s.Namespace)) != 0 || len(s.Selector) == 0 {
		return errors.New("cell telemetry scraper requires an explicit namespace and pod selector")
	}
	for key, value := range s.Selector {
		if len(validation.IsQualifiedName(key)) != 0 || len(validation.IsValidLabelValue(value)) != 0 || value == "" || key == "io.kubernetes.pod.namespace" {
			return errors.New("invalid cell telemetry scraper selector")
		}
	}
	return nil
}

// CellTelemetry returns the provider's scraper, or nil when cell telemetry is off.
func CellTelemetry(p Provider) *Scraper {
	if t, ok := p.(interface{ Scraper() *Scraper }); ok {
		return t.Scraper()
	}
	return nil
}

func (c Cilium) Scraper() *Scraper { return c.Telemetry }

// Scraper is always nil: Validate refuses cell telemetry with this provider.
func (p NetworkPolicy) Scraper() *Scraper { return nil }

// StripTelemetry returns a copy of a worker or proxy policy spec without the
// rules that only open telemetry ports. The controller uses it to tell a
// telemetry-only difference (switch turned on or off) from real drift.
func StripTelemetry(spec map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range spec {
		out[k] = v
	}
	for _, key := range []string{"ingress", "egress"} {
		rules, ok := out[key].([]any)
		if !ok {
			continue
		}
		kept := []any{}
		for _, raw := range rules {
			if !telemetryOnly(raw) {
				kept = append(kept, raw)
			}
		}
		out[key] = kept
	}
	return out
}

func telemetryOnly(raw any) bool {
	rule, ok := raw.(map[string]any)
	if !ok {
		return false
	}
	ports := []any{}
	if list, ok := rule["ports"].([]any); ok { // NetworkPolicy
		ports = append(ports, list...)
	}
	if list, ok := rule["toPorts"].([]any); ok { // CiliumNetworkPolicy
		for _, tp := range list {
			if m, ok := tp.(map[string]any); ok {
				if inner, ok := m["ports"].([]any); ok {
					ports = append(ports, inner...)
				}
			}
		}
	}
	if len(ports) == 0 {
		return false
	}
	for _, p := range ports {
		m, ok := p.(map[string]any)
		if !ok {
			return false
		}
		n := fmt.Sprint(m["port"])
		if n != fmt.Sprint(TelemetryIntakePort) && n != fmt.Sprint(TelemetryMetricsPort) {
			return false
		}
	}
	return true
}
