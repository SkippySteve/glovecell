package store

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

// Status is the database state the console and readiness check report.
type Status struct {
	// Configured is false when the controller runs without a database
	// (legacy ConfigMap history; approvals and requests are refused).
	Configured bool `json:"configured"`
	// Ready is true when the schema is migrated and the last ping succeeded.
	Ready bool `json:"ready"`
	// Reason explains a false Ready in words the console shows.
	Reason string    `json:"reason,omitempty"`
	Since  time.Time `json:"since,omitzero"`
}

// Monitor migrates the schema (retrying until it succeeds), checks the
// database periodically, prunes old records and reports Status. A nil
// *Monitor reports an unconfigured database.
type Monitor struct {
	Store Store
	// Migrate applies the schema; nil when the store needs none.
	Migrate func(context.Context) error
	// OnReady runs once after the first successful migration, for example
	// the one-time audit ConfigMap import.
	OnReady func(context.Context)
	// Retention is applied every PruneEvery (default 1h).
	Retention  Retention
	PruneEvery time.Duration
	// Interval between health checks (default 10s).
	Interval time.Duration

	mu       sync.Mutex
	status   Status
	migrated bool
}

// NewMonitor returns a Monitor for s, initially not ready.
func NewMonitor(s Store, migrate func(context.Context) error) *Monitor {
	return &Monitor{Store: s, Migrate: migrate, status: Status{Configured: true, Reason: "database not checked yet", Since: time.Now().UTC()}}
}

// Status returns the current state.
func (m *Monitor) Status() Status {
	if m == nil {
		return Status{Reason: "no database is configured: approvals and access requests are turned off"}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status
}

// Check is the readiness check: it fails while the database is unusable.
func (m *Monitor) Check(_ *http.Request) error {
	s := m.Status()
	if !s.Configured || s.Ready {
		return nil
	}
	return errors.New(s.Reason)
}

func (m *Monitor) set(ready bool, reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.status.Ready != ready || m.status.Reason != reason {
		if ready {
			slog.Info("database available")
		} else {
			slog.Warn("database unavailable; new approvals and access requests are refused", "reason", reason)
		}
		m.status = Status{Configured: true, Ready: ready, Reason: reason, Since: time.Now().UTC()}
	}
}

// Reason words for the console.
func unavailableReason(err error) string {
	if err == nil {
		return ""
	}
	return "database unavailable: " + err.Error()
}

// Step runs one migration-or-ping round and reports whether the store is
// usable. Run calls it on a timer; tests call it directly.
func (m *Monitor) Step(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	m.mu.Lock()
	migrated := m.migrated
	m.mu.Unlock()
	if !migrated {
		if m.Migrate != nil {
			if err := m.Migrate(ctx); err != nil {
				m.set(false, unavailableReason(err))
				return false
			}
		}
		m.mu.Lock()
		m.migrated = true
		m.mu.Unlock()
		if m.OnReady != nil {
			m.OnReady(ctx)
		}
	}
	pingCtx, cancelPing := context.WithTimeout(ctx, 5*time.Second)
	defer cancelPing()
	if err := m.Store.Ping(pingCtx); err != nil {
		m.set(false, unavailableReason(err))
		return false
	}
	m.set(true, "")
	return true
}

// Run loops until ctx ends.
func (m *Monitor) Run(ctx context.Context) {
	interval := m.Interval
	if interval <= 0 {
		interval = 10 * time.Second
	}
	pruneEvery := m.PruneEvery
	if pruneEvery <= 0 {
		pruneEvery = time.Hour
	}
	meter := otel.Meter("agent-project-controller")
	if gauge, err := meter.Int64ObservableGauge("apc.database.up", metric.WithDescription("1 when the controller database is migrated and reachable")); err == nil {
		_, _ = meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
			v := int64(0)
			if m.Status().Ready {
				v = 1
			}
			o.ObserveInt64(gauge, v)
			return nil
		}, gauge)
	}
	var lastPrune time.Time
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if m.Step(ctx) && time.Since(lastPrune) >= pruneEvery {
			lastPrune = time.Now()
			pruneCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
			n, err := m.Store.Prune(pruneCtx, m.Retention, time.Now().UTC())
			cancel()
			if err != nil {
				slog.Warn("database retention failed; retrying later", "error", err.Error())
			} else if n.History+n.Requests+n.ProxySummaries > 0 {
				slog.Info("database retention applied", "history", n.History, "requests", n.Requests, "proxySummaries", n.ProxySummaries)
			}
		}
		timer.Reset(interval)
	}
}
