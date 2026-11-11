package githubbroker

import (
	"context"

	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/telemetry"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// leaseStatuses are every status publish writes; the gauge reports 1 for the
// current one and 0 for the rest, so a dashboard shows one live series.
var leaseStatuses = []string{"live", "unavailable", "authorization-required", "revoked"}

var leaseStatus, _ = telemetry.Meter().Int64Gauge("apc.github.lease.status", metric.WithDescription("1 for the GitHub broker's currently published lease status"))

func recordLeaseStatus(ctx context.Context, current string) {
	for _, s := range leaseStatuses {
		v := int64(0)
		if s == current {
			v = 1
		}
		leaseStatus.Record(ctx, v, metric.WithAttributes(attribute.String(telemetry.KeyStatus, s)))
	}
}
