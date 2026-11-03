// Command glovecell-voice is the voice service for Glovecell (decision
// D15): a Matrix bot with end-to-end encryption that turns Steve's voice
// notes into agent tasks and reads results back, the /v1/notify API agents
// use to reach him, and the voice approval gate that merges PRs on his
// verified spoken approval. See docs/voice.md and docs/approval-gate.md.
//
//	glovecell-voice serve        run the service (default; "glovecell-voice" too)
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
	case "serve", "glovecell-voice":
		err = serve()
	case "check-speech":
		err = checkSpeechFromEnv(os.Stdout)
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
		err = fmt.Errorf("unknown command %q (serve, check-speech, hash-token, version)", cmd)
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
	st, err := load()
	if err != nil {
		return err
	}
	cfg := st.Config

	// Database: verify-full TLS through the controller's store config.
	dbc, err := apcstore.ConfigFromFiles(st.DatabaseURLFile, st.DatabaseCAFile)
	if err != nil {
		return err
	}
	dbc.MaxConns = int32(st.DatabaseConns)
	db, err := store.Open(ctx, dbc)
	if err != nil {
		return err
	}
	defer db.Close()

	sp, err := speech.New(speech.Config{BaseURL: cfg.Speech.BaseURL, APIKey: st.SpeechKey, STTModel: cfg.Speech.STTModel, TTSModel: cfg.Speech.TTSModel,
		Voice: cfg.Speech.Voice, Language: cfg.Speech.Language, Speed: cfg.Speech.Speed})
	if err != nil {
		return err
	}

	reg := targets.NewRegistry()
	for name, t := range cfg.Targets {
		switch t.Kind {
		case "orca":
			if st.OrcaToken == "" {
				return fmt.Errorf("target %s: %s is required", name, envOrcaToken)
			}
			o, err := targets.NewOrca(targets.OrcaConfig{Name: name, HandoffURL: t.HandoffURL, RunRecordsURL: t.RunRecordsURL, Token: st.OrcaToken, Agent: t.Agent,
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
		if sum, err = service.NewChatSummarizer(cfg.Summarizer.BaseURL, cfg.Summarizer.Model, st.SummarizerKey); err != nil {
			return err
		}
	}

	var svc *service.Service
	var roomIDs []string
	for _, r := range cfg.Rooms {
		roomIDs = append(roomIDs, r.ID)
	}
	bot, err := matrix.New(matrix.Config{Homeserver: cfg.Matrix.Homeserver, UserID: cfg.Matrix.UserID, Password: st.Password, AccessToken: st.AccessToken,
		DeviceID: cfg.Matrix.DeviceID, DeviceName: orDefault(cfg.Matrix.DeviceName, "glovecell-voice"), PickleKey: []byte(st.PickleKey), Steve: st.Steve, Rooms: roomIDs,
		Trust: matrix.TrustPolicy{PinnedMasterKey: st.MasterKey}, MaxEventAge: duration(cfg.Matrix.MaxEventAge, time.Hour)},
		stdlib.OpenDBFromPool(db.Pool()), func(ctx context.Context, m matrix.Message) { svc.HandleMessage(ctx, m) })
	if err != nil {
		return err
	}
	svc, err = service.New(service.Config{Steve: st.Steve, Rooms: cfg.Rooms, PollInterval: duration(cfg.PollInterval, 30*time.Second), RequireEncryption: true},
		db, sp, sp, bot, reg, sum)
	if err != nil {
		return err
	}
	var gate *approvalgate.Gate
	if st.GateEnabled {
		forge, err := approvalgate.NewForgejo(cfg.Gate.ForgejoOrigin, st.ForgejoToken)
		if err != nil {
			return err
		}
		gate, err = approvalgate.New(approvalgate.Config{Steve: st.Steve, Room: svc.ApprovalsRoom(), StatusContext: cfg.Gate.StatusContext,
			CodeTTL: duration(cfg.Gate.CodeTTL, 24*time.Hour), MaxMergesPerHour: cfg.Gate.MaxMergesPerHour, Repos: cfg.Gate.Repos}, db, forge, svc, svc.Feedback)
		if err != nil {
			return err
		}
		svc.SetGate(gate)
	} else if svc.ApprovalsRoom() != "" {
		slog.Warn("approval gate off; the Approvals room is ignored", "variable", envApprovalEnabled)
	}

	// Notify callers: token files (one per caller) and/or a file of hashes.
	var callers []notify.CallerConfig
	if dir := os.Getenv(envNotifyDir); dir != "" {
		tmap, err := notify.ParseTargets(os.Getenv(envNotifyTargets))
		if err != nil {
			return err
		}
		if _, statErr := os.Stat(dir); statErr == nil {
			fromDir, err := notify.LoadTokenDir(dir, nil, tmap)
			if err != nil {
				return err
			}
			callers = append(callers, fromDir...)
		}
	}
	if path := os.Getenv(envNotifyCallers); path != "" {
		if _, statErr := os.Stat(path); statErr == nil {
			hashed, err := notify.LoadCallers(path)
			if err != nil {
				return err
			}
			callers = append(callers, hashed...)
		}
	}
	if len(callers) == 0 {
		slog.Warn("no notify callers; /v1/notify refuses every request")
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
	api := http.NewServeMux()
	api.Handle("POST /v1/notify", nh)
	health := api
	if st.Health != st.Listen {
		// The notify port carries only the API; probes and metrics have
		// their own port, outside the agents' network path.
		health = http.NewServeMux()
	}
	health.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok\n")) })
	health.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
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
	health.Handle("GET /metrics", promhttp.HandlerFor(promReg, promhttp.HandlerOpts{}))
	servers := []*http.Server{{Addr: st.Listen, Handler: api, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}}
	if st.Health != st.Listen {
		servers = append(servers, &http.Server{Addr: st.Health, Handler: health, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 30 * time.Second})
	}
	for _, srv := range servers {
		go func() {
			if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				slog.Error("http server stopped", "addr", srv.Addr, "error", err.Error())
				stop()
			}
		}()
	}
	slog.Info("glovecell-voice starting", "version", version, "commit", commit, "listen", st.Listen, "health", st.Health, "gate", st.GateEnabled, "targets", reg.Names(), "rooms", len(cfg.Rooms), "notifyCallers", len(callers))

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
	for _, srv := range servers {
		_ = srv.Shutdown(shutdown)
	}
	return err
}
