package web

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"time"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/proxy"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Providers enables per-project brokered provider opt-in (spec.providers) in
// the console. Nil means the controller has no lease source: existing opt-ins
// can still be removed, but none can be added.
type Providers struct {
	// Status reports which providers currently have a usable lease. It never
	// returns token material.
	Status func(context.Context) (map[string]bool, error)
}

// providerLeases serves the lease status shown next to each provider.
func (s *Server) providerLeases(w http.ResponseWriter, r *http.Request) {
	if s.Providers == nil {
		writeJSON(w, map[string]any{"configured": false})
		return
	}
	status, err := s.Providers.Status(r.Context())
	out := map[string]any{"configured": true, "leases": status}
	if err != nil {
		out["error"] = "provider leases unavailable"
	}
	writeJSON(w, out)
}

// addProvider opts a project into one brokered provider. Like LAN host grants
// it needs a signed-in OIDC operator and takes effect while the project runs.
func (s *Server) addProvider(w http.ResponseWriter, r *http.Request) {
	actor, ok := lanOperator(w, r)
	if !ok {
		return
	}
	if s.Providers == nil {
		http.Error(w, "brokered providers are off on this controller; set PROVIDER_LEASES_SECRET", http.StatusForbidden)
		return
	}
	var request struct {
		ResourceVersion string `json:"resourceVersion"`
		Name            string `json:"name"`
	}
	if decode(w, r, &request) != nil || request.ResourceVersion == "" {
		http.Error(w, "name and resourceVersion required", http.StatusBadRequest)
		return
	}
	if _, known := proxy.BrokeredProviders[request.Name]; !known {
		http.Error(w, "unsupported provider", http.StatusBadRequest)
		return
	}
	obj, ok := s.lanProject(w, r, request.ResourceVersion)
	if !ok {
		return
	}
	if obj.Spec.DesiredState == api.DesiredRemoved {
		http.Error(w, "removed projects cannot receive provider access", http.StatusConflict)
		return
	}
	if slices.ContainsFunc(obj.Spec.Providers, func(p api.ProviderGrant) bool { return p.Name == request.Name }) {
		http.Error(w, request.Name+" is already enabled for this project", http.StatusConflict)
		return
	}
	now := metav1.NewTime(time.Now().UTC().Truncate(time.Second))
	grant := api.ProviderGrant{Name: request.Name, AddedBy: actor, AddedAt: &now}
	obj.Spec.Providers = append(obj.Spec.Providers, grant)
	if obj.Annotations == nil {
		obj.Annotations = map[string]string{}
	}
	obj.Annotations["projects.agentwork.dev/requested-by"] = actor
	if err := s.Client.Update(r.Context(), obj); err != nil {
		slog.WarnContext(r.Context(), "provider access not saved", "workspace_uid", string(obj.UID), "provider", grant.Name, "error", err.Error())
		http.Error(w, "provider access could not be saved; refresh before retrying", http.StatusConflict)
		return
	}
	// A CRD without the providers field prunes it silently. Say so instead of
	// reporting access that does not exist.
	if !slices.ContainsFunc(obj.Spec.Providers, func(p api.ProviderGrant) bool { return p.Name == grant.Name }) {
		http.Error(w, "the installed ProjectWorkspace CRD does not store providers; apply the updated CRD", http.StatusInternalServerError)
		return
	}
	if err := s.providerAudit(r.Context(), obj, "provider-enabled", grant.Name, actor); err != nil {
		slog.ErrorContext(r.Context(), "provider access saved without its audit record", "workspace_uid", string(obj.UID), "provider", grant.Name, "error", err.Error())
		http.Error(w, "provider access saved, but its audit record failed; check the controller log", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, obj)
}

// removeProvider revokes one provider for one project. It works with the
// feature disabled, so access can always be withdrawn.
func (s *Server) removeProvider(w http.ResponseWriter, r *http.Request) {
	actor, ok := lanOperator(w, r)
	if !ok {
		return
	}
	var request struct {
		ResourceVersion string `json:"resourceVersion"`
	}
	if decode(w, r, &request) != nil || request.ResourceVersion == "" {
		http.Error(w, "resourceVersion required", http.StatusBadRequest)
		return
	}
	name := r.PathValue("provider")
	obj, ok := s.lanProject(w, r, request.ResourceVersion)
	if !ok {
		return
	}
	i := slices.IndexFunc(obj.Spec.Providers, func(p api.ProviderGrant) bool { return p.Name == name })
	if i < 0 {
		http.Error(w, "provider not enabled for this project", http.StatusNotFound)
		return
	}
	obj.Spec.Providers = slices.Delete(obj.Spec.Providers, i, i+1)
	if len(obj.Spec.Providers) == 0 {
		obj.Spec.Providers = nil
	}
	if obj.Annotations == nil {
		obj.Annotations = map[string]string{}
	}
	obj.Annotations["projects.agentwork.dev/requested-by"] = actor
	if err := s.Client.Update(r.Context(), obj); err != nil {
		slog.WarnContext(r.Context(), "provider access not revoked", "workspace_uid", string(obj.UID), "provider", name, "error", err.Error())
		http.Error(w, "provider access could not be revoked; refresh before retrying", http.StatusConflict)
		return
	}
	if err := s.providerAudit(r.Context(), obj, "provider-revoked", name, actor); err != nil {
		slog.ErrorContext(r.Context(), "provider access revoked without its audit record", "workspace_uid", string(obj.UID), "provider", name, "error", err.Error())
		http.Error(w, "provider access revoked, but its audit record failed; check the controller log", http.StatusInternalServerError)
		return
	}
	writeJSON(w, obj)
}

// providerAudit writes an immutable record next to the controller's own audit
// ConfigMaps, so it appears in the project's access history.
func (s *Server) providerAudit(ctx context.Context, obj *api.ProjectWorkspace, action, provider, actor string) error {
	verb := map[string]string{"provider-enabled": "enabled", "provider-revoked": "revoked"}[action]
	now := time.Now().UTC().Format(time.RFC3339)
	slog.InfoContext(ctx, "brokered provider "+verb, "workspace", obj.Name, "workspace_uid", string(obj.UID), "generation", obj.Generation, "actor", actor, "provider", provider, "time", now)
	name := fmt.Sprintf("audit-%s-%d-%s-%s", stringUID(obj), obj.Generation, action, provider)
	data := map[string]string{
		"workspace": obj.Name, "workspaceUID": string(obj.UID), "generation": fmt.Sprint(obj.Generation),
		"action": action, "actor": actor, "time": now, "provider": provider,
		"message": fmt.Sprintf("%s access %s by %s", provider, verb, actor),
	}
	return s.auditLog().Record(ctx, string(obj.UID), name, data, "console")
}
