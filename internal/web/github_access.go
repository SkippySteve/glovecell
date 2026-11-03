package web

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/githubpolicy"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Brokered GitHub access (spec.github) in the console API. Like provider
// opt-in it needs a signed-in OIDC operator; the automation bearer and the
// static login get 403. Revocation works with the feature off, so access can
// always be withdrawn. Every change writes an immutable audit record.

// setGitHub opts a project into GitHub access or replaces its policy. Existing
// approvals are kept.
func (s *Server) setGitHub(w http.ResponseWriter, r *http.Request) {
	actor, ok := lanOperator(w, r)
	if !ok {
		return
	}
	if !s.GitHubBrokering {
		http.Error(w, "brokered GitHub access is off on this controller; set GITHUB_LEASE_SECRET", http.StatusForbidden)
		return
	}
	var request struct {
		ResourceVersion   string   `json:"resourceVersion"`
		Repositories      []string `json:"repositories"`
		ProtectedBranches []string `json:"protectedBranches"`
		PushBranches      []string `json:"pushBranches"`
		AllowMerge        bool     `json:"allowMerge"`
		AllowReleases     bool     `json:"allowReleases"`
	}
	if decode(w, r, &request) != nil || request.ResourceVersion == "" {
		http.Error(w, "repositories and resourceVersion required", http.StatusBadRequest)
		return
	}
	obj, ok := s.lanProject(w, r, request.ResourceVersion)
	if !ok {
		return
	}
	if obj.Spec.DesiredState == "Removed" {
		http.Error(w, "removed projects cannot receive GitHub access", http.StatusConflict)
		return
	}
	now := metav1.NewTime(time.Now().UTC().Truncate(time.Second))
	next := &api.GitHubAccess{Repositories: request.Repositories, ProtectedBranches: request.ProtectedBranches, PushBranches: request.PushBranches, AllowMerge: request.AllowMerge, AllowReleases: request.AllowReleases, AddedBy: actor, AddedAt: &now}
	if obj.Spec.GitHub != nil {
		next.Approvals = obj.Spec.GitHub.Approvals
	}
	check := (&githubpolicy.Policy{Repositories: next.Repositories, ProtectedBranches: next.ProtectedBranches, PushBranches: next.PushBranches}).Validate()
	if check != nil {
		http.Error(w, check.Error(), http.StatusBadRequest)
		return
	}
	obj.Spec.GitHub = next
	summary := fmt.Sprintf("repositories %s; merge %t; releases %t; protected %s; push only %s", strings.Join(next.Repositories, ", "), next.AllowMerge, next.AllowReleases, strings.Join(next.ProtectedBranches, ", "), strings.Join(next.PushBranches, ", "))
	s.saveGitHub(w, r, obj, actor, "github-enabled", summary, http.StatusOK)
}

// removeGitHub revokes GitHub access for one project.
func (s *Server) removeGitHub(w http.ResponseWriter, r *http.Request) {
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
	obj, ok := s.lanProject(w, r, request.ResourceVersion)
	if !ok {
		return
	}
	if obj.Spec.GitHub == nil {
		http.Error(w, "GitHub access is not enabled for this project", http.StatusNotFound)
		return
	}
	obj.Spec.GitHub = nil
	s.saveGitHub(w, r, obj, actor, "github-revoked", "GitHub access revoked", http.StatusOK)
}

