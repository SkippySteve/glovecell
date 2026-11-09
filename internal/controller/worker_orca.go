package controller

import (
	"fmt"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
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

func (r *Reconciler) orcaSpokeCommand(w *api.ProjectWorkspace) []string {
	if !r.usesOrcaSpoke(w) {
		return []string{"/bin/sh", "-c", "trap 'exit 0' TERM INT; while :; do sleep 3600 & wait $!; done"}
	}
	addr := fmt.Sprintf("ws://orca-runtime.%s.svc.cluster.local:6768", namespace(w))
	// orcad prints orca_server_ready on stdout; the hub scrapes those logs for
	// a runtime-scope pairing offer. Keep ORCA_USER_DATA on the workspace PVC.
	return []string{"/bin/sh", "-c", fmt.Sprintf(
		`set -e
mkdir -p /workspace/orca /workspace/home
export ORCA_USER_DATA=/workspace/orca HOME=/workspace/home
if [ -x /opt/orca/node/bin/node ] && [ -f /opt/orca/server/orcad.js ]; then
  exec /opt/orca/node/bin/node /opt/orca/server/orcad.js --port 6768 --bind 0.0.0.0 --pairing-address %q --json
fi
if command -v orcad >/dev/null 2>&1; then
  exec orcad --port 6768 --bind 0.0.0.0 --pairing-address %q --json
fi
echo "orca spoke image missing orcad" >&2
exit 1`, addr, addr)}
}

func (r *Reconciler) orcaSpokeEnv(w *api.ProjectWorkspace, env []corev1.EnvVar) []corev1.EnvVar {
	if !r.usesOrcaSpoke(w) {
		return env
	}
	addr := fmt.Sprintf("ws://orca-runtime.%s.svc.cluster.local:6768", namespace(w))
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
