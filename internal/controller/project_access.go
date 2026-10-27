package controller

import (
	"context"
	"fmt"
	"strings"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	"k8s.io/apimachinery/pkg/util/validation"
)

// projectAccessName is the RoleBinding that gives the controller what it
// needs inside one project namespace and must not hold cluster-wide: the
// project's Secrets (git-identity, proxy-ca, model-access) and deleting the
// project namespace itself.
const projectAccessName = "agent-project-controller-access"

func validateProjectAccess(role, account string) error {
	role = strings.TrimSpace(role)
	if role == "" {
		return nil
	}
	if errs := validation.IsDNS1123Subdomain(role); len(errs) > 0 {
		return fmt.Errorf("project access cluster role invalid: %s", strings.Join(errs, "; "))
	}
	if account == "" {
		return nil
	}
	if errs := validation.IsDNS1123Subdomain(account); len(errs) > 0 {
		return fmt.Errorf("controller service account invalid: %s", strings.Join(errs, "; "))
	}
	return nil
}

// ensureProjectAccess binds ProjectAccessClusterRole to the controller's own
// ServiceAccount in this project namespace only.
//
// A RoleBinding in namespace apc-<uid> that grants `namespaces` verbs applies
// to the namespace object apc-<uid> itself (the API server authorizes
// /api/v1/namespaces/<name> with namespace <name>), so the controller can
// delete its own project namespaces without a cluster-wide delete.
//
// Empty ProjectAccessClusterRole leaves project RBAC unchanged (an
// installation that still grants Secrets and namespace delete cluster-wide).
func (r *Reconciler) ensureProjectAccess(ctx context.Context, w *api.ProjectWorkspace) error {
	role := strings.TrimSpace(r.Config.ProjectAccessClusterRole)
	if role == "" {
		return nil
	}
	if err := validateProjectAccess(role, r.Config.ControllerServiceAccount); err != nil {
		return err
	}
	return r.ensureSelfBinding(ctx, w, projectAccessName, role)
}
