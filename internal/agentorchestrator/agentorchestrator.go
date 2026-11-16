// Package agentorchestrator runs Agent Orchestrator (AO,
// github.com/OrchestratorInc/agent-orchestrator) in a Glovecell worker and
// builds the pairing offers its desktop and phone apps accept
// (docs/agent-orchestrator.md).
//
// One AO daemon runs per cell. Its unauthenticated API stays on loopback; its
// authenticated remote listener (LAN mode, plain HTTP on RemotePort) is
// published only through a Gateway listener that terminates TLS, so devices
// see https://<workspace>.<domain>. Devices pair with a one-time code that the
// console fetches from the loopback API inside the worker; the code is never
// stored or logged. AO account sign-in, AO Cloud and AO's Cloudflare tunnel
// are not used.
package agentorchestrator

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	// Resources is where the workspace image keeps AO's release layout
	// (resources/daemon/ao, resources/tmux, resources/acp-runtime). AO finds
	// its bundled tmux relative to the daemon binary, so keep the layout.
	Resources = "/opt/ao/resources"
	// Binary is the AO daemon and CLI.
	Binary = Resources + "/daemon/ao"
	// DataDir keeps AO's database, sessions and device tokens on the
	// workspace volume (the root filesystem is read-only).
	DataDir = "/workspace/ao"
	// Home is the worker's HOME, as for every other worker process.
	Home = "/workspace/home"
	// LoopbackPort is AO's unauthenticated local API (AO_PORT). It never
	// leaves the pod: no Service, route or policy names it.
	LoopbackPort = 3001
	// RemotePort is AO's authenticated remote listener (mobilebridge
	// DefaultPort). The Service and HTTPRoute publish only this port.
	RemotePort = 3011
	// ServiceName names the per-cell Service, HTTPRoute and
	// CiliumNetworkPolicy.
	ServiceName = "agent-orchestrator"
)

// WorkerCommand starts the AO daemon, turns on its remote listener in LAN
// mode (no tunnel, no account) and requires per-device credentials, so the
// only way in is a one-time pairing code minted through the console. The
// pairing code that `remote-host enable` prints is discarded, never logged.
func WorkerCommand() []string {
	return []string{"/bin/sh", "-c", fmt.Sprintf(`set -e
mkdir -p %[1]s %[2]s
export HOME=%[2]s AO_DATA_DIR=%[1]s AO_PORT=%[3]d
AO=%[4]s
if [ ! -x "$AO" ]; then
  echo "agent-orchestrator missing from workspace image" >&2
  exit 1
fi
"$AO" daemon &
pid=$!
trap 'kill -TERM "$pid" 2>/dev/null; wait "$pid"; exit 0' TERM INT
i=0
until "$AO" status --json 2>/dev/null | grep -Eq '"state": *"ready"'; do
  i=$((i+1))
  if [ "$i" -ge 120 ] || ! kill -0 "$pid" 2>/dev/null; then
    echo "agent-orchestrator daemon did not become ready" >&2
    exit 1
  fi
  sleep 1
done
"$AO" remote-host enable >/dev/null
"$AO" remote-host require-device-credentials on >/dev/null
echo "agent-orchestrator remote listener ready on :%[5]d"
wait "$pid"`, DataDir, Home, LoopbackPort, Binary, RemotePort)}
}

// Env is added to the worker's environment so `kubectl exec` and agent
// shells reach the same daemon and data directory.
func Env() map[string]string {
	return map[string]string{
		"AO_DATA_DIR": DataDir,
		"AO_PORT":     fmt.Sprint(LoopbackPort),
	}
}

// Host is the device-facing host name of one cell: <workspace>.<domain>.
func Host(workspace, domain string) string {
	domain = strings.TrimSpace(domain)
	if workspace == "" || domain == "" {
		return ""
	}
	return workspace + "." + domain
}

// Address is the URL devices use (desktop "Add host"), always HTTPS on 443
// because the Gateway terminates TLS.
func Address(workspace, domain string) string {
	if h := Host(workspace, domain); h != "" {
		return "https://" + h
	}
	return ""
}

// StatusCommand and PairingCodeCommand run inside the worker and talk to the
// loopback API. --noproxy keeps the request off the project proxy.
func StatusCommand() []string {
	return []string{"curl", "--noproxy", "*", "--fail", "--silent", "--show-error", "--max-time", "5", fmt.Sprintf("http://127.0.0.1:%d/api/v1/mobile/status", LoopbackPort)}
}

