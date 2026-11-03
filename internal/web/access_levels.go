package web

// Access levels (docs/access-levels.md): the console reads AccessProfiles,
// sets a cell's spec.accessProfileName, and writes AccessGrants. The
// controller enforces both (internal/controller/access_profiles.go).
//
// Project cells support the locked, standard and custom levels and LAN
// grants. Ask needs a request log and Open needs time-limited internet windows
// that the project proxy does not have yet; those requests are refused here,
// and the controller refuses such grants too, so nothing is ever opened that
// is not enforced.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/controller"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/network"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// projectLevels are the levels the project proxy can enforce.
var projectLevels = []api.AccessLevel{api.AccessLevelLocked, api.AccessLevelStandard, api.AccessLevelCustom}

// unsupportedLevel explains why a level is refused for project cells.
func unsupportedLevel(level api.AccessLevel) string {
	switch level {
	case api.AccessLevelAsk:
		return "project sandboxes do not record blocked requests yet, so there is nothing to approve one by one"
	case api.AccessLevelOpen:
		return "the project proxy cannot open time-limited internet windows yet"
	}
	return "unknown access level"
}

// profileView is one AccessProfile as the console shows it.
type profileView struct {
	Name             string             `json:"name"`
	Level            api.AccessLevel    `json:"level"`
	Description      string             `json:"description,omitempty"`
	Rules            []api.HTTPSRule    `json:"rules"`
	AllowedGrants    []string           `json:"allowedGrants"`
	MaxGrantMinutes  map[string]float64 `json:"maxGrantMinutes,omitempty"`
	ApproveRequests  bool               `json:"approveRequests,omitempty"`
	Default          bool               `json:"default,omitempty"`
	ProjectSupported bool               `json:"projectSupported"`
	Reason           string             `json:"reason,omitempty"`
}

// listProfiles returns nil, nil when the AccessProfile CRD is not installed.
func (s *Server) listProfiles(ctx context.Context) ([]api.AccessProfile, error) {
	var list api.AccessProfileList
	if err := s.Client.List(ctx, &list); err != nil {
		if meta.IsNoMatchError(err) || apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	sort.Slice(list.Items, func(i, j int) bool { return list.Items[i].Name < list.Items[j].Name })
	return list.Items, nil
}

func (s *Server) accessProfiles(w http.ResponseWriter, r *http.Request) {
	profiles, err := s.listProfiles(r.Context())
	if err != nil {
		http.Error(w, "access profiles unavailable", http.StatusServiceUnavailable)
		return
	}
	out := []profileView{}
	for i := range profiles {
		p := &profiles[i]
		rules, err := controller.ProfileRules(p)
		if err != nil {
			continue // the controller refuses it too
		}
		v := profileView{Name: p.Name, Level: p.Spec.Level, Description: p.Spec.Description, Rules: rules, AllowedGrants: []string{}, ApproveRequests: p.Spec.ApproveRequests,
			Default: p.Annotations[api.DefaultAccessProfileAnnotation] == "true", ProjectSupported: slices.Contains(projectLevels, p.Spec.Level)}
		if !v.ProjectSupported {
			v.Reason = unsupportedLevel(p.Spec.Level)
		}
		for _, t := range p.Spec.AllowedGrants {
			v.AllowedGrants = append(v.AllowedGrants, string(t))
		}
		if len(p.Spec.MaxGrantDuration) > 0 {
			v.MaxGrantMinutes = map[string]float64{}
			for t, d := range p.Spec.MaxGrantDuration {
				v.MaxGrantMinutes[string(t)] = d.Minutes()
			}
		}
		out = append(out, v)
	}
	writeJSON(w, out)
}

// defaultProfile names the cluster default profile, or "" when none is marked.
func (s *Server) defaultProfile(ctx context.Context) (string, error) {
	profiles, err := s.listProfiles(ctx)
	if err != nil {
		return "", err
	}
	for _, p := range profiles {
		if p.Annotations[api.DefaultAccessProfileAnnotation] == "true" && slices.Contains(projectLevels, p.Spec.Level) {
			return p.Name, nil
		}
	}
	return "", nil
}

// projectProfile loads a profile a project cell may use.
func (s *Server) projectProfile(ctx context.Context, name string) (*api.AccessProfile, int, error) {
	p := &api.AccessProfile{}
	if err := s.Client.Get(ctx, types.NamespacedName{Name: name}, p); err != nil {
		if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
			return nil, http.StatusBadRequest, fmt.Errorf("access profile %s does not exist", name)
		}
		return nil, http.StatusServiceUnavailable, errors.New("access profiles unavailable")
	}
	if !slices.Contains(projectLevels, p.Spec.Level) {
		return nil, http.StatusBadRequest, fmt.Errorf("%s: %s", p.Name, unsupportedLevel(p.Spec.Level))
	}
	if _, err := controller.ProfileRules(p); err != nil {
		return nil, http.StatusBadRequest, err
	}
	return p, 0, nil
}

