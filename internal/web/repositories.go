package web

import (
	"net/http"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/controller"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
)

func (s *Server) enrollRepository(w http.ResponseWriter, r *http.Request) {
	var request struct {
		ResourceVersion string                `json:"resourceVersion"`
		Project         api.RepositoryProject `json:"project"`
	}
	if decode(w, r, &request) != nil || request.ResourceVersion == "" || controller.ValidateProject(request.Project, s.Owner, s.GitHost, s.GitHubOwner) != nil {
		http.Error(w, "valid project name, repository host, owner/name and resourceVersion required; private GitHub authorization is not configured", 400)
		return
	}
	actor, _ := r.Context().Value(actorKey{}).(string)
	name := r.PathValue("name")
	// Ignore exact resourceVersion match (status churn); re-get and retry conflicts.
	for attempt := 0; attempt < 5; attempt++ {
		var obj api.ProjectWorkspace
		if s.Client.Get(r.Context(), types.NamespacedName{Namespace: s.Namespace, Name: name}, &obj) != nil {
			http.Error(w, "workbench unavailable", 404)
			return
		}
		if !obj.DeletionTimestamp.IsZero() {
			http.Error(w, "project changed; refresh before retrying", 409)
			return
		}
		// Enroll while Ready (Running) or settled Paused. Pause is not required:
		// the reconciler grants access, widens policy, and refreshes the worker
		// so bootstrap clones onto the existing volume.
		running := obj.Spec.DesiredState == api.DesiredRunning && obj.Settled(api.PhaseReady)
		paused := obj.Spec.DesiredState == api.DesiredPaused && obj.Settled(api.PhasePaused)
		if !running && !paused {
			http.Error(w, "wait until the sandbox is Ready (or settled Paused) before enrolling a repository", 409)
			return
		}
		if len(obj.Spec.Repositories) >= 16 {
			http.Error(w, "additional repository limit reached", 409)
			return
		}
		for _, p := range obj.Spec.Repositories {
			if p.Name == request.Project.Name || p.Host == request.Project.Host && p.Repository == request.Project.Repository {
				http.Error(w, "repository project already enrolled", 409)
				return
			}
		}
		if (request.Project.Host == s.GitHost || request.Project.Host == "forgejo" && s.GitHost == "") && request.Project.Repository == obj.Spec.Repository {
			http.Error(w, "repository is already the original checkout", 409)
			return
		}
		obj.Spec.Repositories = append(obj.Spec.Repositories, request.Project)
		if obj.Annotations == nil {
			obj.Annotations = map[string]string{}
		}
		obj.Annotations["projects.agentwork.dev/requested-by"] = actor
		if e := s.Client.Update(r.Context(), &obj); e != nil {
			if apierrors.IsConflict(e) && attempt < 4 {
				continue
			}
			http.Error(w, "enrollment changed; refresh before retrying", 409)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		writeJSON(w, &obj)
		return
	}
}
