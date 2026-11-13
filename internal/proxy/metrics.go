package proxy

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// MetricsAddressEnv names the proxy's metrics listener. It must be a loopback
// address: only the telemetry relay in the same pod reads it (decision GT A).
const MetricsAddressEnv = "APC_PROXY_METRICS_ADDR"

var (
	metricsRegistry = prometheus.NewRegistry()
	requests        = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "glovecell_cell_proxy_requests_total", Help: "Inspecting proxy decisions by reason (a fixed set) and HTTP status code."}, []string{"reason", "code"})
)

func init() { metricsRegistry.MustRegister(requests) }

// countRequest records one proxy decision. It carries only a fixed reason
// and the status code: never the destination host, path, headers or body,
// which are the cell's private work.
func countRequest(_ context.Context, reason string, code int) {
	requests.WithLabelValues(reason, strconv.Itoa(code)).Inc()
}

// serveMetrics serves the counters on a loopback address until ctx ends. A
// non-loopback address is refused: the proxy never exposes a listener to the
// worker or the cluster other than its inspected port.
func serveMetrics(ctx context.Context, addr string) error {
	host, _, err := net.SplitHostPort(addr)
	ip := net.ParseIP(host)
	if err != nil || ip == nil || !ip.IsLoopback() || addr == ControlAddress {
		return errors.New("proxy metrics address must be loopback and not the control address")
	}
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.HandlerFor(metricsRegistry, promhttp.HandlerOpts{}))
	server := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, MaxHeaderBytes: 4 << 10}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return errors.New("proxy metrics listener unavailable")
	}
	go func() { <-ctx.Done(); _ = server.Close() }()
	go func() { _ = server.Serve(listener) }()
	return nil
}
