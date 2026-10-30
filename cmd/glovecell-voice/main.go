// Command glovecell-voice is the voice service for Glovecell (decision
// D15): a Matrix bot with end-to-end encryption that turns Steve's voice
// notes into agent tasks and reads results back, the /v1/notify API agents
// use to reach him, and the voice approval gate that merges PRs on his
// verified spoken approval. See docs/voice.md and docs/approval-gate.md.
//
//	glovecell-voice serve        run the service (default)
//	glovecell-voice hash-token   read a token on stdin, print its SHA-256
//	glovecell-voice version
//
// It runs in the trusted system namespace, never in a sandbox, as one
// replica (one Matrix device).
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/stdlib"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/approvalgate"
	apcstore "git.cluster.aimingupward.com/steve/agent-project-controller/internal/store"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/voice/matrix"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/voice/notify"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/voice/service"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/voice/speech"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/voice/store"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/voice/targets"
)

var (
	version = "dev"
	commit  = "unknown"
)

func main() {
	level := slog.LevelInfo
	if os.Getenv(envLogLevel) == "debug" {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level})))
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	var err error
	switch cmd {
	case "serve":
		err = serve()
	case "hash-token":
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if len(line) < 32 {
			err = errors.New("tokens must be at least 32 characters (openssl rand -hex 32)")
		} else {
			fmt.Println(notify.HashToken(trim(line)))
		}
	case "version", "--version":
		err = json.NewEncoder(os.Stdout).Encode(map[string]any{"version": version, "commit": commit, "e2ee": matrix.Enabled})
	default:
		err = fmt.Errorf("unknown command %q (serve, hash-token, version)", cmd)
	}
	if err != nil {
		slog.Error("glovecell-voice failed", "error", err.Error())
		os.Exit(1)
	}
}

func trim(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r' || s[len(s)-1] == ' ') {
		s = s[:len(s)-1]
	}
	return s
}

type metrics struct {
	notify    *prometheus.CounterVec
	decisions *prometheus.CounterVec
	tasks     *prometheus.CounterVec
	ready     prometheus.Gauge
}

func newMetrics(reg *prometheus.Registry) *metrics {
	m := &metrics{
		notify:    prometheus.NewCounterVec(prometheus.CounterOpts{Name: "glovecell_voice_notify_requests_total", Help: "Notify API requests by caller, kind and outcome."}, []string{"caller", "kind", "outcome"}),
		decisions: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "glovecell_voice_gate_decisions_total", Help: "Approval gate decisions by kind and outcome."}, []string{"kind", "outcome"}),
		tasks:     prometheus.NewCounterVec(prometheus.CounterOpts{Name: "glovecell_voice_tasks_finished_total", Help: "Agent tasks finished by target and state."}, []string{"target", "state"}),
		ready:     prometheus.NewGauge(prometheus.GaugeOpts{Name: "glovecell_voice_ready", Help: "1 when the database is migrated and Matrix has synced."}),
	}
	reg.MustRegister(m.notify, m.decisions, m.tasks, m.ready, prometheus.NewGoCollector(), prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}))
	return m
}

