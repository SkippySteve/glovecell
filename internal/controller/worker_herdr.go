package controller

import (
	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
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

// workerCommand selects the long-running workspace process. Herdr and Orca are
// mutually exclusive per workspace class.
func (r *Reconciler) workerCommand(w *api.ProjectWorkspace) []string {
	if r.usesHerdrCore(w) {
		return r.herdrSpokeCommand()
	}
	return r.orcaSpokeCommand(w)
}

func (r *Reconciler) herdrSpokeCommand() []string {
	// hc server start daemonizes; keep the container alive like the default
	// sleep worker. State stays on the workspace PVC (ReadOnlyRootFilesystem).
	return []string{"/bin/sh", "-c", `set -e
mkdir -p /workspace/herdr /workspace/home
export HOME=/workspace/home
export XDG_RUNTIME_DIR=/workspace/herdr
if ! command -v hc >/dev/null 2>&1 && ! command -v herdr-core >/dev/null 2>&1; then
  echo "herdr-core missing from workspace image" >&2
  exit 1
fi
HC=hc
command -v hc >/dev/null 2>&1 || HC=herdr-core
"$HC" server start
trap 'exit 0' TERM INT
while :; do sleep 3600 & wait $!; done`}
}
