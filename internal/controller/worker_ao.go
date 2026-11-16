package controller

import (
	"encoding/hex"
	"errors"
	"strconv"
	"strings"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	ao "git.cluster.aimingupward.com/steve/agent-project-controller/internal/agentorchestrator"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/validation"
)

// usesAgentOrchestrator reports whether this workspace's worker runs the
// Agent Orchestrator daemon (docs/agent-orchestrator.md).
func (r *Reconciler) usesAgentOrchestrator(w *api.ProjectWorkspace) bool {
	if r == nil || !r.Config.AgentOrchestratorEnabled || r.Config.AgentOrchestratorImage == "" || w == nil {
		return false
	}
	class := r.Config.AgentOrchestratorClass
	if class == "" {
		class = "ao"
	}
	return w.Spec.OrchestratorClassName == class
}

// workerImage is the workspace container's image: the Orca spoke image, the
// AO image, or the shared workspace image.
func (r *Reconciler) workerImage(w *api.ProjectWorkspace) string {
	if r.usesAgentOrchestrator(w) {
		return r.Config.AgentOrchestratorImage
	}
	return r.orcaSpokeImage(w)
}

// usesWorkerRuntime reports whether the workspace container runs a
// class-specific command, env and ports that an image roll must carry over.
func (r *Reconciler) usesWorkerRuntime(w *api.ProjectWorkspace) bool {
	return r.usesOrcaSpoke(w) || r.usesAgentOrchestrator(w)
}

func (r *Reconciler) agentOrchestratorEnv(w *api.ProjectWorkspace, env []corev1.EnvVar) []corev1.EnvVar {
	if !r.usesAgentOrchestrator(w) {
		return env
	}
	vars := ao.Env()
	return append(env,
		corev1.EnvVar{Name: "AO_DATA_DIR", Value: vars["AO_DATA_DIR"]},
		corev1.EnvVar{Name: "AO_PORT", Value: vars["AO_PORT"]},
	)
}

func (r *Reconciler) agentOrchestratorService(w *api.ProjectWorkspace) *corev1.Service {
	if !r.usesAgentOrchestrator(w) {
		return nil
	}
	selector := labels(w)
	selector["projects.agentwork.dev/role"] = "worker"
	return &corev1.Service{
		ObjectMeta: metadata(w, ao.ServiceName),
		Spec: corev1.ServiceSpec{
			Selector: selector,
			Ports:    []corev1.ServicePort{{Name: "ao", Port: ao.RemotePort, TargetPort: fromInt(ao.RemotePort)}},
		},
	}
}

// agentOrchestratorPublished reports whether the cell gets a device-facing
// host: the class is in use and a domain and Gateway listener are set.
func (r *Reconciler) agentOrchestratorPublished(w *api.ProjectWorkspace) bool {
	c := r.Config
	return r.usesAgentOrchestrator(w) && strings.TrimSpace(c.AgentOrchestratorDomain) != "" &&
		strings.TrimSpace(c.AgentOrchestratorGatewayName) != "" && strings.TrimSpace(c.AgentOrchestratorGatewayNamespace) != "" && strings.TrimSpace(c.AgentOrchestratorGatewaySection) != ""
}