func serve() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if !matrix.Enabled {
		return matrix.ErrNoE2EE
	}
	cfgPath := os.Getenv(envConfig)
	if cfgPath == "" {
		cfgPath = "/etc/glovecell-voice/config.json"
	}
	cfg, err := loadConfig(cfgPath)
	if err != nil {
		return err
	}

	// Database: verify-full TLS through the controller's store config.
	dbURLFile := os.Getenv(envDatabaseURL)
	if dbURLFile == "" {
		return fmt.Errorf("%s is required", envDatabaseURL)
	}
	dbc, err := apcstore.ConfigFromFiles(dbURLFile, os.Getenv(envDatabaseCA))
	if err != nil {
		return err
	}
	if n, err := strconv.Atoi(os.Getenv(envDatabaseMaxConns)); err == nil && n > 0 {
		dbc.MaxConns = int32(n)
	}
	db, err := store.Open(ctx, dbc)
	if err != nil {
		return err
	}
	defer db.Close()

	// Speech.
	speechKey, err := secret(envSpeechKey, false)
	if err != nil {
		return err
	}
	sp, err := speech.New(speech.Config{BaseURL: cfg.Speech.BaseURL, APIKey: speechKey, STTModel: cfg.Speech.STTModel, TTSModel: cfg.Speech.TTSModel,
		Voice: cfg.Speech.Voice, Language: cfg.Speech.Language, Speed: cfg.Speech.Speed})
	if err != nil {
		return err
	}

	// Targets.
	reg := targets.NewRegistry()
	for name, t := range cfg.Targets {
		switch t.Kind {
		case "orca":
			token, err := secret(envOrcaToken, true)
			if err != nil {
				return err
			}
			o, err := targets.NewOrca(targets.OrcaConfig{Name: name, HandoffURL: t.HandoffURL, RunRecordsURL: t.RunRecordsURL, Token: token, Agent: t.Agent,
				DefaultRepo: t.DefaultRepo, Base: t.Base, BriefFooter: t.BriefFooter})
			if err != nil {
				return fmt.Errorf("target %s: %w", name, err)
			}
			reg.Add(o, t.Aliases...)
		case "archon":
			reg.Add(targets.Archon{}, t.Aliases...)
		default:
			return fmt.Errorf("target %s: kind must be orca or archon", name)
		}
	}

	var sum service.Summarizer
	if cfg.Summarizer.Enabled {
		key, err := secret(envSummarizerKey, false)
		if err != nil {
			return err
		}
		if sum, err = service.NewChatSummarizer(cfg.Summarizer.BaseURL, cfg.Summarizer.Model, key); err != nil {
			return err
		}
	}

	// Matrix.
	password, err := secret(envMatrixPassword, false)
	if err != nil {
		return err
	}
	accessToken, err := secret(envMatrixToken, false)
	if err != nil {
		return err
	}
	pickle, err := secret(envPickleKey, true)
	if err != nil {
		return err
	}
	var svc *service.Service
	var roomIDs []string
	for _, r := range cfg.Rooms {
		roomIDs = append(roomIDs, r.ID)
	}
	bot, err := matrix.New(matrix.Config{Homeserver: cfg.Matrix.Homeserver, UserID: cfg.Matrix.UserID, Password: password, AccessToken: accessToken,
		DeviceID: cfg.Matrix.DeviceID, DeviceName: orDefault(cfg.Matrix.DeviceName, "glovecell-voice"), PickleKey: []byte(pickle), Steve: cfg.Steve, Rooms: roomIDs,
		Trust: matrix.TrustPolicy{PinnedMasterKey: cfg.Matrix.PinnedMasterKey}, MaxEventAge: duration(cfg.Matrix.MaxEventAge, time.Hour)},
		stdlib.OpenDBFromPool(db.Pool()), func(ctx context.Context, m matrix.Message) { svc.HandleMessage(ctx, m) })
	if err != nil {
		return err
	}

	svc, err = service.New(service.Config{Steve: cfg.Steve, Rooms: cfg.Rooms, PollInterval: duration(cfg.PollInterval, 30*time.Second), RequireEncryption: true},
		db, sp, sp, bot, reg, sum)
	if err != nil {
		return err
	}
	var gate *approvalgate.Gate
	if cfg.Gate.Enabled {
		token, err := secret(envForgejoToken, true)
		if err != nil {
			return err
		}
		forge, err := approvalgate.NewForgejo(cfg.Gate.ForgejoOrigin, token)
		if err != nil {
			return err
		}
		gate, err = approvalgate.New(approvalgate.Config{Steve: cfg.Steve, Room: svc.ApprovalsRoom(), StatusContext: cfg.Gate.StatusContext,
			CodeTTL: duration(cfg.Gate.CodeTTL, 24*time.Hour), MaxMergesPerHour: cfg.Gate.MaxMergesPerHour, Repos: cfg.Gate.Repos}, db, forge, svc, svc.Feedback)
		if err != nil {
			return err
		}
		svc.SetGate(gate)
	}

	// Notify API.
	var callers []notify.CallerConfig
	if path := os.Getenv(envNotifyCallers); path != "" {
		if _, statErr := os.Stat(path); statErr == nil {
			if callers, err = notify.LoadCallers(path); err != nil {
				return err
			}
		} else {
			slog.Warn("no notify callers file; /v1/notify refuses every caller")
		}
	}
	nh, err := notify.New(svc, callers)
	if err != nil {
		return err
	}
	promReg := prometheus.NewRegistry()
	m := newMetrics(promReg)
	nh.OnRequest = func(caller, kind, outcome string) { m.notify.WithLabelValues(caller, kind, outcome).Inc() }
	svc.OnDecision = func(d approvalgate.Decision) { m.decisions.WithLabelValues(string(d.Kind), d.Outcome).Inc() }
	svc.OnTask = func(target, state string) { m.tasks.WithLabelValues(target, state).Inc() }

	var migrated atomic.Bool
	mux := http.NewServeMux()
	mux.Handle("POST /v1/notify", nh)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok\n")) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case !migrated.Load():
			http.Error(w, "database not migrated", http.StatusServiceUnavailable)
		case db.Ping(r.Context()) != nil:
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
		case !bot.Ready():
			http.Error(w, "matrix not synced", http.StatusServiceUnavailable)
		default:
			_, _ = w.Write([]byte("ready\n"))
		}
	})
	mux.Handle("GET /metrics", promhttp.HandlerFor(promReg, promhttp.HandlerOpts{}))
	listen := orDefault(os.Getenv(envListen), ":8080")
	srv := &http.Server{Addr: listen, Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("http server stopped", "error", err.Error())
			stop()
		}
	}()
	slog.Info("glovecell-voice starting", "version", version, "commit", commit, "listen", listen, "gate", cfg.Gate.Enabled, "targets", reg.Names())

	// The database must be migrated before anything is handled; retry
	// until it is, failing closed meanwhile.
	for {
		if err := db.Migrate(ctx); err == nil {
			break
		} else {
			slog.Info("voice database not ready; retrying", "error", err.Error())
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(10 * time.Second):
		}
	}
	migrated.Store(true)
	if gate != nil {
		if err := gate.Recover(ctx); err != nil {
			slog.Warn("approval recovery incomplete; retried on next start", "error", err.Error())
		}
	}
	go svc.Run(ctx)
	go func() {
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for {
			v := 0.0
			if bot.Ready() && db.Ping(ctx) == nil {
				v = 1
			}
			m.ready.Set(v)
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	err = bot.Run(ctx)
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdown)
	return err
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}
