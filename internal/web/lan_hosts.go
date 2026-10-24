package web

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"time"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/controller"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/network"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// LANGrants enables per-project LAN host grants in the console. Nil means the
// operator configured no LAN range: grants can be removed but not added.
type LANGrants struct {
	Config network.LANConfig
	// Nodes lists cluster node addresses, which are always refused.
	Nodes func(context.Context, client.Client) ([]netip.Addr, error)
}

// authKey records how the request authenticated: "oidc", "bearer" or "basic".
type authKey struct{}

// lanOperator admits only a signed-in OIDC operator. The automation bearer and
// static login have operator authority elsewhere, but not over LAN reachability.
func lanOperator(w http.ResponseWriter, r *http.Request) (string, bool) {
	method, _ := r.Context().Value(authKey{}).(string)
	actor, _ := r.Context().Value(actorKey{}).(string)
	if method != "oidc" || actor == "" {
		http.Error(w, "LAN host grants need a signed-in OIDC operator; automation and static credentials cannot change them", http.StatusForbidden)
		return "", false
	}
	return actor, true
}

// lanProject loads a project for a grant change and applies the shared
// optimistic-concurrency and lifecycle checks.
func (s *Server) lanProject(w http.ResponseWriter, r *http.Request, version string) (*api.ProjectWorkspace, bool) {
	obj := &api.ProjectWorkspace{}
	if err := s.Client.Get(r.Context(), types.NamespacedName{Namespace: s.Namespace, Name: r.PathValue("name")}, obj); err != nil {
		http.Error(w, "project unavailable", http.StatusNotFound)
		return nil, false
	}
	if obj.ResourceVersion != version {
		http.Error(w, "project changed; refresh before retrying", http.StatusConflict)
		return nil, false
	}
	if !obj.DeletionTimestamp.IsZero() {
		http.Error(w, "project is being deleted", http.StatusConflict)
		return nil, false
	}
	return obj, true
}

