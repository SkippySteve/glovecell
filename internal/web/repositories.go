package web

import (
	"net/http"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/controller"
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
	var obj api.ProjectWorkspace
	if s.Client.Get(r.Context(), types.NamespacedName{Namespace: s.Namespace, Name: r.PathValue("name")}, &obj) != nil {
		http.Error(w, "workbench unavailable", 404)
		return
	}
	if obj.ResourceVersion != request.ResourceVersion || obj.Spec.DesiredState != "Paused" || obj.Status.Phase != "Paused" || obj.Status.ObservedGeneration != obj.Generation || !obj.DeletionTimestamp.IsZero() {
		http.Error(w, "pause the whole workbench and wait for Paused before enrolling a repository", 409)
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
	if (request.Project.Host == s.GitHost || request.Project.Host=="forgejo" && s.GitHost=="") && request.Project.Repository == obj.Spec.Repository {
		http.Error(w, "repository is already the original checkout", 409)
		return
	}
	obj.Spec.Repositories = append(obj.Spec.Repositories, request.Project)
	if obj.Annotations == nil {
		obj.Annotations = map[string]string{}
	}
	actor, _ := r.Context().Value(actorKey{}).(string)
	obj.Annotations["projects.agentwork.dev/requested-by"] = actor
	if s.Client.Update(r.Context(), &obj) != nil {
		http.Error(w, "enrollment changed; refresh before retrying", 409)
		return
	}
	w.WriteHeader(http.StatusAccepted)
	writeJSON(w, &obj)
}
