package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	ao "git.cluster.aimingupward.com/steve/agent-project-controller/internal/agentorchestrator"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/connect"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// AOPairing mints one-time Agent Orchestrator pairing codes for cells of the
// AO class, so the AO desktop and phone apps can add a cell without kubectl
// (docs/agent-orchestrator.md). Codes are bearer credentials for one device
// for ten minutes: never stored, logged or audited.
type AOPairing struct {
	Rest      *rest.Config
	Domain    string // AGENT_ORCHESTRATOR_DOMAIN; empty disables the endpoint
	ClassName string
	Container string
	// Exec runs a command in the worker container and returns its stdout.
	// Nil uses pods/exec with Rest.
	Exec func(ctx context.Context, pod *corev1.Pod, container string, command []string) ([]byte, error)
	Now  func() time.Time
}

func (s *Server) aoPairingEnabled() bool {
	return s.AOPairing != nil && strings.TrimSpace(s.AOPairing.Domain) != ""
}

func (p *AOPairing) exec(ctx context.Context, pod *corev1.Pod, command []string) ([]byte, error) {
	container := p.Container
	if container == "" {
		container = "workspace"
	}
	if p.Exec != nil {
		return p.Exec(ctx, pod, container, command)
	}
	var out bytes.Buffer
	err := connect.ExecInContainer(ctx, p.Rest, pod.Namespace, pod.Name, container, command, nil, &limitedWriter{w: &out, n: 64 << 10}, io.Discard, false)
	return out.Bytes(), err
}

type limitedWriter struct {
	w io.Writer
	n int
}

func (l *limitedWriter) Write(b []byte) (int, error) {
	if len(b) > l.n {
		return 0, io.ErrShortWrite
	}
	l.n -= len(b)
	return l.w.Write(b)
}

func (s *Server) mintAOPairing(w http.ResponseWriter, r *http.Request) {
	// Pairing codes are bearer credentials. Apply before every response path.
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	if !s.aoPairingEnabled() {
		http.Error(w, "agent-orchestrator pairing is not configured", http.StatusNotFound)
		return
	}
	if method, _ := r.Context().Value(authKey{}).(string); method != "oidc" {
		http.Error(w, "agent-orchestrator pairing requires an OIDC operator session", http.StatusForbidden)
		return
	}
	p := s.AOPairing
	var obj api.ProjectWorkspace
	if err := s.Client.Get(r.Context(), types.NamespacedName{Namespace: s.Namespace, Name: r.PathValue("name")}, &obj); err != nil {
		http.Error(w, "project not found", http.StatusNotFound)
		return
	}
	class := p.ClassName
	if class == "" {
		class = "ao"
	}
	if obj.Spec.OrchestratorClassName != class {
		http.Error(w, "project is not an agent-orchestrator cell", http.StatusConflict)
		return
	}
	address := ao.Address(obj.Name, p.Domain)
	var pods corev1.PodList
	ns := "apc-" + strings.ReplaceAll(string(obj.UID), "-", "")
	if err := s.Client.List(r.Context(), &pods, client.InNamespace(ns), client.MatchingLabels{api.OwnerLabel: string(obj.UID), "projects.agentwork.dev/role": "worker"}); err != nil {
		http.Error(w, "worker list failed", http.StatusServiceUnavailable)
		return
	}
	if len(pods.Items) != 1 {
		http.Error(w, "need exactly one Ready worker", http.StatusConflict)
		return
	}
	pod := pods.Items[0].DeepCopy()
	unavailable := func(reason string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]any{"available": false, "address": address, "reason": reason})
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	raw, err := p.exec(ctx, pod, ao.StatusCommand())
	if err != nil {
		unavailable("agent-orchestrator daemon not reachable in the worker yet")
		return
	}
	status, err := ao.ParseStatus(raw)
	if err != nil {
		unavailable(err.Error())
		return
	}
	now := time.Now
	if p.Now != nil {
		now = p.Now
	}
	raw, err = p.exec(ctx, pod, ao.PairingCodeCommand())
	if err != nil {
		unavailable("agent-orchestrator could not mint a pairing code")
		return
	}
	code, err := ao.ParsePairingCode(raw, now())
	if err != nil {
		unavailable(err.Error())
		return
	}
	offer, err := ao.NewOffer(obj.Name, p.Domain, status.HostID, code.Code)
	if err != nil {
		unavailable(err.Error())
		return
	}
	encoded, link, err := offer.Encode()
	if err != nil {
		unavailable("pairing offer could not be encoded")
		return
	}
	actor, _ := r.Context().Value(actorKey{}).(string)
	if err := s.accessAudit(r.Context(), &obj, "ao-pairing-minted", actor, "Agent Orchestrator pairing code minted by "+actor, map[string]string{"expiresAt": code.ExpiresAt.UTC().Format(time.RFC3339)}); err != nil {
		http.Error(w, "audit unavailable; no pairing code issued", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"available":   true,
		"address":     address,
		"hostId":      status.HostID,
		"code":        code.Code,
		"expiresAt":   code.ExpiresAt.UTC().Format(time.RFC3339),
		"pairingCode": encoded,
		"pairingUrl":  link,
		"desktop":     "Settings > Remote hosts > Add host: name " + obj.Name + ", address " + address + ", the code above.",
		"phone":       "Open pairingUrl on the phone, or Pair > Enter details manually with the address and code.",
	})
}