// approveGitHub approves one repository creation or fork for exactly the
// given arguments. It expires after minutes (default 60, at most a day) and
// the proxy uses it once. Expired approvals are pruned.
func (s *Server) approveGitHub(w http.ResponseWriter, r *http.Request) {
	actor, ok := lanOperator(w, r)
	if !ok {
		return
	}
	var request struct {
		ResourceVersion string `json:"resourceVersion"`
		Action          string `json:"action"`
		Name            string `json:"name"`
		Visibility      string `json:"visibility"`
		Source          string `json:"source"`
		Minutes         int    `json:"minutes"`
	}
	if decode(w, r, &request) != nil || request.ResourceVersion == "" {
		http.Error(w, "action, name and resourceVersion required", http.StatusBadRequest)
		return
	}
	if request.Minutes == 0 {
		request.Minutes = 60
	}
	if request.Minutes < 1 || time.Duration(request.Minutes)*time.Minute > githubpolicy.MaxApprovalLifetime {
		http.Error(w, "minutes must be between 1 and 1440", http.StatusBadRequest)
		return
	}
	obj, ok := s.lanProject(w, r, request.ResourceVersion)
	if !ok {
		return
	}
	if obj.Spec.GitHub == nil {
		http.Error(w, "enable GitHub access for this project first", http.StatusConflict)
		return
	}
	now := time.Now().UTC().Truncate(time.Second)
	approval := api.GitHubApproval{Action: request.Action, Name: request.Name, Visibility: request.Visibility, Source: request.Source, ExpiresAt: metav1.NewTime(now.Add(time.Duration(request.Minutes) * time.Minute)), ApprovedBy: actor}
	check := githubpolicy.Approval{Action: approval.Action, Name: approval.Name, Visibility: approval.Visibility, Source: approval.Source, ExpiresAt: approval.ExpiresAt.Time, ApprovedBy: actor}
	if err := check.Validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	live := slices.DeleteFunc(slices.Clone(obj.Spec.GitHub.Approvals), func(a api.GitHubApproval) bool { return !a.ExpiresAt.Time.After(now) })
	if len(live) >= githubpolicy.MaxApprovals {
		http.Error(w, "too many open GitHub approvals", http.StatusConflict)
		return
	}
	obj.Spec.GitHub.Approvals = append(live, approval)
	summary := fmt.Sprintf("%s %s", approval.Action, approval.Name)
	if approval.Visibility != "" {
		summary += " (" + approval.Visibility + ")"
	}
	if approval.Source != "" {
		summary += " from " + approval.Source
	}
	s.saveGitHub(w, r, obj, actor, "github-approved", summary+" until "+approval.ExpiresAt.Format(time.RFC3339), http.StatusCreated)
}

func (s *Server) saveGitHub(w http.ResponseWriter, r *http.Request, obj *api.ProjectWorkspace, actor, action, summary string, status int) {
	if obj.Annotations == nil {
		obj.Annotations = map[string]string{}
	}
	obj.Annotations["projects.agentwork.dev/requested-by"] = actor
	wanted := obj.Spec.GitHub != nil
	if err := s.Client.Update(r.Context(), obj); err != nil {
		http.Error(w, "GitHub access could not be saved; refresh before retrying", http.StatusConflict)
		return
	}
	// A CRD without the github field prunes it silently.
	if wanted && obj.Spec.GitHub == nil {
		http.Error(w, "the installed ProjectWorkspace CRD does not store github; apply the updated CRD", http.StatusInternalServerError)
		return
	}
	if err := s.githubAudit(r.Context(), obj, action, summary, actor); err != nil {
		slog.ErrorContext(r.Context(), "GitHub access saved without its audit record", "workspace_uid", string(obj.UID), "action", action, "error", err.Error())
		http.Error(w, "GitHub access saved, but its audit record failed; check the controller log", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(status)
	writeJSON(w, obj)
}

func (s *Server) githubAudit(ctx context.Context, obj *api.ProjectWorkspace, action, summary, actor string) error {
	now := time.Now().UTC()
	slog.InfoContext(ctx, "brokered GitHub access changed", "workspace", obj.Name, "workspace_uid", string(obj.UID), "generation", obj.Generation, "actor", actor, "action", action, "summary", summary)
	immutable := true
	name := fmt.Sprintf("audit-%s-%d-%s-%d", stringUID(obj), obj.Generation, action, now.UnixNano()%1_000_000)
	data := map[string]string{
		"workspace": obj.Name, "workspaceUID": string(obj.UID), "generation": fmt.Sprint(obj.Generation),
		"action": action, "actor": actor, "time": now.Format(time.RFC3339), "message": "GitHub: " + summary + " by " + actor,
	}
	return s.Client.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: s.Namespace, Labels: map[string]string{api.OwnerLabel: string(obj.UID)}}, Immutable: &immutable, Data: data})
}
