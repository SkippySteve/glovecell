package proxy

import (
	"context"
	"strconv"

	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/telemetry"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

var requests, _ = telemetry.Meter().Int64Counter("apc.proxy.requests", metric.WithDescription("Inspecting proxy decisions by reason (a fixed set) and HTTP status code"))

// countRequest records one proxy decision. It carries only a fixed reason
// and the status code: never the destination host, path, headers or body,
// which are the cell's private work. The proxy never pushes telemetry out of
// the cell (decision GT A): until the scrape endpoint lands, the global no-op
// meter drops these.
func countRequest(ctx context.Context, reason string, code int) {
	requests.Add(ctx, 1, metric.WithAttributes(attribute.String(telemetry.KeyReason, reason), attribute.String(telemetry.KeyCode, strconv.Itoa(code))))
}
