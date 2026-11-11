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
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/time/rate"
)

const (
	// MaxBody bounds one decompressed OTLP request.
	MaxBody = 1 << 20
	// MaxSeries bounds the relay's series, so a worker cannot exhaust the
	// collector's sample limit or the relay's memory.
	MaxSeries = 256
	// MaxConcurrent bounds requests decoded at once.
	MaxConcurrent = 2
)

// Label values a worker supplies (model and tool names) must come from
// finite, structured sets: a syntactically tidy string can still be an ID or
// a secret. Anything else is recorded as "other".
var models = []*regexp.Regexp{
	regexp.MustCompile(`^claude-(opus|sonnet|haiku|fable)-[0-9]{1,2}(-[0-9]{1,2})?(-[0-9]{8})?$`),
	regexp.MustCompile(`^claude-[0-9](-[0-9])?-(opus|sonnet|haiku)(-[0-9]{8})?$`),
	regexp.MustCompile(`^gpt-[0-9]{1,2}(\.[0-9]{1,2})?(-(mini|nano|pro|codex|codex-mini|codex-max|sol))?$`),
	regexp.MustCompile(`^o[0-9](-(mini|pro))?$`),
	regexp.MustCompile(`^grok-[0-9](\.[0-9])?(-(mini|fast|code|code-fast))?$`),
}
var editTools = map[string]bool{"Edit": true, "Write": true, "MultiEdit": true, "NotebookEdit": true}

func modelLabel(s string) string {
	for _, m := range models {
		if m.MatchString(s) {
			return s
		}
	}
	return "other"
}

func toolLabel(s string) string {
	if editTools[s] {
		return s
	}
	return "other"
}

// counterRule maps one known agent metric to a relay counter. Only attributes
// listed in attrs are read, each into a fixed output label.
type counterRule struct {
	out   string
	attrs map[string]string // input attribute -> output label (type, name, status)
	only  map[string]map[string]bool
	// label maps a free-form attribute (model, tool) to a finite value.
	label map[string]func(string) string
}

// Every output counter carries labels component, type, name and status, so
// one series key is the same shape everywhere.
var labelNames = []string{"component", "type", "name", "status"}

var metricRules = map[string]counterRule{
	"claude_code.token.usage":             {out: "tokens", attrs: map[string]string{"type": "type", "model": "name"}, label: map[string]func(string) string{"model": modelLabel}, only: map[string]map[string]bool{"type": {"input": true, "output": true, "cacheRead": true, "cacheCreation": true}}},
	"claude_code.cost.usage":              {out: "cost_usd", attrs: map[string]string{"model": "name"}, label: map[string]func(string) string{"model": modelLabel}},
	"claude_code.session.count":           {out: "sessions"},
	"claude_code.lines_of_code.count":     {out: "lines", attrs: map[string]string{"type": "type"}, only: map[string]map[string]bool{"type": {"added": true, "removed": true}}},
	"claude_code.commit.count":            {out: "commits"},
	"claude_code.pull_request.count":      {out: "pull_requests"},
	"claude_code.code_edit_tool.decision": {out: "tool_decisions", attrs: map[string]string{"tool_name": "name", "decision": "status"}, label: map[string]func(string) string{"tool_name": toolLabel}, only: map[string]map[string]bool{"decision": {"accept": true, "reject": true}}},
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
	busy     chan struct{} // bounds requests decoded at once, across handlers
	// Registry holds every relay series.
	Registry *prometheus.Registry
	// ProxyMetrics, if set, is the proxy's loopback metrics URL merged into /metrics.
	ProxyMetrics string
	client       *http.Client
}