func (s *Server) addLANHost(w http.ResponseWriter, r *http.Request) {
	actor, ok := lanOperator(w, r)
	if !ok {
		return
	}
	if s.LAN == nil || !s.LAN.Config.Enabled() {
		http.Error(w, "LAN host grants are disabled on this controller; set LAN_GRANT_ALLOWED_CIDRS", http.StatusForbidden)
		return
	}
	var request struct {
		ResourceVersion string  `json:"resourceVersion"`
		Address         string  `json:"address"`
		Ports           []int32 `json:"ports"`
		Note            string  `json:"note,omitempty"`
	}
	if decode(w, r, &request) != nil || request.ResourceVersion == "" {
		http.Error(w, "address, ports and resourceVersion required", http.StatusBadRequest)
		return
	}
	request.Address = strings.TrimSpace(request.Address)
	request.Note = strings.TrimSpace(request.Note)
	if err := network.CheckPorts(request.Ports); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := network.CheckNote(request.Note); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	nodes, err := s.LAN.Nodes(r.Context(), s.Client)
	if err != nil {
		http.Error(w, "cannot verify cluster node addresses; try again", http.StatusServiceUnavailable)
		return
	}
	if err := s.LAN.Config.CheckAddress(request.Address, nodes); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	obj, ok := s.lanProject(w, r, request.ResourceVersion)
	if !ok {
		return
	}
	if obj.Spec.DesiredState == "Removed" {
		http.Error(w, "removed projects cannot receive grants", http.StatusConflict)
		return
	}
	for _, h := range obj.Spec.LANHosts {
		if h.Address == request.Address {
			http.Error(w, request.Address+" is already granted; remove it first to change its ports", http.StatusConflict)
			return
		}
	}
	if len(obj.Spec.LANHosts) >= network.MaxLANHosts {
		http.Error(w, fmt.Sprintf("a project can hold at most %d LAN host grants", network.MaxLANHosts), http.StatusConflict)
		return
	}
	ports := slices.Clone(request.Ports)
	slices.Sort(ports)
	now := metav1.NewTime(time.Now().UTC().Truncate(time.Second))
	grant := api.LANHost{Address: request.Address, Ports: ports, Note: request.Note, AddedBy: actor, AddedAt: &now}
	obj.Spec.LANHosts = append(obj.Spec.LANHosts, grant)
	if obj.Annotations == nil {
		obj.Annotations = map[string]string{}
	}
	obj.Annotations["projects.agentwork.dev/requested-by"] = actor
	if err := s.Client.Update(r.Context(), obj); err != nil {
		http.Error(w, "grant could not be saved; refresh before retrying", http.StatusConflict)
		return
	}
	// A CRD without the lanHosts field prunes it silently. Say so instead of
	// reporting a grant that does not exist.
	if !slices.ContainsFunc(obj.Spec.LANHosts, func(h api.LANHost) bool { return h.Address == grant.Address }) {
		http.Error(w, "the installed ProjectWorkspace CRD does not store lanHosts; apply the updated CRD", http.StatusInternalServerError)
		return
	}
	if err := s.lanAudit(r.Context(), obj, "lan-host-added", grant, actor); err != nil {
		slog.ErrorContext(r.Context(), "LAN host grant saved without its audit record", "workspace_uid", string(obj.UID), "address", grant.Address, "error", err.Error())
		http.Error(w, "grant saved, but its audit record failed; check the controller log", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, obj)
}

func (s *Server) removeLANHost(w http.ResponseWriter, r *http.Request) {
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
	address := r.PathValue("address")
	// Removal works with the feature disabled, so grants can always be withdrawn.
	obj, ok := s.lanProject(w, r, request.ResourceVersion)
	if !ok {
		return
	}
	i := slices.IndexFunc(obj.Spec.LANHosts, func(h api.LANHost) bool { return h.Address == address })
	if i < 0 {
		http.Error(w, "no such LAN host grant", http.StatusNotFound)
		return
	}
	grant := obj.Spec.LANHosts[i]
	obj.Spec.LANHosts = slices.Delete(obj.Spec.LANHosts, i, i+1)
	if len(obj.Spec.LANHosts) == 0 {
		obj.Spec.LANHosts = nil
	}
	if obj.Annotations == nil {
		obj.Annotations = map[string]string{}
	}
	obj.Annotations["projects.agentwork.dev/requested-by"] = actor
	if err := s.Client.Update(r.Context(), obj); err != nil {
		http.Error(w, "grant could not be removed; refresh before retrying", http.StatusConflict)
		return
	}
	if err := s.lanAudit(r.Context(), obj, "lan-host-removed", grant, actor); err != nil {
		slog.ErrorContext(r.Context(), "LAN host grant removed without its audit record", "workspace_uid", string(obj.UID), "address", grant.Address, "error", err.Error())
		http.Error(w, "grant removed, but its audit record failed; check the controller log", http.StatusInternalServerError)
		return
	}
	writeJSON(w, obj)
}

// lanAudit writes an immutable record next to the controller's own audit
// ConfigMaps, so it appears in the project's access history.
func (s *Server) lanAudit(ctx context.Context, obj *api.ProjectWorkspace, action string, grant api.LANHost, actor string) error {
	summary := controller.LANSummary([]api.LANHost{grant})
	verb := map[string]string{"lan-host-added": "added", "lan-host-removed": "removed"}[action]
	now := time.Now().UTC().Format(time.RFC3339)
	slog.InfoContext(ctx, "LAN host grant "+verb, "workspace", obj.Name, "workspace_uid", string(obj.UID), "generation", obj.Generation, "actor", actor, "address", grant.Address, "ports", summary, "time", now)
	immutable := true
	name := fmt.Sprintf("audit-%s-%d-%s-%s", stringUID(obj), obj.Generation, action, strings.ReplaceAll(grant.Address, ".", "-"))
	ports := strings.TrimPrefix(summary, grant.Address+" tcp/")
	data := map[string]string{
		"workspace": obj.Name, "workspaceUID": string(obj.UID), "generation": fmt.Sprint(obj.Generation),
		"action": action, "actor": actor, "time": now, "address": grant.Address, "ports": ports,
		"message": fmt.Sprintf("LAN host %s TCP %s %s by %s", grant.Address, ports, verb, actor),
	}
	if grant.Note != "" {
		data["note"] = grant.Note
	}
	return s.Client.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: s.Namespace, Labels: map[string]string{api.OwnerLabel: string(obj.UID)}}, Immutable: &immutable, Data: data})
}
