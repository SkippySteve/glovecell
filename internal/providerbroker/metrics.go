package providerbroker

import (
	"context"
	"time"

	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/telemetry"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

var (
	renewals, _    = telemetry.Meter().Int64Counter("apc.provider.renewals", metric.WithDescription("Credential owner renewals by provider (name) and status (ok or unavailable)"))
	leaseActive, _ = telemetry.Meter().Int64Gauge("apc.provider.lease.active", metric.WithDescription("1 when the provider's published lease is enabled and unexpired"))
)

// recordRenewal counts one owner renewal. Only the provider name and the
// outcome are recorded: never the owner, account, subject or token.
func recordRenewal(ctx context.Context, provider string, ok bool) {
	status := "ok"
	if !ok {
		status = "unavailable"
	}
	renewals.Add(ctx, 1, metric.WithAttributes(attribute.String(telemetry.KeyName, provider), attribute.String(telemetry.KeyStatus, status)))
}

// recordLeases publishes, per provider, whether the lease about to be
// published is usable now.
func recordLeases(ctx context.Context, next map[string]Grant, now time.Time) {
	for name, grant := range next {
		v := int64(0)
		if grant.Enabled && grant.Access != nil && grant.Access.Expires > float64(now.Unix()) {
			v = 1
		}
		leaseActive.Record(ctx, v, metric.WithAttributes(attribute.String(telemetry.KeyName, name)))
	}
}
