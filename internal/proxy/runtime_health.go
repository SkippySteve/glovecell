package proxy

import (
	"context"
	"crypto/sha256"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/credentials"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/githubpolicy"
)

// runtimeHealth is opt-in until disposable-cell qualification. Its marker must
// live on a pod-scoped volume, not a container filesystem or persistent PVC.
// Only replacement of the pod may clear a quarantine.
type runtimeHealth struct {
	mu           sync.Mutex
	dir, marker  string
	pinned       map[string][32]byte
	seen         map[string]bool
	activePolicy bool
	reason       string
	control      bool
	connections  map[net.Conn]bool
	ctx          context.Context
	cancel       context.CancelFunc
}

func newRuntimeHealth(policyDir, tlsDir, stateDir string) (*runtimeHealth, error) {
	if stateDir == "" {
		return nil, errors.New("runtime health requires pod-scoped state directory")
	}
	// Prove the marker directory is writable before serving any traffic.
	f, err := os.CreateTemp(stateDir, ".health-")
	if err != nil {
		return nil, errors.New("runtime health state unavailable")
	}
	name := f.Name()
	err = f.Close()
	_ = os.Remove(name)
	if err != nil {
		return nil, errors.New("runtime health state unavailable")
	}
	h := &runtimeHealth{dir: tlsDir, marker: filepath.Join(stateDir, "quarantine"), pinned: map[string][32]byte{}, seen: map[string]bool{}, connections: map[net.Conn]bool{}}
	h.ctx, h.cancel = context.WithCancel(context.Background())
	// Arm the marker before forwarding starts. Even a disk failure while
	// recording quarantine cannot make a container restart look like recovery.
	marker, err := os.OpenFile(h.marker, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if errors.Is(err, os.ErrExist) {
		h.reason = "previous-process-requires-replacement"
	} else if err != nil {
		return nil, errors.New("runtime health state unavailable")
	} else {
		if _, err = marker.WriteString("armed\n"); err == nil {
			err = marker.Sync()
		}
		closeErr := marker.Close()
		if err != nil || closeErr != nil {
			return nil, errors.New("runtime health state unavailable")
		}
	}

	for _, path := range []string{filepath.Join(policyDir, "policy.json"), filepath.Join(tlsDir, "ca.crt"), filepath.Join(tlsDir, "ca.key")} {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, errors.New("runtime health input unavailable")
		}
		h.pinned[path] = sha256.Sum256(raw)
	}
	return h, nil
}

func (h *runtimeHealth) quarantineLocked(reason string) {
	if h.reason != "" {
		return
	}
	h.reason = reason
	h.cancel()
	// A failed marker write still denies in memory. A later process must prove
	// state is writable at startup; activation qualification must test storage.
	if err := os.WriteFile(h.marker, []byte(reason+"\n"), 0600); err != nil {
		h.reason = "quarantine-state-write-failed"
	}
	for c := range h.connections {
		_ = c.Close()
	}
	slog.Error("proxy quarantined", "reason_code", h.reason)
}

func (h *runtimeHealth) stopControl() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.control = false
	h.quarantineLocked("control-listener-stopped")
}
func (h *runtimeHealth) startControl() { h.mu.Lock(); defer h.mu.Unlock(); h.control = true }
func (h *runtimeHealth) track(c net.Conn) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.reason != "" {
		_ = c.Close()
		return false
	}
	h.connections[c] = true
	return true
}
func (h *runtimeHealth) untrack(c net.Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.connections, c)
}

// check rereads projected files. Optional inputs may be absent before first
// activation; once observed, disappearance is uncertainty, not disabled auth.
func (h *runtimeHealth) check() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.reason != "" {
		return false
	}
	if !h.control {
		return false
	}
	marker, err := os.ReadFile(h.marker)
	if err != nil || string(marker) != "armed\n" {
		h.quarantineLocked("runtime-state-invalid")
		return false
	}
	for path, digest := range h.pinned {
		raw, err := os.ReadFile(path)
		if err != nil || sha256.Sum256(raw) != digest {
			h.quarantineLocked("pinned-input-changed")
			return false
		}
	}
	for _, key := range []string{"provider-access", BrokeredProviderKey, GitHubPolicyKey, GitHubAccessKey, "github-token", "muse-subscription-key"} {
		raw, err := os.ReadFile(filepath.Join(h.dir, key))
		if errors.Is(err, os.ErrNotExist) && !h.seen[key] {
			continue
		}
		if err != nil || len(raw) > 128<<10 {
			h.quarantineLocked("credential-input-unavailable")
			return false
		}
		h.seen[key] = true
		switch key {
		case "provider-access":
			s := &Server{ProviderGrants: func() ([]byte, error) { return raw, nil }}
			_, err = s.currentGrants()
		case BrokeredProviderKey:
			_, err = ParseBrokeredProviders(raw)
		case GitHubPolicyKey:
			var policy *githubpolicy.Policy
			policy, err = githubpolicy.Parse(raw)
			if err == nil && policy != nil {
				h.activePolicy = true
			}
			if err == nil && policy == nil && h.activePolicy {
				err = errors.New("active GitHub policy disappeared")
			}
		case GitHubAccessKey:
			_, err = ParseGitHubLease(raw)
		default:
			err = credentials.Usable(raw)
		}
		if err != nil {
			h.quarantineLocked("credential-input-invalid")
			return false
		}
	}
	return true
}

func (s *Server) runtimeReady() bool {
	if s.health == nil {
		return true
	}
	if s.CA != nil && (time.Now().Before(s.CA.NotBefore) || !time.Now().Before(s.CA.NotAfter)) {
		s.health.mu.Lock()
		s.health.quarantineLocked("inspection-ca-expired")
		s.health.mu.Unlock()
	}
	return s.health.check()
}
func (s *Server) readiness(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !s.runtimeReady() {
		http.Error(w, "proxy recovery required", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// ReadyCLI probes the loopback health endpoint without exposing credentials.
func ReadyCLI(ctx context.Context) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+ControlAddress+"/ready", nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}}
	response, err := client.Do(request)
	if err != nil {
		return errors.New("proxy readiness unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return errors.New("proxy recovery required")
	}
	return nil
}

// Quarantine messages use a closed reason-code vocabulary, never file contents.
func (h *runtimeHealth) reasonCode() string { h.mu.Lock(); defer h.mu.Unlock(); return h.reason }
func (h *runtimeHealth) quarantineError() error {
	reason := h.reasonCode()
	if reason == "" {
		reason = "runtime-health-unavailable"
	}
	_ = os.WriteFile("/dev/termination-log", []byte("proxy-quarantine:"+reason+"\n"), 0600)
	return errors.New("proxy recovery required: " + reason)
}

// Inputs that are part of the loaded route configuration must be readable on
// first readiness, not just after the first successful read.
func (h *runtimeHealth) requireConfiguredInputs(c Config) {
	if c.BrokeredProviders {
		h.seen["provider-access"] = true
	}
	if c.MuseSubscriptionGateway {
		h.seen["muse-subscription-key"] = true
	}
	if len(c.PrivateGitHubRepositories) > 0 {
		h.seen["github-token"] = true
	}
}
