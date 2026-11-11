// Package cellrelay is the cell telemetry relay: a separate container in each
// cell's proxy pod (decision GT A). The worker's coding agents send OTLP/HTTP
// to it on port 4318, the only telemetry path a worker has. The relay keeps an
// allowlist of counters, labelled only with bounded values it chooses, and
// serves them, with the proxy's own counters, as Prometheus text on /metrics
// for the cluster collector to scrape. Nothing leaves the cell by push.
//
// What it never keeps: spans, log bodies, prompts, tool arguments, file
// contents, hosts, user or session identifiers, and the sender's resource
// attributes. The cell's identity is the scrape target's namespace and pod,
// which Kubernetes service discovery sets; a worker cannot name itself.
package cellrelay

import (
	"compress/gzip"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	collogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	colmetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	common "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	"golang.org/x/time/rate"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const (
	// MaxBody bounds one decompressed OTLP request.
	MaxBody = 1 << 20
	// MaxSeries bounds the relay's series, so a worker cannot exhaust the
	// collector's sample limit or the relay's memory.
	MaxSeries = 256
)

// value is a bounded label value: models, tools and event names fit; free text,
// paths and identifiers do not and become "other".
var value = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,63}$`)

func bounded(s string) string {
	if value.MatchString(s) {
		return s
	}
	if s == "" {
		return ""
	}
	return "other"
}

// counterRule maps one known agent metric to a relay counter. Only attributes
// listed in attrs are read, each into a fixed output label.
type counterRule struct {
	out   string
	attrs map[string]string // input attribute -> output label (type, name, status)
	only  map[string]map[string]bool
}

// Every output counter carries labels component, type, name and status, so
// one series key is the same shape everywhere.
var labelNames = []string{"component", "type", "name", "status"}

var metricRules = map[string]counterRule{
	"claude_code.token.usage":             {out: "tokens", attrs: map[string]string{"type": "type", "model": "name"}, only: map[string]map[string]bool{"type": {"input": true, "output": true, "cacheRead": true, "cacheCreation": true}}},
	"claude_code.cost.usage":              {out: "cost_usd", attrs: map[string]string{"model": "name"}},
	"claude_code.session.count":           {out: "sessions"},
	"claude_code.lines_of_code.count":     {out: "lines", attrs: map[string]string{"type": "type"}, only: map[string]map[string]bool{"type": {"added": true, "removed": true}}},
	"claude_code.commit.count":            {out: "commits"},
	"claude_code.pull_request.count":      {out: "pull_requests"},
	"claude_code.code_edit_tool.decision": {out: "tool_decisions", attrs: map[string]string{"tool_name": "name", "decision": "status"}, only: map[string]map[string]bool{"decision": {"accept": true, "reject": true}}},
	"claude_code.active_time.total":       {out: "active_seconds", attrs: map[string]string{"type": "type"}, only: map[string]map[string]bool{"type": {"user": true, "cli": true}}},
}

// Events are counted by name only; their bodies and attributes are dropped
// except a success flag. Names are agent event names without the prefix.
var eventNames = map[string]bool{
	"api_request": true, "api_error": true, "tool_result": true, "tool_decision": true,
	"user_prompt": true, "sse_event": true, "conversation_starts": true,
}
var components = map[string]bool{"claude_code": true, "codex": true}

// Relay holds the reduced counters. Its zero value is not usable; use New.
type Relay struct {
	mu       sync.Mutex
	series   map[string]bool
	counters map[string]*prometheus.CounterVec
	events   *prometheus.CounterVec
	requests *prometheus.CounterVec
	dropped  *prometheus.CounterVec
	limiter  *rate.Limiter
	// Registry holds every relay series.
	Registry *prometheus.Registry
	// ProxyMetrics, if set, is the proxy's loopback metrics URL merged into /metrics.
	ProxyMetrics string
	client       *http.Client
}

func New() *Relay {
	r := &Relay{series: map[string]bool{}, counters: map[string]*prometheus.CounterVec{}, Registry: prometheus.NewRegistry(),
		limiter: rate.NewLimiter(20, 40), client: &http.Client{Timeout: 2 * time.Second}}
	outs := map[string]string{
		"tokens": "Tokens used by the cell's coding agents, by type and model (name).", "cost_usd": "Agent-reported cost in USD by model (name).",
		"sessions": "Agent sessions started.", "lines": "Lines changed by agents, by type (added, removed).",
		"commits": "Commits created by agents.", "pull_requests": "Pull requests created by agents.",
		"tool_decisions": "Edit tool decisions by tool (name) and decision (status).", "active_seconds": "Agent active time by type (user, cli).",
	}
	for out, help := range outs {
		v := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "glovecell_cell_agent_" + out + "_total", Help: help}, labelNames)
		r.counters[out] = v
		r.Registry.MustRegister(v)
	}
	r.events = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "glovecell_cell_agent_events_total", Help: "Agent events by event (name) and success (status); bodies are never kept."}, labelNames)
	r.requests = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "glovecell_cell_relay_requests_total", Help: "OTLP requests received by signal (type) and HTTP status (code)."}, []string{"type", "code"})
	r.dropped = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "glovecell_cell_relay_dropped_total", Help: "Telemetry dropped by the relay, by reason."}, []string{"reason"})
	r.Registry.MustRegister(r.events, r.requests, r.dropped)
	for _, reason := range []string{"traces", "unknown_metric", "cumulative", "series_limit", "too_large", "rate_limited", "invalid"} {
		r.dropped.WithLabelValues(reason)
	}
	return r
}

// add records n on counter vec with labels, unless that would create a series
// beyond MaxSeries.
func (r *Relay) add(vec *prometheus.CounterVec, key string, n float64, labels ...string) {
	if n <= 0 {
		return
	}
	r.mu.Lock()
	known := r.series[key]
	if !known && len(r.series) >= MaxSeries {
		r.mu.Unlock()
		r.dropped.WithLabelValues("series_limit").Inc()
		return
	}
	r.series[key] = true
	r.mu.Unlock()
	vec.WithLabelValues(labels...).Add(n)
}

func attrString(attrs []*common.KeyValue, key string) (string, bool) {
	for _, kv := range attrs {
		if kv.GetKey() != key {
			continue
		}
		switch v := kv.GetValue().GetValue().(type) {
		case *common.AnyValue_StringValue:
			return v.StringValue, true
		case *common.AnyValue_BoolValue:
			return strconv.FormatBool(v.BoolValue), true
		}
		return "", true
	}
	return "", false
}

// component is the agent kind, from the metric or event prefix only.
func component(name string) (string, string) {
	prefix, rest, ok := strings.Cut(name, ".")
	if !ok || !components[prefix] {
		return "", ""
	}
	return prefix, rest
}

// Metrics reduces one OTLP metrics export. Only delta sums of known metrics
// count; everything else is dropped and counted by reason.
func (r *Relay) Metrics(req *colmetrics.ExportMetricsServiceRequest) {
	for _, rm := range req.GetResourceMetrics() {
		for _, sm := range rm.GetScopeMetrics() {
			for _, m := range sm.GetMetrics() {
				rule, ok := metricRules[m.GetName()]
				comp, _ := component(m.GetName())
				sum := m.GetSum()
				if !ok || sum == nil || comp == "" {
					r.dropped.WithLabelValues("unknown_metric").Inc()
					continue
				}
				if sum.GetAggregationTemporality() != metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_DELTA {
					r.dropped.WithLabelValues("cumulative").Inc()
					continue
				}
				for _, dp := range sum.GetDataPoints() {
					n := dp.GetAsDouble()
					if _, isInt := dp.GetValue().(*metricspb.NumberDataPoint_AsInt); isInt {
						n = float64(dp.GetAsInt())
					}
					labels := map[string]string{"component": comp, "type": "", "name": "", "status": ""}
					valid := true
					for in, out := range rule.attrs {
						v, _ := attrString(dp.GetAttributes(), in)
						if allowed := rule.only[in]; allowed != nil && !allowed[v] {
							valid = false
							break
						}
						labels[out] = bounded(v)
					}
					if !valid {
						r.dropped.WithLabelValues("invalid").Inc()
						continue
					}
					values := []string{labels["component"], labels["type"], labels["name"], labels["status"]}
					r.add(r.counters[rule.out], rule.out+"|"+strings.Join(values, "|"), n, values...)
				}
			}
		}
	}
}

// Logs counts known agent events by name and success. Bodies, prompts and
// every other attribute are discarded unread.
func (r *Relay) Logs(req *collogs.ExportLogsServiceRequest) {
	for _, rl := range req.GetResourceLogs() {
		for _, sl := range rl.GetScopeLogs() {
			for _, rec := range sl.GetLogRecords() {
				name := rec.GetEventName()
				if name == "" {
					name, _ = attrString(rec.GetAttributes(), "event.name")
				}
				comp, event := component(name)
				if comp == "" {
					// Some agents send the bare event name and their kind in a scope name.
					comp, _ = component(sl.GetScope().GetName() + ".x")
					event = name
				}
				if comp == "" || !eventNames[event] {
					r.dropped.WithLabelValues("unknown_metric").Inc()
					continue
				}
				status := ""
				if v, ok := attrString(rec.GetAttributes(), "success"); ok && (v == "true" || v == "false") {
					status = v
				}
				values := []string{comp, "", event, status}
				r.add(r.events, "events|"+strings.Join(values, "|"), 1, values...)
			}
		}
	}
}

func (r *Relay) body(req *http.Request) ([]byte, error) {
	var in io.Reader = http.MaxBytesReader(nil, req.Body, MaxBody)
	if strings.EqualFold(req.Header.Get("Content-Encoding"), "gzip") {
		z, err := gzip.NewReader(in)
		if err != nil {
			return nil, err
		}
		defer z.Close()
		in = z
	}
	raw, err := io.ReadAll(io.LimitReader(in, MaxBody+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > MaxBody {
		return nil, errors.New("too large")
	}
	return raw, nil
}

// Intake serves OTLP/HTTP (protobuf or JSON) on /v1/metrics, /v1/logs and
// /v1/traces. Traces are read and discarded with success, so exporters do
// not retry or log errors in the worker.
func (r *Relay) Intake() http.Handler {
	mux := http.NewServeMux()
	handle := func(signal string, decode func([]byte, bool) error) {
		mux.HandleFunc("POST /v1/"+signal, func(w http.ResponseWriter, req *http.Request) {
			code := http.StatusOK
			defer func() { r.requests.WithLabelValues(signal, strconv.Itoa(code)).Inc() }()
			if !r.limiter.Allow() {
				code = http.StatusTooManyRequests
				r.dropped.WithLabelValues("rate_limited").Inc()
				w.Header().Set("Retry-After", "5")
				http.Error(w, "rate limited", code)
				return
			}
			raw, err := r.body(req)
			if err != nil {
				code = http.StatusRequestEntityTooLarge
				r.dropped.WithLabelValues("too_large").Inc()
				http.Error(w, "request too large", code)
				return
			}
			json := strings.HasPrefix(req.Header.Get("Content-Type"), "application/json")
			if err := decode(raw, json); err != nil {
				code = http.StatusBadRequest
				r.dropped.WithLabelValues("invalid").Inc()
				http.Error(w, "invalid OTLP", code)
				return
			}
			if json {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte("{}"))
				return
			}
			w.Header().Set("Content-Type", "application/x-protobuf")
			w.WriteHeader(code)
		})
	}
	unmarshal := func(raw []byte, json bool, m proto.Message) error {
		if json {
			return protojson.UnmarshalOptions{DiscardUnknown: true}.Unmarshal(raw, m)
		}
		return proto.Unmarshal(raw, m)
	}
	handle("metrics", func(raw []byte, json bool) error {
		var m colmetrics.ExportMetricsServiceRequest
		if err := unmarshal(raw, json, &m); err != nil {
			return err
		}
		r.Metrics(&m)
		return nil
	})
	handle("logs", func(raw []byte, json bool) error {
		var m collogs.ExportLogsServiceRequest
		if err := unmarshal(raw, json, &m); err != nil {
			return err
		}
		r.Logs(&m)
		return nil
	})
	handle("traces", func([]byte, bool) error {
		r.dropped.WithLabelValues("traces").Inc()
		return nil
	})
	return mux
}
