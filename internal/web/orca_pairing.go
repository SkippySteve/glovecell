package web

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/hubspoke"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// OrcaPairing mints one-time runtime pairing offers for project spokes so a
// desktop can `orca environment add` without kubectl. Offers are never stored.
type OrcaPairing struct {
	Rest      *rest.Config
	Domain    string // HUBSPOKE_SPOKE_DOMAIN; empty disables the endpoint
	HubSpoke  bool
	ClassName string
	Container string
	OfferFrom func(ctx context.Context, w *api.ProjectWorkspace, pod *corev1.Pod) (hubspoke.Offer, error)
}

func (s *Server) orcaPairingEnabled() bool {
	return s.OrcaPairing != nil && s.OrcaPairing.HubSpoke && strings.TrimSpace(s.OrcaPairing.Domain) != ""
}

func (s *Server) orcaSpokeEndpoint(name string) string {
	if !s.orcaPairingEnabled() || name == "" {
		return ""
	}
	return "wss://" + name + "." + strings.TrimSpace(s.OrcaPairing.Domain)
}

func (s *Server) mintOrcaPairing(w http.ResponseWriter, r *http.Request) {
	if !s.orcaPairingEnabled() {
		http.Error(w, "spoke desktop pairing is not configured", http.StatusNotFound)
		return
	}
	if method, _ := r.Context().Value(authKey{}).(string); method != "oidc" {
		http.Error(w, "spoke pairing requires an OIDC operator session", http.StatusForbidden)
		return
	}
	name := r.PathValue("name")
	var obj api.ProjectWorkspace
	if err := s.Client.Get(r.Context(), types.NamespacedName{Namespace: s.Namespace, Name: name}, &obj); err != nil {
		http.Error(w, "project not found", http.StatusNotFound)
		return
	}
	class := s.OrcaPairing.ClassName
	if class == "" {
		class = "orca"
	}
	if obj.Spec.OrchestratorClassName != class {
		http.Error(w, "project is not an orca hubspoke cell", http.StatusConflict)
		return
	}
	endpoint := s.orcaSpokeEndpoint(obj.Name)
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
	offerFrom := s.OrcaPairing.OfferFrom
	if offerFrom == nil {
		offerFrom = hubspoke.OfferFromLogs(s.OrcaPairing.Rest, hubspoke.OrcaRuntime{}, s.OrcaPairing.Container)
	}
	offer, err := offerFrom(r.Context(), &obj, pods.Items[0].DeepCopy())
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"available": false,
			"endpoint":  endpoint,
			"reason":    "pairing offer not in worker logs; Pause then Resume (or wait for a spoke restart) to mint a new runtime offer",
		})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"available":  true,
		"endpoint":   endpoint,
		"pairingUrl": offer.URL,
		"deviceId":   offer.DeviceID,
		"command":    "orca environment add --name " + obj.Name + " --pairing-code '<pairingUrl>'",
		"scope":      offer.Scope,
	})
}
