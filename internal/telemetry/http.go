package telemetry

import (
	"bufio"
	"net"
	"net/http"
	"strconv"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

var (
	httpRequests, _ = Meter().Int64Counter("apc.http.requests", metric.WithDescription("HTTP requests served by component, route pattern (type) and status code"))
	httpDuration, _ = Meter().Float64Histogram("apc.http.request.duration", metric.WithUnit("s"), metric.WithDescription("HTTP request duration by component and route pattern (type)"))
)

// HTTP records a server span, a request count and a duration for every
// request next serves. route maps a request to a fixed route pattern (for
// example a ServeMux pattern); it must never return raw paths, query strings
// or identifiers, which would leak data and explode series. An empty route
// is recorded as "unmatched". Headers and bodies are never recorded.
func HTTP(component string, route func(*http.Request) string, next http.Handler) http.Handler {
	tracer := otel.Tracer("glovecell")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		name := route(r)
		if name == "" {
			name = "unmatched"
		}
		// A caller's trace context is honored only inside the cluster; it
		// carries no credentials.
		ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))
		ctx, span := tracer.Start(ctx, component+" "+name, trace.WithSpanKind(trace.SpanKindServer))
		defer span.End()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r.WithContext(ctx))
		code := strconv.Itoa(sw.status)
		span.SetAttributes(attribute.Int("http.response.status_code", sw.status), attribute.String("http.route", name))
		if sw.status >= 500 {
			span.SetStatus(codes.Error, http.StatusText(sw.status))
		}
		attrs := metric.WithAttributes(attribute.String(KeyComponent, component), attribute.String(KeyType, name), attribute.String(KeyCode, code))
		httpRequests.Add(ctx, 1, attrs)
		httpDuration.Record(ctx, time.Since(started).Seconds(), attrs)
	})
}

// statusWriter captures the status code. Unwrap, Flush and Hijack keep
// streaming, uploads and protocol upgrades working through it.
type statusWriter struct {
	http.ResponseWriter
	status  int
	written bool
}

func (w *statusWriter) WriteHeader(code int) {
	if !w.written {
		w.status, w.written = code, true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(p []byte) (int, error) {
	w.written = true
	return w.ResponseWriter.Write(p)
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *statusWriter) Flush() { _ = http.NewResponseController(w.ResponseWriter).Flush() }

func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	w.written = true
	if w.status == http.StatusOK {
		w.status = http.StatusSwitchingProtocols
	}
	return http.NewResponseController(w.ResponseWriter).Hijack()
}
