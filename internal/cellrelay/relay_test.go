package cellrelay

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	collogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	colmetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	common "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func str(k, v string) *common.KeyValue {
	return &common.KeyValue{Key: k, Value: &common.AnyValue{Value: &common.AnyValue_StringValue{StringValue: v}}}
}

// spoofed resource attributes a hostile worker might send; none may appear.
var spoofed = []*common.KeyValue{str("service.name", "agent-project-controller"), str("k8s.namespace.name", "agent-project-controller"), str("user.email", "steve@example.invalid")}

func sum(name string, temporality metricspb.AggregationTemporality, points ...*metricspb.NumberDataPoint) *colmetrics.ExportMetricsServiceRequest {
	return &colmetrics.ExportMetricsServiceRequest{ResourceMetrics: []*metricspb.ResourceMetrics{{
		Resource: &resourcepb.Resource{Attributes: spoofed},
		ScopeMetrics: []*metricspb.ScopeMetrics{{Metrics: []*metricspb.Metric{{Name: name,
			Data: &metricspb.Metric_Sum{Sum: &metricspb.Sum{AggregationTemporality: temporality, IsMonotonic: true, DataPoints: points}}}}}},
	}}}
}

func point(v float64, attrs ...*common.KeyValue) *metricspb.NumberDataPoint {
	return &metricspb.NumberDataPoint{Attributes: attrs, Value: &metricspb.NumberDataPoint_AsDouble{AsDouble: v}}
}

const delta = metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_DELTA

func scrape(t *testing.T, r *Relay) string {
	t.Helper()
	rec := httptest.NewRecorder()
	r.MetricsHandler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if rec.Code != 200 {
		t.Fatalf("scrape status %d", rec.Code)
	}
	return rec.Body.String()
}

func post(t *testing.T, r *Relay, path, contentType string, body []byte, headers ...string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", path, bytes.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	r.Intake().ServeHTTP(rec, req)
	return rec
}

func protobuf(t *testing.T, m proto.Message) []byte {
	t.Helper()
	b, err := proto.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestKnownMetricsAreReducedToBoundedLabels(t *testing.T) {
	r := New()
	req := sum("claude_code.token.usage", delta,
		point(120, str("type", "input"), str("model", "claude-sonnet-4-5"), str("session.id", "secret-session"), str("user.account_uuid", "acct-1")),
		point(30, str("type", "output"), str("model", "claude-sonnet-4-5")),
		point(5, str("type", "prompt text pretending to be a type"), str("model", "claude-sonnet-4-5")),
		point(7, str("type", "input"), str("model", "/home/steve/private path with spaces")))
	if rec := post(t, r, "/v1/metrics", "application/x-protobuf", protobuf(t, req)); rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	out := scrape(t, r)
	for _, want := range []string{
		`glovecell_cell_agent_tokens_total{component="claude_code",name="claude-sonnet-4-5",status="",type="input"} 120`,
		`glovecell_cell_agent_tokens_total{component="claude_code",name="claude-sonnet-4-5",status="",type="output"} 30`,
		`glovecell_cell_agent_tokens_total{component="claude_code",name="other",status="",type="input"} 7`,
		`glovecell_cell_relay_dropped_total{reason="invalid"} 1`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s in\n%s", want, out)
		}
	}
	for _, leak := range []string{"secret-session", "acct-1", "steve@example.invalid", "agent-project-controller", "private path", "prompt text"} {
		if strings.Contains(out, leak) {
			t.Errorf("%q leaked into /metrics", leak)
		}
	}
}

func TestUnknownAndCumulativeMetricsAreDropped(t *testing.T) {
	r := New()
	r.Metrics(sum("claude_code.token.usage", metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE, point(99, str("type", "input"), str("model", "m"))))
	r.Metrics(sum("worker.exfiltration.bytes", delta, point(1, str("data", "anything"))))
	r.Metrics(sum("evil.token.usage", delta, point(1, str("type", "input"))))
	out := scrape(t, r)
	if strings.Contains(out, "glovecell_cell_agent_tokens_total{") || strings.Contains(out, "exfiltration") {
		t.Fatalf("dropped metric was kept:\n%s", out)
	}
	for _, want := range []string{`glovecell_cell_relay_dropped_total{reason="cumulative"} 1`, `glovecell_cell_relay_dropped_total{reason="unknown_metric"} 2`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s", want)
		}
	}
}