// agentOrchestratorRoute publishes the remote listener on
// <workspace>.<AgentOrchestratorDomain> through the configured Gateway
// listener, which terminates TLS. Use a listener reachable only from the
// LAN or VPN (docs/agent-orchestrator.md).
func (r *Reconciler) agentOrchestratorRoute(w *api.ProjectWorkspace) *unstructured.Unstructured {
	if !r.agentOrchestratorPublished(w) {
		return nil
	}
	c := r.Config
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "gateway.networking.k8s.io/v1",
		"kind":       "HTTPRoute",
		"metadata": map[string]any{
			"name":      ao.ServiceName,
			"namespace": namespace(w),
			"labels":    map[string]any{api.OwnerLabel: string(w.UID), "projects.agentwork.dev/role": ao.ServiceName},
		},
		"spec": map[string]any{
			"parentRefs": []any{map[string]any{
				"name":        strings.TrimSpace(c.AgentOrchestratorGatewayName),
				"namespace":   strings.TrimSpace(c.AgentOrchestratorGatewayNamespace),
				"sectionName": strings.TrimSpace(c.AgentOrchestratorGatewaySection),
			}},
			"hostnames": []any{ao.Host(w.Name, c.AgentOrchestratorDomain)},
			"rules": []any{map[string]any{
				"backendRefs": []any{map[string]any{
					"name": ao.ServiceName,
					"port": int64(ao.RemotePort),
				}},
			}},
		},
	}}
}

// agentOrchestratorIngressPolicy lets only Traefik reach the remote listener.
// The loopback API (ao.LoopbackPort) is never opened. The base worker policy
// keeps ingress empty.
func (r *Reconciler) agentOrchestratorIngressPolicy(w *api.ProjectWorkspace) *unstructured.Unstructured {
	if !r.agentOrchestratorPublished(w) {
		return nil
	}
	uid := string(w.UID)
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "cilium.io/v2",
		"kind":       "CiliumNetworkPolicy",
		"metadata": map[string]any{
			"name":      ao.ServiceName,
			"namespace": namespace(w),
			"labels":    map[string]any{api.OwnerLabel: uid, "projects.agentwork.dev/policy": ao.ServiceName},
		},
		"spec": map[string]any{
			"endpointSelector": map[string]any{"matchLabels": map[string]any{
				"projects.agentwork.dev/role": "worker",
				api.OwnerLabel:                uid,
			}},
			"ingress": []any{map[string]any{
				"fromEndpoints": []any{map[string]any{"matchLabels": map[string]any{
					"k8s:io.kubernetes.pod.namespace": "traefik-system",
					"app.kubernetes.io/name":          "traefik",
				}}},
				"toPorts": []any{map[string]any{"ports": []any{map[string]any{"port": strconv.Itoa(ao.RemotePort), "protocol": "TCP"}}}},
			}},
		},
	}}
}

// validateAgentOrchestrator refuses an enabled AO path without a
// digest-pinned image, and a domain without its Gateway listener.
func (c Config) validateAgentOrchestrator() error {
	if !c.AgentOrchestratorEnabled {
		return nil
	}
	digest := strings.Split(c.AgentOrchestratorImage, "@sha256:")
	if len(digest) != 2 || digest[0] == "" || len(digest[1]) != 64 {
		return errors.New("AGENT_ORCHESTRATOR_IMAGE requires a sha256 digest when AGENT_ORCHESTRATOR_ENABLED=true")
	}
	if _, e := hex.DecodeString(digest[1]); e != nil {
		return errors.New("invalid AGENT_ORCHESTRATOR_IMAGE digest")
	}
	if class := c.AgentOrchestratorClass; class != "" && len(validation.IsDNS1123Subdomain(class)) > 0 {
		return errors.New("AGENT_ORCHESTRATOR_CLASS must be a DNS subdomain name")
	}
	domain := strings.TrimSpace(c.AgentOrchestratorDomain)
	if domain == "" {
		return nil
	}
	if len(validation.IsDNS1123Subdomain(domain)) > 0 {
		return errors.New("AGENT_ORCHESTRATOR_DOMAIN must be a DNS suffix without scheme or port")
	}
	if strings.TrimSpace(c.AgentOrchestratorGatewayName) == "" || strings.TrimSpace(c.AgentOrchestratorGatewayNamespace) == "" || strings.TrimSpace(c.AgentOrchestratorGatewaySection) == "" {
		return errors.New("AGENT_ORCHESTRATOR_GATEWAY_NAME/NAMESPACE/SECTION required when AGENT_ORCHESTRATOR_DOMAIN is set")
	}
	return nil
}
