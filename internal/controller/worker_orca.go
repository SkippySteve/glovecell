package controller

import (
	"fmt"
	"strings"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/sessionhub"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// usesOrcaSpoke reports whether this workspace's worker should run orcad for
// hub-and-spoke enrollment.
func (r *Reconciler) usesOrcaSpoke(w *api.ProjectWorkspace) bool {
	if r == nil || !r.Config.OrcaHubSpoke || r.Config.OrcaRuntimeImage == "" {
		return false
	}
	class := r.Config.OrcaOrchestratorClass
	if class == "" {
		class = "orca"
	}
	return w.Spec.OrchestratorClassName == class
}

func (r *Reconciler) orcaSpokeImage(w *api.ProjectWorkspace) string {
	if r.usesOrcaSpoke(w) {
		return r.Config.OrcaRuntimeImage
	}
	return r.Config.Image
}

// orcaSpokeDialAddress is the WebSocket URL baked into pairing offers and
// ORCA_PUBLIC_ADDRESS. Prefer the public wss://<project>.<spokeDomain> when
// configured so desktops outside the cluster can pair. Hub enrollment uses the
// same offer; the hub reaches it through the cluster Traefik path.
func (r *Reconciler) orcaSpokeDialAddress(w *api.ProjectWorkspace) string {
	if domain := strings.TrimSpace(r.Config.OrcaSpokePublicDomain); domain != "" && w != nil && w.Name != "" {
		return "wss://" + w.Name + "." + domain
	}
	return fmt.Sprintf("ws://orca-runtime.%s.svc.cluster.local:6768", namespace(w))
}

func (r *Reconciler) orcaSpokeCommand(w *api.ProjectWorkspace) []string {
	if !r.usesOrcaSpoke(w) {
		return []string{"/bin/sh", "-c", "trap 'exit 0' TERM INT; while :; do sleep 3600 & wait $!; done"}
	}
	addr := r.orcaSpokeDialAddress(w)
	// Only session-hub cells install Orca's hub slot, so every other cell's
	// command, and so its pod, stays exactly as before.
	slot := ""
	if r.usesOrcaHubSlot(w) {
		slot = orcaHubSlotInstall(orcaHubSlotHome, orcaHubSlotScript(sessionhub.Binary, orcaHubSlotAgents))
	}
	// orcad prints orca_server_ready on stdout; the hub scrapes those logs for
	// a runtime-scope pairing offer. Keep ORCA_USER_DATA on the workspace PVC.
	// orca-pilot lays Node under /opt/orca/runtimes/node-<sha>/bin/node.
	// Older layouts used /opt/orca/node/bin/node; keep both.
	return []string{"/bin/sh", "-c", fmt.Sprintf(
		`set -e
mkdir -p /workspace/orca /workspace/home
export ORCA_USER_DATA=/workspace/orca HOME=/workspace/home
%sNODE=
if [ -x /opt/orca/node/bin/node ]; then
  NODE=/opt/orca/node/bin/node
else
  for cand in /opt/orca/runtimes/node-*/bin/node; do
    if [ -x "$cand" ]; then NODE=$cand; break; fi
  done
fi
if [ -n "$NODE" ] && [ -f /opt/orca/server/orcad.js ]; then
  exec "$NODE" /opt/orca/server/orcad.js --port 6768 --bind 0.0.0.0 --pairing-address %q --json
fi
if command -v orcad >/dev/null 2>&1; then
  exec orcad --port 6768 --bind 0.0.0.0 --pairing-address %q --json
fi
echo "orca spoke image missing orcad" >&2
exit 1`, slot, addr, addr)}
}

// Orca's chat slot for the session hub (docs/session-hub.md, "Orca").
//
// Orca's native chat runs a fixed list of ACP agents and has no user-defined
// ones yet (stablyai/orca#38083). In a session-hub cell, Glovecell borrows one
// of them, OMP, whose CLI the workspace images don't ship: an executable named
// omp in ~/.local/bin, a folder Orca searches for agent CLIs, runs the hub
// connector instead. Orca then lists "OMP" as a chat agent, and its chats are
// hub sessions of the first agent in the cell's allowlist.
const (
	orcaHubSlotHome = "/workspace/home"
	// orcaHubSlotAgents is the cell's allowlist as the workspace container
	// sees it. The shim reads it at each chat start, so the worker's command
	// stays the same when the allowlist changes.
	orcaHubSlotAgents = "/run/session-hub-client/agents.json"
	// orcaHubSlotMarker is the shim's second line. Startup replaces only a file
	// that carries it, never an omp someone installed.
	orcaHubSlotMarker = "# glovecell-session-hub-slot"
	// orcaHubSlotVersion is what the shim answers to --version: Orca 1.4 runs
	// OMP chats only on OMP 17.0.5 or later.
	orcaHubSlotVersion = "17.0.5"
)

// usesOrcaHubSlot reports whether this cell gets Orca's hub slot: only an
// Orca spoke cell created with spec.sessionHub on an installation with
// sessionHub.enabled. Every other cell's command stays as it was.
func (r *Reconciler) usesOrcaHubSlot(w *api.ProjectWorkspace) bool {
	return r.usesOrcaSpoke(w) && r.usesSessionHub(w) && r.Config.SessionHubEnabled
}

// orcaHubSlotScript is the omp shim. Orca starts an OMP chat as `omp acp`,
// with `--plugin-dir <dir>` for its visuals skill, and probes `omp --version`
// first. It also lists OMP's models in a throwaway session kept out of OMP's
// store with `--session-dir`; the shim refuses that one, so a model listing
// never opens a hub session (Orca then shows no model list, and the hub agent
// uses its own default). Anything else, such as a terminal running `omp`,
// is refused with a note.
//
// The hub agent is the first entry of agents, the allowlist the controller
// writes with encoding/json, so it starts with {"id":"<id>", and ids match
// sessionhub's id pattern. Without a readable allowlist (outside a hub cell,
// or before the workspace container has it) --version fails too, so Orca
// offers no OMP chat.
func orcaHubSlotScript(binary, agents string) string {
	return `#!/bin/sh
` + orcaHubSlotMarker + `
# Written by Glovecell at start. Orca's OMP chat slot in this cell is the
# session hub connector for the first agent in ` + agents + `,
# not OMP (docs/session-hub.md).
agent=$(sed -n '1s/^\[{"id":"\([a-z][a-z0-9-]*\)",.*/\1/p' ` + agents + ` 2>/dev/null)
case "$agent" in
[a-z]*) [ ${#agent} -le 32 ] || agent= ;;
*) agent= ;;
esac
if [ -z "$agent" ]; then
  echo "omp: this cell has no session hub agent for Orca (` + agents + `)" >&2
  exit 2
fi
case "${1-}" in
--version) echo "` + orcaHubSlotVersion + ` (Glovecell session hub connector for $agent, not OMP)"; exit 0 ;;
acp) ;;
*) echo "omp: in this cell, omp is Orca's chat connection to the Glovecell session hub, not OMP; start a chat in Orca instead" >&2; exit 2 ;;
esac
for arg in "$@"; do
  if [ "$arg" = --session-dir ]; then
    echo "omp: the session hub lists no models; its agent uses its own default" >&2
    exit 2
  fi
done
exec ` + binary + ` session-hub-acp --agent "$agent"
`
}

// orcaHubSlotInstall is the startup step that writes the shim to
// <home>/.local/bin/omp, replacing only an earlier shim, through a temporary
// file so Orca never runs half a script. A failure is reported and Orca
// starts anyway, without the slot.
func orcaHubSlotInstall(home, script string) string {
	dir := home + "/.local/bin"
	return `if ! (
  set -e
  slot=` + dir + `/omp
  mkdir -p ` + dir + `
  if [ ! -e "$slot" ] || [ "$(sed -n 2p "$slot")" = '` + orcaHubSlotMarker + `' ]; then
    printf '%s' ` + shellSingleQuote(script) + ` >"$slot.tmp"
    chmod 0755 "$slot.tmp"
    mv -f "$slot.tmp" "$slot"
  else
    echo "glovecell: $slot is not Glovecell's; Orca's OMP slot is not connected to the session hub" >&2
  fi
); then
  echo "glovecell: could not write Orca's session hub slot; Orca starts without it" >&2
fi
`
}

// shellSingleQuote quotes s as one POSIX shell word.
func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func (r *Reconciler) orcaSpokeEnv(w *api.ProjectWorkspace, env []corev1.EnvVar) []corev1.EnvVar {
	if !r.usesOrcaSpoke(w) {
		return env
	}
	addr := r.orcaSpokeDialAddress(w)
	return append(env,
		corev1.EnvVar{Name: "ORCA_USER_DATA", Value: "/workspace/orca"},
		corev1.EnvVar{Name: "ORCA_PUBLIC_ADDRESS", Value: addr},
	)
}

func (r *Reconciler) orcaSpokeService(w *api.ProjectWorkspace) *corev1.Service {
	if !r.usesOrcaSpoke(w) {
		return nil
	}
	labels := labels(w)
	labels["projects.agentwork.dev/role"] = "worker"
	return &corev1.Service{
		ObjectMeta: metadata(w, "orca-runtime"),
		Spec: corev1.ServiceSpec{
			Selector: labels,
			Ports:    []corev1.ServicePort{{Name: "orca", Port: 6768, TargetPort: fromInt(6768)}},
		},
	}
}

// orcaSpokeRoute is the Gateway API HTTPRoute that publishes the spoke on
// <project>.<OrcaSpokePublicDomain>. Nil when the public domain is unset.
func (r *Reconciler) orcaSpokeRoute(w *api.ProjectWorkspace) *unstructured.Unstructured {
	if !r.usesOrcaSpoke(w) || strings.TrimSpace(r.Config.OrcaSpokePublicDomain) == "" {
		return nil
	}
	gwName := strings.TrimSpace(r.Config.OrcaSpokeGatewayName)
	gwNS := strings.TrimSpace(r.Config.OrcaSpokeGatewayNamespace)
	section := strings.TrimSpace(r.Config.OrcaSpokeGatewaySection)
	if gwName == "" || gwNS == "" || section == "" {
		return nil
	}
	host := w.Name + "." + strings.TrimSpace(r.Config.OrcaSpokePublicDomain)
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "gateway.networking.k8s.io/v1",
		"kind":       "HTTPRoute",
		"metadata": map[string]any{
			"name":      "orca-runtime",
			"namespace": namespace(w),
			"labels":    map[string]any{api.OwnerLabel: string(w.UID), "projects.agentwork.dev/role": "orca-runtime"},
		},
		"spec": map[string]any{
			"parentRefs": []any{map[string]any{
				"name":        gwName,
				"namespace":   gwNS,
				"sectionName": section,
			}},
			"hostnames": []any{host},
			"rules": []any{map[string]any{
				"backendRefs": []any{map[string]any{
					"name": "orca-runtime",
					"port": int64(6768),
				}},
			}},
		},
	}}
}

