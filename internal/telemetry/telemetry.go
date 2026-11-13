// Package telemetry sets up optional OpenTelemetry export and the redacting
// process logger shared by every Glovecell process.
//
// Traces and metrics go to the OTLP/HTTP endpoint in the standard
// OTEL_EXPORTER_OTLP_* variables when OTEL_ENABLED is "true". Logs stay JSON
// on stderr, where the cluster's log collector picks them up; Logger redacts
// credentials before a line is written, so no sink receives them.
package telemetry

import (
	"context"
	"errors"
	"os"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// Namespace is the service.namespace of every Glovecell process, so one
// dashboard selector covers them all.
const Namespace = "glovecell"

// Enabled reports the OTEL_ENABLED switch. Telemetry is off unless it is "true".
func Enabled() bool { return os.Getenv("OTEL_ENABLED") == "true" }

// Version is stamped into service.version; main packages set it from their
// build version before calling Setup.
var Version = ""

// Setup installs the global tracer and meter providers for service. It never
// blocks the caller on an unavailable collector: exports are bounded and
// dropped on failure. OTEL_SERVICE_NAME and OTEL_RESOURCE_ATTRIBUTES override
// the defaults. Without OTEL_TRACES_SAMPLER, 10% of root traces are kept.
func Setup(ctx context.Context, service string, enabled bool) (func(context.Context) error, error) {
	if !enabled {
		return func(context.Context) error { return nil }, nil
	}
	attrs := []attribute.KeyValue{attribute.String("service.name", service), attribute.String("service.namespace", Namespace)}
	if Version != "" {
		attrs = append(attrs, attribute.String("service.version", Version))
	}
	// Later options win, so the environment can rename the service.
	res, e := resource.New(ctx, resource.WithAttributes(attrs...), resource.WithTelemetrySDK(), resource.WithFromEnv())
	if e != nil && res == nil {
		return nil, e
	}
	traces, e := otlptracehttp.New(ctx, otlptracehttp.WithTimeout(2*time.Second))
	if e != nil {
		return nil, e
	}
	options := []sdktrace.TracerProviderOption{sdktrace.WithResource(res), sdktrace.WithBatcher(traces, sdktrace.WithMaxQueueSize(256), sdktrace.WithExportTimeout(2*time.Second), sdktrace.WithBatchTimeout(2*time.Second))}
	if os.Getenv("OTEL_TRACES_SAMPLER") == "" {
		options = append(options, sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(.1))))
	}
	tp := sdktrace.NewTracerProvider(options...)
	metrics, e := otlpmetrichttp.New(ctx, otlpmetrichttp.WithTimeout(2*time.Second))
	if e != nil {
		_ = tp.Shutdown(ctx)
		return nil, e
	}
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithResource(res), sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metrics, sdkmetric.WithInterval(30*time.Second), sdkmetric.WithTimeout(2*time.Second))))
	otel.SetTracerProvider(tp)
	otel.SetMeterProvider(mp)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	return func(ctx context.Context) error { return errors.Join(tp.Shutdown(ctx), mp.Shutdown(ctx)) }, nil
}

// Start runs Setup for a long-running process and returns its shutdown, which
// is always safe to call. A failed setup is logged once and the process
// continues without telemetry: export is never a reason to stop enforcement.
func Start(ctx context.Context, service string) func() {
	shutdown, e := Setup(ctx, service, Enabled())
	if e != nil {
		Logger().Warn("optional telemetry unavailable; continuing without it", "service", service, "error", e.Error())
		return func() {}
	}
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = shutdown(ctx)
	}
}
