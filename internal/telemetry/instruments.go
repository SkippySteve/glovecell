package telemetry

import (
	"context"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
)

// Attribute keys on Glovecell metrics are limited to names the cluster's
// Grafana metrics branch keeps (transform/workplace in the o2c gateway):
// component, name, status, reason, code, type and phase. Any other datapoint
// attribute is dropped there, and its series would collapse.
const (
	KeyComponent = "component"
	KeyName      = "name"
	KeyStatus    = "status"
	KeyReason    = "reason"
	KeyCode      = "code"
	KeyType      = "type"
)

// Meter and Tracer are the instrumentation scope of every Glovecell process.
// They bind to the global providers, so instruments created at package init
// start exporting once Setup runs.
func Meter() metric.Meter { return otel.Meter("glovecell") }

var (
	ticks, _        = Meter().Int64Counter("apc.loop.ticks", metric.WithDescription("Background loop iterations by component and status (ok or error)"))
	tickDuration, _ = Meter().Float64Histogram("apc.loop.tick.duration", metric.WithUnit("s"), metric.WithDescription("Background loop iteration duration"))
	lastSuccess, _  = Meter().Int64Gauge("apc.loop.last_success", metric.WithUnit("s"), metric.WithDescription("Unix time of the last successful loop iteration"))
)

// Tick runs one iteration of a background loop as a span and records its
// count, duration and last success under component. It returns fn's error.
func Tick(ctx context.Context, component string, fn func(context.Context) error) error {
	started := time.Now()
	ctx, span := otel.Tracer("glovecell").Start(ctx, component+".tick")
	defer span.End()
	err := fn(ctx)
	status := "ok"
	if err != nil {
		status = "error"
		// The error text stays out of the span: it can name owners and hosts.
		span.SetStatus(codes.Error, component+" iteration failed")
	} else {
		lastSuccess.Record(ctx, time.Now().Unix(), metric.WithAttributes(attribute.String(KeyComponent, component)))
	}
	attrs := metric.WithAttributes(attribute.String(KeyComponent, component), attribute.String(KeyStatus, status))
	ticks.Add(ctx, 1, attrs)
	tickDuration.Record(ctx, time.Since(started).Seconds(), attrs)
	return err
}