func PairingCodeCommand() []string {
	return []string{"curl", "--noproxy", "*", "--fail", "--silent", "--show-error", "--max-time", "5", "-X", "POST", fmt.Sprintf("http://127.0.0.1:%d/api/v1/mobile/pairing-code", LoopbackPort)}
}

// Status is the part of GET /api/v1/mobile/status the console needs. The
// response also carries the pairing password; it is deliberately not decoded.
type Status struct {
	Enabled                  bool   `json:"enabled"`
	LoopbackOnly             bool   `json:"loopbackOnly"`
	HostID                   string `json:"hostId"`
	RequireDeviceCredentials bool   `json:"requireDeviceCredentials"`
}

// PairingCode is POST /api/v1/mobile/pairing-code: one device, ten minutes.
type PairingCode struct {
	Code      string    `json:"code"`
	ExpiresAt time.Time `json:"expiresAt"`
}

const maxResponse = 64 << 10

// ParseStatus checks that the remote listener is on, LAN-bound and refuses
// the pairing password.
func ParseStatus(raw []byte) (Status, error) {
	var s Status
	if len(raw) > maxResponse {
		return s, errors.New("agent-orchestrator status too large")
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return s, errors.New("agent-orchestrator status unreadable")
	}
	switch {
	case !s.Enabled:
		return s, errors.New("agent-orchestrator remote listener is off")
	case s.LoopbackOnly:
		return s, errors.New("agent-orchestrator remote listener is loopback-only")
	case !s.RequireDeviceCredentials:
		return s, errors.New("agent-orchestrator still accepts its pairing password")
	case s.HostID == "" || len(s.HostID) > 128 || strings.ContainsAny(s.HostID, " \t\r\n"):
		return s, errors.New("agent-orchestrator host id missing")
	}
	return s, nil
}

// ParsePairingCode checks a freshly minted code.
func ParsePairingCode(raw []byte, now time.Time) (PairingCode, error) {
	var p PairingCode
	if len(raw) > maxResponse {
		return p, errors.New("agent-orchestrator pairing code too large")
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return p, errors.New("agent-orchestrator pairing code unreadable")
	}
	if p.Code == "" || len(p.Code) > 256 || strings.ContainsAny(p.Code, " \t\r\n") {
		return p, errors.New("agent-orchestrator returned no pairing code")
	}
	if !p.ExpiresAt.After(now) || p.ExpiresAt.After(now.Add(15*time.Minute)) {
		return p, errors.New("agent-orchestrator pairing code has an unexpected expiry")
	}
	return p, nil
}

// Endpoint and Offer are AO's version 2 pairing offer, the payload of the
// phone's QR code and aomobile://pair links (packages/mobile/lib/pairingCode.ts).
type Endpoint struct {
	Kind   string `json:"kind"`
	Host   string `json:"host"`
	Port   int    `json:"port"`
	Secure bool   `json:"secure"`
}

type Offer struct {
	V         int        `json:"v"`
	HostID    string     `json:"hostId"`
	Name      string     `json:"name"`
	Platform  string     `json:"platform"`
	Endpoints []Endpoint `json:"endpoints"`
	Token     string     `json:"token"`
}

// NewOffer builds the offer for one cell. The single endpoint is the cell's
// HTTPS host; kind "lan" is how AO labels a direct listener.
func NewOffer(workspace, domain, hostID, code string) (Offer, error) {
	host := Host(workspace, domain)
	if host == "" || hostID == "" || code == "" {
		return Offer{}, errors.New("agent-orchestrator offer needs a host, host id and code")
	}
	return Offer{V: 2, HostID: hostID, Name: workspace, Platform: "linux", Endpoints: []Endpoint{{Kind: "lan", Host: host, Port: 443, Secure: true}}, Token: code}, nil
}

// Encode returns the offer as AO's unpadded base64url code, and as a link
// that opens the phone app (the payload stays in the fragment).
func (o Offer) Encode() (code, link string, err error) {
	raw, err := json.Marshal(o)
	if err != nil {
		return "", "", err
	}
	code = base64.RawURLEncoding.EncodeToString(raw)
	return code, "aomobile://pair#" + code, nil
}