func TestEventsAreCountedWithoutBodiesOrAttributes(t *testing.T) {
	r := New()
	logs := &collogs.ExportLogsServiceRequest{ResourceLogs: []*logspb.ResourceLogs{{
		Resource: &resourcepb.Resource{Attributes: spoofed},
		ScopeLogs: []*logspb.ScopeLogs{{LogRecords: []*logspb.LogRecord{
			{EventName: "claude_code.user_prompt", Body: &common.AnyValue{Value: &common.AnyValue_StringValue{StringValue: "please read ~/.ssh/id_ed25519"}}, Attributes: []*common.KeyValue{str("prompt", "please read ~/.ssh/id_ed25519")}},
			{Attributes: []*common.KeyValue{str("event.name", "codex.tool_result"), str("success", "true"), str("arguments", "rm -rf /")}},
			{EventName: "claude_code.made_up_event"},
			{EventName: "not_an_agent.api_request"},
		}}},
	}}}
	raw, err := protojson.Marshal(logs)
	if err != nil {
		t.Fatal(err)
	}
	if rec := post(t, r, "/v1/logs", "application/json", raw); rec.Code != 200 || rec.Body.String() != "{}" {
		t.Fatalf("status %d body %q", rec.Code, rec.Body.String())
	}
	out := scrape(t, r)
	for _, want := range []string{
		`glovecell_cell_agent_events_total{component="claude_code",name="user_prompt",status="",type=""} 1`,
		`glovecell_cell_agent_events_total{component="codex",name="tool_result",status="true",type=""} 1`,
		`glovecell_cell_relay_dropped_total{reason="unknown_metric"} 2`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s in\n%s", want, out)
		}
	}
	for _, leak := range []string{"ssh", "rm -rf", "made_up_event"} {
		if strings.Contains(out, leak) {
			t.Errorf("%q leaked into /metrics", leak)
		}
	}
}

func TestTracesAreAcceptedAndDiscarded(t *testing.T) {
	r := New()
	rec := post(t, r, "/v1/traces", "application/x-protobuf", []byte("span bytes with secrets"))
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	out := scrape(t, r)
	if !strings.Contains(out, `glovecell_cell_relay_dropped_total{reason="traces"} 1`) || strings.Contains(out, "secrets") {
		t.Fatalf("traces not discarded:\n%s", out)
	}
}

func TestOversizedCompressedAndInvalidRequestsAreRefused(t *testing.T) {
	r := New()
	var z bytes.Buffer
	gz := gzip.NewWriter(&z)
	_, _ = gz.Write(bytes.Repeat([]byte{0}, MaxBody+10)) // tiny compressed, too large decompressed
	_ = gz.Close()
	if rec := post(t, r, "/v1/metrics", "application/x-protobuf", z.Bytes(), "Content-Encoding", "gzip"); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("decompression bomb: status %d", rec.Code)
	}
	if rec := post(t, r, "/v1/metrics", "application/x-protobuf", []byte{0xff, 0xff, 0xff}); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid protobuf: status %d", rec.Code)
	}
	req := httptest.NewRequest("GET", "/v1/metrics", nil)
	rec := httptest.NewRecorder()
	r.Intake().ServeHTTP(rec, req)
	if rec.Code == 200 {
		t.Fatal("intake accepted GET")
	}
	// The intake never serves /metrics, and the metrics port never takes OTLP.
	rec = httptest.NewRecorder()
	r.Intake().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if rec.Code == 200 {
		t.Fatal("intake served /metrics")
	}
	rec = httptest.NewRecorder()
	r.MetricsHandler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/metrics", nil))
	if rec.Code == 200 {
		t.Fatal("metrics port accepted OTLP")
	}
}

func TestRateLimit(t *testing.T) {
	r := New()
	body := protobuf(t, sum("claude_code.session.count", delta, point(1)))
	limited := 0
	for i := 0; i < 100; i++ {
		if post(t, r, "/v1/metrics", "application/x-protobuf", body).Code == http.StatusTooManyRequests {
			limited++
		}
	}
	if limited == 0 {
		t.Fatal("no request was rate limited")
	}
}

func TestSeriesAreCapped(t *testing.T) {
	r := New()
	for i := 0; i < MaxSeries+50; i++ {
		r.Metrics(sum("claude_code.token.usage", delta, point(1, str("type", "input"), str("model", fmt.Sprintf("model-%d", i)))))
	}
	out := scrape(t, r)
	if n := strings.Count(out, "glovecell_cell_agent_tokens_total{"); n > MaxSeries {
		t.Fatalf("%d series exceed the cap", n)
	}
	if !strings.Contains(out, `glovecell_cell_relay_dropped_total{reason="series_limit"} 50`) {
		t.Fatalf("series limit not counted:\n%s", out)
	}
}

func TestProxyMetricsAreMergedByPrefixOnly(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "# TYPE glovecell_cell_proxy_requests_total counter\nglovecell_cell_proxy_requests_total{code=\"403\",reason=\"destination_denied\"} 4\n# TYPE glovecell_cell_agent_tokens_total counter\nglovecell_cell_agent_tokens_total{component=\"x\"} 1e9\n")
	}))
	defer proxy.Close()
	r := New()
	r.ProxyMetrics = proxy.URL
	out := scrape(t, r)
	if !strings.Contains(out, `glovecell_cell_proxy_requests_total{code="403",reason="destination_denied"} 4`) {
		t.Fatalf("proxy metrics missing:\n%s", out)
	}
	if strings.Contains(out, "1e+09") || strings.Contains(out, `component="x"`) {
		t.Fatalf("non-proxy family accepted from the proxy endpoint:\n%s", out)
	}
	r.ProxyMetrics = "http://127.0.0.1:1/metrics"
	if out := scrape(t, r); !strings.Contains(out, "glovecell_cell_relay_requests_total") && !strings.Contains(out, "glovecell_cell_relay_dropped_total") {
		t.Fatalf("relay series missing when the proxy is down:\n%s", out)
	}
}