// pausedForAccess repeats changeAccess's checks for a change that rewrites spec.https.
func (s *Server) pausedForAccess(r *http.Request, obj *api.ProjectWorkspace) (int, string) {
	if obj.Spec.DesiredState != "Paused" || obj.Status.Phase != "Paused" || obj.Status.ObservedGeneration != obj.Generation {
		return http.StatusConflict, "the sandbox has its own HTTPS rules; pause it and wait for Paused to replace them with a level"
	}
	var pods corev1.PodList
	if err := s.Client.List(r.Context(), &pods, client.InNamespace(obj.Status.Namespace), client.MatchingLabels{api.OwnerLabel: string(obj.UID)}); err != nil {
		return http.StatusServiceUnavailable, "cannot verify stopped project processes"
	}
	if obj.Status.Namespace == "" || len(pods.Items) != 0 {
		return http.StatusConflict, "waiting for all project processes to stop"
	}
	if !s.accessApplied(r, obj) {
		return http.StatusConflict, "previous access policy has not finished reconciling"
	}
	return 0, ""
}

// changeLevel sets a cell's access profile. Profile rules apply while the
// cell runs. Moving to a profile other than custom also removes the cell's own
// HTTPS rules, which needs the cell paused; both change in one update.
func (s *Server) changeLevel(w http.ResponseWriter, r *http.Request) {
	var request struct {
		ResourceVersion string `json:"resourceVersion"`
		Profile         string `json:"profile"`
	}
	if decode(w, r, &request) != nil || request.ResourceVersion == "" || request.Profile == "" {
		http.Error(w, "profile and resourceVersion required", http.StatusBadRequest)
		return
	}
	obj, ok := s.lanProject(w, r, request.ResourceVersion)
	if !ok {
		return
	}
	if obj.Spec.DesiredState == "Removed" {
		http.Error(w, "removed projects cannot change access", http.StatusConflict)
		return
	}
	p, code, err := s.projectProfile(r.Context(), request.Profile)
	if err != nil {
		http.Error(w, err.Error(), code)
		return
	}
	clearRules := p.Spec.Level != api.AccessLevelCustom && len(obj.Spec.HTTPS) > 0
	if clearRules {
		if code, msg := s.pausedForAccess(r, obj); code != 0 {
			http.Error(w, msg, code)
			return
		}
	}
	actor, _ := r.Context().Value(actorKey{}).(string)
	detail := map[string]string{"profile": p.Name, "level": string(p.Spec.Level)}
	message := fmt.Sprintf("Access level %s (profile %s) requested by %s", p.Spec.Level, p.Name, actor)
	if clearRules {
		message += fmt.Sprintf("; the sandbox's own %d HTTPS rules are removed", len(obj.Spec.HTTPS))
	}
	// The record comes first: a change that cannot be recorded is not made.
	if err := s.accessAudit(r.Context(), obj, "access-level-requested", actor, message, detail); err != nil {
		slog.ErrorContext(r.Context(), "access level refused: audit record failed", "workspace_uid", string(obj.UID), "error", err.Error())
		http.Error(w, "access change could not be recorded; nothing was changed", http.StatusServiceUnavailable)
		return
	}
	obj.Spec.AccessProfileName = p.Name
	if clearRules {
		obj.Spec.HTTPS = nil
	}
	if obj.Annotations == nil {
		obj.Annotations = map[string]string{}
	}
	obj.Annotations["projects.agentwork.dev/requested-by"] = actor
	if err := s.Client.Update(r.Context(), obj); err != nil {
		http.Error(w, "access update failed; refresh before retrying", http.StatusConflict)
		return
	}
	if obj.Spec.AccessProfileName != p.Name {
		http.Error(w, "the installed ProjectWorkspace CRD does not store accessProfileName; apply the updated CRD", http.StatusInternalServerError)
		return
	}
	writeJSON(w, obj)
}

