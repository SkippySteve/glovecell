package controller

import (
	"fmt"
	"strings"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
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
	// orcad prints orca_server_ready on stdout; the hub scrapes those logs for
	// a runtime-scope pairing offer. Keep ORCA_USER_DATA on the workspace PVC.
	// orca-pilot lays Node under /opt/orca/runtimes/node-<sha>/bin/node.
	// Older layouts used /opt/orca/node/bin/node; keep both.
	return []string{"/bin/sh", "-c", fmt.Sprintf(
		`set -e
mkdir -p /workspace/orca /workspace/home
export ORCA_USER_DATA=/workspace/orca HOME=/workspace/home
NODE=
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
exit 1`, addr, addr)}
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
