package telemetry

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

var reader = sdkmetric.NewManualReader()

func TestMain(m *testing.M) {
	// The package's instruments bind to the global provider on first use.
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	os.Exit(m.Run())
}

// points returns every data point attribute set recorded for metric name.
func points(t *testing.T, name string) []attribute.Set {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	var sets []attribute.Set
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			switch d := m.Data.(type) {
			case metricdata.Sum[int64]:
				for _, p := range d.DataPoints {
					sets = append(sets, p.Attributes)
				}
			case metricdata.Histogram[float64]:
				for _, p := range d.DataPoints {
					sets = append(sets, p.Attributes)
				}
			case metricdata.Gauge[int64]:
				for _, p := range d.DataPoints {
					sets = append(sets, p.Attributes)
				}
			}
		}
	}
	return sets
}

func has(sets []attribute.Set, kv ...attribute.KeyValue) bool {
	want := attribute.NewSet(kv...)
	for _, s := range sets {
		if s.Equals(&want) {
			return true
		}
	}
	return false
}

func TestTickRecordsStatusAndReturnsError(t *testing.T) {
	failure := errors.New("owner unavailable")
	if err := Tick(context.Background(), "test-loop", func(context.Context) error { return failure }); !errors.Is(err, failure) {
		t.Fatalf("Tick returned %v", err)
	}
	if err := Tick(context.Background(), "test-loop", func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	got := points(t, "apc.loop.ticks")
	for _, status := range []string{"ok", "error"} {
		if !has(got, attribute.String(KeyComponent, "test-loop"), attribute.String(KeyStatus, status)) {
			t.Errorf("no %s tick recorded: %v", status, got)
		}
	}
	if !has(points(t, "apc.loop.last_success"), attribute.String(KeyComponent, "test-loop")) {
		t.Error("last success not recorded")
	}
}

func TestHTTPRecordsRoutePatternNotPath(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/workspaces/{name}", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) })
	route := func(r *http.Request) string { _, p := mux.Handler(r); return p }
	h := HTTP("test-http", route, mux)
	for _, path := range []string{"/api/v1/workspaces/secret-project?token=abc", "/nowhere/else"} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", path, nil))
	}
	got := points(t, "apc.http.requests")
	if !has(got, attribute.String(KeyComponent, "test-http"), attribute.String(KeyType, "GET /api/v1/workspaces/{name}"), attribute.String(KeyCode, "418")) {
		t.Fatalf("route pattern and status not recorded: %v", got)
	}
	if !has(got, attribute.String(KeyComponent, "test-http"), attribute.String(KeyType, "unmatched"), attribute.String(KeyCode, "404")) {
		t.Fatalf("unmatched request not recorded: %v", got)
	}
	for _, s := range got {
		if strings.Contains(s.Encoded(attribute.DefaultEncoder()), "secret-project") || strings.Contains(s.Encoded(attribute.DefaultEncoder()), "token") {
			t.Fatalf("raw path or query leaked into metrics: %v", s)
		}
	}
}

func TestHTTPKeepsFlushAndUnwrap(t *testing.T) {
	h := HTTP("test-flush", func(*http.Request) string { return "stream" }, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Errorf("flush through middleware failed: %v", err)
		}
		if _, ok := w.(http.Flusher); !ok {
			t.Error("middleware hides http.Flusher")
		}
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
}
