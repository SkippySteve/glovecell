package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// gloveHubAccessName is the RoleBinding that lets glove-hub, the in-cluster
// Herdr hub, find and exec into the worker of one Herdr workspace.
const gloveHubAccessName = "glove-hub-exec"

func validateGloveHub(role, namespace, account string, herdrCore bool) error {
	role = strings.TrimSpace(role)
	if role == "" {
		return nil
	}
	if !herdrCore {
		return errors.New("GLOVE_HUB_EXEC_CLUSTER_ROLE requires HERDR_CORE_ENABLED=true")
	}
	for what, v := range map[string]string{"cluster role": role, "namespace": namespace, "service account": account} {
		if strings.TrimSpace(v) == "" {
			return fmt.Errorf("glove-hub %s required", what)
		}
		if errs := validation.IsDNS1123Subdomain(v); len(errs) > 0 {
			return fmt.Errorf("glove-hub %s invalid: %s", what, strings.Join(errs, "; "))
		}
	}
	return nil
}

// ensureGloveHubAccess binds GloveHubExecClusterRole to glove-hub's
// ServiceAccount in this project namespace while the workspace uses the
// Herdr class, and deletes that binding otherwise. Orca and T3 cells never
// grant the hub anything. Empty GloveHubExecClusterRole changes nothing.
func (r *Reconciler) ensureGloveHubAccess(ctx context.Context, w *api.ProjectWorkspace) error {
	role := strings.TrimSpace(r.Config.GloveHubExecClusterRole)
	if role == "" || !r.Config.HerdrCoreEnabled {
		return nil
	}
	if err := validateGloveHub(role, r.Config.GloveHubNamespace, r.Config.GloveHubServiceAccount, r.Config.HerdrCoreEnabled); err != nil {
		return err
	}
	if r.usesHerdrCore(w) {
		return r.ensureProjectBinding(ctx, w, gloveHubAccessName, role, rbacv1.Subject{
			Kind:      rbacv1.ServiceAccountKind,
			Name:      r.Config.GloveHubServiceAccount,
			Namespace: r.Config.GloveHubNamespace,
		})
	}
	binding := &rbacv1.RoleBinding{}
	err := r.Get(ctx, client.ObjectKey{Namespace: namespace(w), Name: gloveHubAccessName}, binding)
	switch {
	case apierrors.IsNotFound(err):
		return nil
	case err != nil:
		return err
	case binding.Labels[api.OwnerLabel] != string(w.UID):
		return fmt.Errorf("RoleBinding %s/%s is not owned by this project", namespace(w), gloveHubAccessName)
	}
	if err := r.Delete(ctx, binding); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}
