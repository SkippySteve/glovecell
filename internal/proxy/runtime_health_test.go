package proxy

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func healthFixture(t *testing.T) (*runtimeHealth, string, string, string) {
	t.Helper()
	policy, tls, state := t.TempDir(), t.TempDir(), t.TempDir()
	for path, raw := range map[string]string{filepath.Join(policy, "policy.json"): "{}", filepath.Join(tls, "ca.crt"): "cert", filepath.Join(tls, "ca.key"): "key"} {
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
	}
	h, err := newRuntimeHealth(policy, tls, state)
	if err != nil {
		t.Fatal(err)
	}
	h.startControl()
	return h, policy, tls, state
}

func TestRuntimeHealthPinnedInputsQuarantineUntilPodReplacement(t *testing.T) {
	for _, key := range []string{"policy.json", "ca.crt", "ca.key"} {
		t.Run(key, func(t *testing.T) {
			h, policy, tls, state := healthFixture(t)
			if !h.check() {
				t.Fatal("initial health")
			}
			dir := tls
			if key == "policy.json" {
				dir = policy
			}
			path := filepath.Join(dir, key)
			original, _ := os.ReadFile(path)
			if err := os.WriteFile(path, []byte("changed"), 0600); err != nil {
				t.Fatal(err)
			}
			if h.check() {
				t.Fatal("changed pinned input stayed ready")
			}
			_ = os.WriteFile(path, original, 0600)
			if h.check() {
				t.Fatal("repair cleared quarantine")
			}
			restarted, err := newRuntimeHealth(policy, tls, state)
			if err != nil {
				t.Fatal(err)
			}
			restarted.startControl()
			if restarted.check() {
				t.Fatal("process restart cleared quarantine")
			}
			replaced, err := newRuntimeHealth(policy, tls, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			replaced.startControl()
			if !replaced.check() {
				t.Fatal("pod replacement did not recover")
			}
		})
	}
}

func TestRuntimeHealthOptionalInputsNeverWidenOnLoss(t *testing.T) {
	for key, valid := range map[string]string{"provider-access": "{}", BrokeredProviderKey: "grok", GitHubPolicyKey: "", GitHubAccessKey: `{"enabled":false}`, "github-token": "test-token", "muse-subscription-key": "test-key"} {
		t.Run(key, func(t *testing.T) {
			h, _, tls, _ := healthFixture(t)
			if !h.check() {
				t.Fatal("absent optional inputs")
			}
			path := filepath.Join(tls, key)
			if err := os.WriteFile(path, []byte(valid), 0600); err != nil {
				t.Fatal(err)
			}
			if !h.check() {
				t.Fatal("valid input rejected", h.reason)
			}
			_ = os.Remove(path)
			if h.check() {
				t.Fatal("disappearance accepted")
			}
			_ = os.WriteFile(path, []byte(valid), 0600)
			if h.check() {
				t.Fatal("restoration cleared quarantine")
			}
		})
	}
}

func TestRuntimeHealthInvalidInputsDenyWithoutCredentialLeak(t *testing.T) {
	for _, key := range []string{"provider-access", BrokeredProviderKey, GitHubPolicyKey, GitHubAccessKey, "github-token", "muse-subscription-key"} {
		t.Run(key, func(t *testing.T) {
			h, _, tls, _ := healthFixture(t)
			raw := []byte("invalid-secret\x00")
			if err := os.WriteFile(filepath.Join(tls, key), raw, 0600); err != nil {
				t.Fatal(err)
			}
			if h.check() {
				t.Fatal("malformed credential accepted")
			}
			if h.reason != "credential-input-invalid" {
				t.Fatal("unsafe reason", h.reason)
			}
		})
	}
}

func TestRuntimeHealthControlFailureClosesTunnelsAndDeniesRequests(t *testing.T) {
	h, _, _, _ := healthFixture(t)
	left, right := net.Pipe()
	defer right.Close()
	if !h.track(left) {
		t.Fatal("track")
	}
	h.stopControl()
	_ = right.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := right.Read(make([]byte, 1)); err == nil {
		t.Fatal("tunnel open")
	}
	select {
	case <-h.ctx.Done():
	default:
		t.Fatal("forwarding context not canceled")
	}
	s := &Server{health: h}
	response := httptest.NewRecorder()
	s.ServeHTTP(response, httptest.NewRequest("CONNECT", "https://example.com", nil))
	if response.Code != 503 {
		t.Fatal(response.Code)
	}
	response = httptest.NewRecorder()
	s.ControlHandler().ServeHTTP(response, httptest.NewRequest("GET", "/ready", nil))
	if response.Code != 503 {
		t.Fatal(response.Code)
	}
	h.startControl()
	if h.check() {
		t.Fatal("listener restart cleared quarantine")
	}
}

func TestRuntimeReadinessNotReadyBeforeControlStarts(t *testing.T) {
	h, _, _, _ := healthFixture(t)
	h.control = false
	s := &Server{health: h}
	w := httptest.NewRecorder()
	s.readiness(w, httptest.NewRequest("GET", "/ready", nil))
	if w.Code != 503 {
		t.Fatal(w.Code)
	}
	h.startControl()
	w = httptest.NewRecorder()
	s.readiness(w, httptest.NewRequest("GET", "/ready", nil))
	if w.Code != http.StatusOK {
		t.Fatal(w.Code)
	}
}