// lockNow removes every grant of the cell, then sets its locked profile.
// Each step narrows, so an interruption leaves less access, and a retry
// finishes the job. The cell's own HTTPS rules and LAN hosts stay (removing
// HTTPS rules needs a pause); the response lists them.
func (s *Server) lockNow(w http.ResponseWriter, r *http.Request) {
	var request struct {
		ResourceVersion string `json:"resourceVersion"`
	}
	if decode(w, r, &request) != nil || request.ResourceVersion == "" {
		http.Error(w, "resourceVersion required", http.StatusBadRequest)
		return
	}
	obj, ok := s.lanProject(w, r, request.ResourceVersion)
	if !ok {
		return
	}
	locked, err := s.lockedProfile(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	actor, _ := r.Context().Value(actorKey{}).(string)
	if err := s.accessAudit(r.Context(), obj, "access-locked", actor, "Lock now by "+actor+": every access grant removed, profile "+locked, map[string]string{"profile": locked}); err != nil {
		http.Error(w, "lock could not be recorded; nothing was changed", http.StatusServiceUnavailable)
		return
	}
	grants, err := s.cellGrants(r.Context(), obj.Name)
	if err != nil {
		http.Error(w, "cannot list access grants; nothing was changed", http.StatusServiceUnavailable)
		return
	}
	for i := range grants {
		if err := s.Client.Delete(r.Context(), &grants[i]); err != nil && !apierrors.IsNotFound(err) {
			http.Error(w, fmt.Sprintf("removed %d of %d grants, then failed; retry Lock now", i, len(grants)), http.StatusInternalServerError)
			return
		}
	}
	if obj.Spec.AccessProfileName != locked {
		obj.Spec.AccessProfileName = locked
		if obj.Annotations == nil {
			obj.Annotations = map[string]string{}
		}
		obj.Annotations["projects.agentwork.dev/requested-by"] = actor
		if err := s.Client.Update(r.Context(), obj); err != nil {
			http.Error(w, "grants removed, but the locked profile could not be set; refresh and retry Lock now", http.StatusConflict)
			return
		}
	}
	writeJSON(w, map[string]any{"project": obj, "grantsRemoved": len(grants), "ownRules": len(obj.Spec.HTTPS), "lanHosts": len(obj.Spec.LANHosts)})
}

// lockedProfile names the profile Lock now uses: "locked", or the only
// profile at the locked level.
func (s *Server) lockedProfile(ctx context.Context) (string, error) {
	profiles, err := s.listProfiles(ctx)
	if err != nil {
		return "", errors.New("access profiles unavailable")
	}
	var found []string
	for _, p := range profiles {
		if p.Spec.Level == api.AccessLevelLocked {
			if p.Name == "locked" {
				return p.Name, nil
			}
			found = append(found, p.Name)
		}
	}
	if len(found) == 1 {
		return found[0], nil
	}
	return "", errors.New("no locked AccessProfile is installed; apply config/samples/accessprofiles.yaml")
}

func (s *Server) cellGrants(ctx context.Context, cell string) ([]api.AccessGrant, error) {
	var list api.AccessGrantList
	if err := s.Client.List(ctx, &list, client.InNamespace(s.Namespace), client.MatchingLabels{api.CellLabel: cell}); err != nil {
		if meta.IsNoMatchError(err) {
			return nil, nil
		}
		return nil, err
	}
	return list.Items, nil
}

func (s *Server) listGrants(w http.ResponseWriter, r *http.Request) {
	grants, err := s.cellGrants(r.Context(), r.PathValue("name"))
	if err != nil {
		http.Error(w, "access grants unavailable", http.StatusServiceUnavailable)
		return
	}
	if grants == nil {
		grants = []api.AccessGrant{}
	}
	writeJSON(w, grants)
}

// addGrant creates one AccessGrant after the checks the controller repeats:
// the profile allows the type and duration, and for LAN the address, ports
// and limits. Only a signed-in OIDC operator may widen access this way.
func (s *Server) addGrant(w http.ResponseWriter, r *http.Request) {
	actor, ok := lanOperator(w, r)
	if !ok {
		return
	}
	var request struct {
		Type    api.AccessGrantType `json:"type"`
		Minutes int                 `json:"minutes"`
		Reason  string              `json:"reason,omitempty"`
		LAN     *api.LANGrant       `json:"lan,omitempty"`
		Package *api.PackageGrant   `json:"package,omitempty"`
	}
	if decode(w, r, &request) != nil || request.Minutes <= 0 {
		http.Error(w, "type and minutes required", http.StatusBadRequest)
		return
	}
	obj := &api.ProjectWorkspace{}
	if err := s.Client.Get(r.Context(), types.NamespacedName{Namespace: s.Namespace, Name: r.PathValue("name")}, obj); err != nil {
		http.Error(w, "project unavailable", http.StatusNotFound)
		return
	}
	if !obj.DeletionTimestamp.IsZero() || obj.Spec.DesiredState == "Removed" {
		http.Error(w, "removed projects cannot receive grants", http.StatusConflict)
		return
	}
	switch request.Type {
	case api.AccessGrantLAN:
		if request.LAN == nil || request.Package != nil {
			http.Error(w, "a lan grant needs lan.address and lan.ports", http.StatusBadRequest)
			return
		}
	case api.AccessGrantInternet:
		http.Error(w, "the project proxy cannot open time-limited internet windows yet; nothing was opened", http.StatusBadRequest)
		return
	case api.AccessGrantPackage:
		http.Error(w, "the project proxy cannot enforce per-package approvals yet; allow the registry with the Standard level instead", http.StatusBadRequest)
		return
	default:
		http.Error(w, "type must be lan, internet or package", http.StatusBadRequest)
		return
	}
	if obj.Spec.AccessProfileName == "" {
		http.Error(w, "choose an access level for this sandbox before adding grants", http.StatusConflict)
		return
	}
	p, code, err := s.projectProfile(r.Context(), obj.Spec.AccessProfileName)
	if err != nil {
		http.Error(w, err.Error(), code)
		return
	}
	expires := time.Duration(request.Minutes) * time.Minute
	if !p.Allows(request.Type) {
		http.Error(w, fmt.Sprintf("the %s level does not allow %s grants; choose Custom or Standard first", p.Spec.Level, request.Type), http.StatusConflict)
		return
	}
	if limit := p.Spec.MaxGrantDuration[request.Type].Duration; limit <= 0 || expires > limit {
		http.Error(w, fmt.Sprintf("%s grants last at most %s at the %s level", request.Type, limit, p.Spec.Level), http.StatusBadRequest)
		return
	}
	lan := *request.LAN
	lan.Address, lan.Note = strings.TrimSpace(lan.Address), strings.TrimSpace(lan.Note)
	if s.LAN == nil || !s.LAN.Config.Enabled() {
		http.Error(w, "LAN host grants are disabled on this controller; set LAN_GRANT_ALLOWED_CIDRS", http.StatusForbidden)
		return
	}
	if err := network.CheckPorts(lan.Ports); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := network.CheckNote(lan.Note); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	nodes, err := s.LAN.Nodes(r.Context(), s.Client)
	if err != nil {
		http.Error(w, "cannot verify cluster node addresses; try again", http.StatusServiceUnavailable)
		return
	}
	existing, err := s.cellGrants(r.Context(), obj.Name)
	if err != nil {
		http.Error(w, "access grants unavailable", http.StatusServiceUnavailable)
		return
	}
	now := time.Now().UTC().Truncate(time.Second)
	grant := &api.AccessGrant{
		ObjectMeta: metav1.ObjectMeta{Name: obj.Name + "-lan-" + randomSuffix(), Namespace: s.Namespace, Labels: map[string]string{api.CellLabel: obj.Name},
			OwnerReferences: []metav1.OwnerReference{{APIVersion: api.GroupVersion.String(), Kind: "ProjectWorkspace", Name: obj.Name, UID: obj.UID}}},
		Spec: api.AccessGrantSpec{Cell: obj.Name, Type: api.AccessGrantLAN, LAN: &lan, ExpiresAt: metav1.NewTime(now.Add(expires)), Reason: strings.TrimSpace(request.Reason), RequestedBy: actor},
	}
	// The controller's own resolution decides whether it would apply.
	trial := *grant
	trial.CreationTimestamp = metav1.NewTime(now)
	_, decisions := controller.ResolveGrants(obj, p, append(slices.Clone(existing), trial), s.LAN.Config, nodes, now)
	for _, d := range decisions {
		if d.Grant.Name == grant.Name && d.State != api.AccessGrantActive {
			http.Error(w, d.Message, http.StatusBadRequest)
			return
		}
	}
	message := fmt.Sprintf("LAN grant %s TCP %s for %s requested by %s", lan.Address, portList(lan.Ports), expires, actor)
	if err := s.accessAudit(r.Context(), obj, "grant-requested", actor, message, map[string]string{"grant": grant.Name, "type": "lan", "expiresAt": grant.Spec.ExpiresAt.Format(time.RFC3339)}); err != nil {
		http.Error(w, "grant could not be recorded; nothing was granted", http.StatusServiceUnavailable)
		return
	}
	if err := s.Client.Create(r.Context(), grant); err != nil {
		http.Error(w, "grant could not be saved", http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, grant)
}

// removeGrant ends one grant at once. Removing narrows access, so any
// signed-in operator may do it.
func (s *Server) removeGrant(w http.ResponseWriter, r *http.Request) {
	g := &api.AccessGrant{}
	if err := s.Client.Get(r.Context(), types.NamespacedName{Namespace: s.Namespace, Name: r.PathValue("grant")}, g); err != nil {
		http.Error(w, "grant not found", http.StatusNotFound)
		return
	}
	if g.Spec.Cell != r.PathValue("name") {
		http.Error(w, "grant belongs to another sandbox", http.StatusNotFound)
		return
	}
	obj := &api.ProjectWorkspace{}
	if err := s.Client.Get(r.Context(), types.NamespacedName{Namespace: s.Namespace, Name: g.Spec.Cell}, obj); err == nil {
		actor, _ := r.Context().Value(actorKey{}).(string)
		if err := s.accessAudit(r.Context(), obj, "grant-removed", actor, fmt.Sprintf("Access grant %s (%s) removed by %s", g.Name, g.Spec.Type, actor), map[string]string{"grant": g.Name}); err != nil {
			slog.ErrorContext(r.Context(), "grant removal audit failed; removing anyway", "grant", g.Name, "error", err.Error())
		}
	}
	if err := s.Client.Delete(r.Context(), g); err != nil && !apierrors.IsNotFound(err) {
		http.Error(w, "grant could not be removed; retry", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"removed": g.Name})
}

func portList(ports []int32) string {
	parts := make([]string, 0, len(ports))
	for _, p := range ports {
		parts = append(parts, fmt.Sprint(p))
	}
	return strings.Join(parts, ",")
}

func randomSuffix() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// accessAudit writes an immutable record next to the controller's audit
// ConfigMaps. Its name derives from the request, so a retry of the same
// request finds its record instead of failing.
func (s *Server) accessAudit(ctx context.Context, obj *api.ProjectWorkspace, action, actor, message string, details map[string]string) error {
	raw, _ := json.Marshal(details)
	sum := sha256.Sum256([]byte(action + "\n" + actor + "\n" + message + "\n" + string(raw)))
	data := map[string]string{"workspace": obj.Name, "workspaceUID": string(obj.UID), "generation": fmt.Sprint(obj.Generation), "action": action, "actor": actor, "time": time.Now().UTC().Format(time.RFC3339), "message": message}
	for k, v := range details {
		data[k] = v
	}
	return s.auditLog().Record(ctx, string(obj.UID), fmt.Sprintf("audit-%s-%d-%s-%s", stringUID(obj), obj.Generation, action, hex.EncodeToString(sum[:6])), data, "console")
}