func New() *Relay {
	r := &Relay{series: map[string]bool{}, counters: map[string]*prometheus.CounterVec{}, Registry: prometheus.NewRegistry(),
		limiter: rate.NewLimiter(20, 40), busy: make(chan struct{}, MaxConcurrent), client: &http.Client{Timeout: 2 * time.Second}}
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

// component is the agent kind, from the metric or event prefix only.
func component(name string) (string, string) {
	prefix, rest, ok := strings.Cut(name, ".")
	if !ok || !components[prefix] {
		return "", ""
	}
	return prefix, rest
}

// keepMetric records one data point of a known delta counter.
func (r *Relay) keepMetric(rule counterRule, comp string, p dataPoint) {
	if math.IsNaN(p.value) || math.IsInf(p.value, 0) || p.value <= 0 || p.value > 1e12 {
		r.dropped.WithLabelValues("invalid").Inc()
		return
	}
	labels := map[string]string{"component": comp, "type": "", "name": "", "status": ""}
	for in, out := range rule.attrs {
		v := p.attrs[in]
		if allowed := rule.only[in]; allowed != nil && !allowed[v] {
			r.dropped.WithLabelValues("invalid").Inc()
			return
		}
		if label := rule.label[in]; label != nil {
			v = label(v)
		}
		labels[out] = v
	}
	values := []string{labels["component"], labels["type"], labels["name"], labels["status"]}
	r.add(r.counters[rule.out], rule.out+"|"+strings.Join(values, "|"), p.value, values...)
}

// keepEvent counts one known agent event by name and success.
func (r *Relay) keepEvent(scope, name, success string) {
	comp, event := component(name)
	if comp == "" {
		// Some agents send the bare event name and their kind as the scope name.
		comp, _ = component(scope + ".x")
		event = name
	}
	if comp == "" || !eventNames[event] {
		r.dropped.WithLabelValues("unknown_metric").Inc()
		return
	}
	if success != "true" && success != "false" {
		success = ""
	}
	values := []string{comp, "", event, success}
	r.add(r.events, "events|"+strings.Join(values, "|"), 1, values...)
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

// Intake serves OTLP/HTTP protobuf on /v1/metrics, /v1/logs and
// /v1/traces. Traces are discarded with success, so exporters do not retry
// or log errors in the worker. JSON is refused (415): decoding it would
// allocate per element before any limit applies.
func (r *Relay) Intake() http.Handler {
	mux := http.NewServeMux()
	handle := func(signal string, reduce func([]byte) error) {
		mux.HandleFunc("POST /v1/"+signal, func(w http.ResponseWriter, req *http.Request) {
			code := http.StatusOK
			defer func() { r.requests.WithLabelValues(signal, strconv.Itoa(code)).Inc() }()
			fail := func(status int, reason, msg string) {
				code = status
				r.dropped.WithLabelValues(reason).Inc()
				http.Error(w, msg, status)
			}
			if !r.limiter.Allow() {
				w.Header().Set("Retry-After", "5")
				fail(http.StatusTooManyRequests, "rate_limited", "rate limited")
				return
			}
			select {
			case r.busy <- struct{}{}:
				defer func() { <-r.busy }()
			default:
				w.Header().Set("Retry-After", "5")
				fail(http.StatusTooManyRequests, "rate_limited", "busy")
				return
			}
			if ct := req.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/x-protobuf") && !strings.HasPrefix(ct, "application/protobuf") {
				fail(http.StatusUnsupportedMediaType, "invalid", "OTLP/HTTP protobuf required")
				return
			}
			raw, err := r.body(req)
			if err != nil {
				fail(http.StatusRequestEntityTooLarge, "too_large", "request too large")
				return
			}
			if err := reduce(raw); errors.Is(err, errTooMany) {
				fail(http.StatusRequestEntityTooLarge, "too_large", "too many records")
				return
			} else if err != nil {
				fail(http.StatusBadRequest, "invalid", "invalid OTLP")
				return
			}
			w.Header().Set("Content-Type", "application/x-protobuf")
			w.WriteHeader(code)
		})
	}
	handle("metrics", r.reduceMetrics)
	handle("logs", r.reduceLogs)
	handle("traces", func([]byte) error {
		r.dropped.WithLabelValues("traces").Inc()
		return nil
	})
	return mux
}
