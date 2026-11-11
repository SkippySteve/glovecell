package cellrelay

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
)

const (
	// IntakeAddress receives OTLP from the cell's worker only (network policy).
	IntakeAddress = ":4318"
	// MetricsAddress is scraped by the cluster collector only (network policy).
	MetricsAddress = ":9464"
	// ProxyMetricsAddress is the proxy's loopback-only metrics listener in the
	// same pod; the relay merges it into /metrics.
	ProxyMetricsAddress = "127.0.0.1:9465"
	// ProxyPrefix is the only metric family prefix accepted from the proxy.
	ProxyPrefix = "glovecell_cell_proxy_"
)

// proxyGatherer reads the proxy's loopback metrics and keeps only its own
// families. A missing proxy endpoint yields no families, not an error, so the
// relay's own series still serve.
type proxyGatherer struct{ r *Relay }

func (g proxyGatherer) Gather() ([]*dto.MetricFamily, error) {
	if g.r.ProxyMetrics == "" {
		return nil, nil
	}
	res, err := g.r.client.Get(g.r.ProxyMetrics)
	if err != nil {
		return nil, nil
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, nil
	}
	parser := expfmt.NewTextParser(model.UTF8Validation)
	families, err := parser.TextToMetricFamilies(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, nil
	}
	out := []*dto.MetricFamily{}
	for name, f := range families {
		if strings.HasPrefix(name, ProxyPrefix) {
			out = append(out, f)
		}
	}
	return out, nil
}

// MetricsHandler serves GET /metrics only.
func (r *Relay) MetricsHandler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.HandlerFor(prometheus.Gatherers{r.Registry, proxyGatherer{r}}, promhttp.HandlerOpts{ErrorHandling: promhttp.ContinueOnError}))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	return mux
}

// Run serves the intake and the metrics endpoint until ctx ends.
func Run(ctx context.Context) error {
	r := New()
	r.ProxyMetrics = "http://" + ProxyMetricsAddress + "/metrics"
	if v := os.Getenv("CELL_TELEMETRY_PROXY_METRICS"); v == "off" {
		r.ProxyMetrics = ""
	}
	intake := &http.Server{Addr: IntakeAddress, Handler: r.Intake(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	metrics := &http.Server{Addr: MetricsAddress, Handler: r.MetricsHandler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8 << 10}
	failures := make(chan error, 2)
	for _, s := range []*http.Server{intake, metrics} {
		go func(s *http.Server) {
			if err := s.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				failures <- err
			}
		}(s)
	}
	slog.Info("cell telemetry relay listening", "intake", IntakeAddress, "metrics", MetricsAddress)
	select {
	case <-ctx.Done():
	case err := <-failures:
		_ = intake.Close()
		_ = metrics.Close()
		return err
	}
	c, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return errors.Join(intake.Shutdown(c), metrics.Shutdown(c))
}