// orcaSpokeIngressPolicy allows Traefik (and the hub, defence in depth with the
// cluster-wide hubspoke policy) to reach orcad on the worker. The base worker
// CNP keeps ingress empty.
func (r *Reconciler) orcaSpokeIngressPolicy(w *api.ProjectWorkspace) *unstructured.Unstructured {
	if !r.usesOrcaSpoke(w) || strings.TrimSpace(r.Config.OrcaSpokePublicDomain) == "" {
		return nil
	}
	uid := string(w.UID)
	ns := namespace(w)
	hubNS := strings.TrimSpace(r.Config.OrcaHubNamespace)
	if hubNS == "" {
		hubNS = "agent-project-control-orca-hub"
	}
	ingress := []any{
		map[string]any{
			"fromEndpoints": []any{map[string]any{"matchLabels": map[string]any{
				"k8s:io.kubernetes.pod.namespace": "traefik-system",
				"app.kubernetes.io/name":          "traefik",
			}}},
			"toPorts": []any{map[string]any{"ports": []any{map[string]any{"port": "6768", "protocol": "TCP"}}}},
		},
		map[string]any{
			"fromEndpoints": []any{map[string]any{"matchLabels": map[string]any{
				"k8s:io.kubernetes.pod.namespace": hubNS,
				"app.kubernetes.io/name":          "orca-hub",
			}}},
			"toPorts": []any{map[string]any{"ports": []any{map[string]any{"port": "6768", "protocol": "TCP"}}}},
		},
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "cilium.io/v2",
		"kind":       "CiliumNetworkPolicy",
		"metadata": map[string]any{
			"name":      "orca-runtime",
			"namespace": ns,
			"labels":    map[string]any{api.OwnerLabel: uid, "projects.agentwork.dev/policy": "orca-runtime"},
		},
		"spec": map[string]any{
			"endpointSelector": map[string]any{"matchLabels": map[string]any{
				"projects.agentwork.dev/role": "worker",
				api.OwnerLabel:                uid,
			}},
			"ingress": ingress,
		},
	}}
}
