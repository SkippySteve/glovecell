package controller

import (
	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	ao "git.cluster.aimingupward.com/steve/agent-project-controller/internal/agentorchestrator"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/herdr"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/sessionhub"
)

// usesHerdrCore reports whether this workspace's worker should run herdr-core
// (`hc server`) for hub `--via` / phone / PC control (docs/herdr.md).
func (r *Reconciler) usesHerdrCore(w *api.ProjectWorkspace) bool {
	if r == nil || !r.Config.HerdrCoreEnabled || w == nil {
		return false
	}
	class := r.Config.HerdrOrchestratorClass
	if class == "" {
		class = "herdr"
	}
	return w.Spec.OrchestratorClassName == class
}

// workerCommand selects the long-running workspace process. Herdr, Agent
// Orchestrator and Orca are mutually exclusive per workspace class.
func (r *Reconciler) workerCommand(w *api.ProjectWorkspace) []string {
	if r.usesHerdrCore(w) {
		return r.herdrSpokeCommand(r.usesSessionHub(w))
	}
	if r.usesAgentOrchestrator(w) {
		if r.usesSessionHub(w) {
			return ao.HubWorkerCommand()
		}
		return ao.WorkerCommand()
	}
	return r.orcaSpokeCommand(w)
}

// herdrSessionHubPanes starts session-hub-herdr beside herdr-core in a cell
// with a session hub: one herdr-core terminal per hub chat, and a template
// per hub agent (docs/session-hub.md, "Herdr"). It runs in the background as
// the workspace user and needs only the hub's loopback port and token.
const herdrSessionHubPanes = `
"` + sessionhub.Binary + `" session-hub-herdr &`

func (r *Reconciler) herdrSpokeCommand(sessionHub bool) []string {
	// hc server start daemonizes; keep the container alive like the default
	// sleep worker. State stays on the workspace PVC (ReadOnlyRootFilesystem).
	// herdr-via sets the same two directories (herdr.ConnectCommand).
	panes := ""
	if sessionHub {
		panes = herdrSessionHubPanes
	}
	return []string{"/bin/sh", "-c", `set -e
mkdir -p ` + herdr.WorkerRuntimeDir + ` ` + herdr.WorkerHome + `
export HOME=` + herdr.WorkerHome + `
export XDG_RUNTIME_DIR=` + herdr.WorkerRuntimeDir + `
if ! command -v hc >/dev/null 2>&1 && ! command -v herdr-core >/dev/null 2>&1; then
  echo "herdr-core missing from workspace image" >&2
  exit 1
fi
HC=hc
command -v hc >/dev/null 2>&1 || HC=herdr-core
"$HC" server start` + panes + `
trap 'exit 0' TERM INT
while :; do sleep 3600 & wait $!; done`}
}
