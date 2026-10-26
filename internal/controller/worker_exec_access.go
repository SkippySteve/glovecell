package controller

import (
	"context"
	"fmt"
	"strings"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// workerExecAccessName is the RoleBinding that lets the controller exec into
// the workers of one project namespace (T3 Connect, and the escalation check
// when it grants the registrar the same).
const workerExecAccessName = "agent-project-controller-exec"

// DefaultControllerServiceAccount is the controller's account in the control
// namespace, as installed by the chart and config/deploy.
const DefaultControllerServiceAccount = "controller"

func validateWorkerExec(role, account string) error {
	role = strings.TrimSpace(role)
	if role == "" {
		return nil
	}
	if errs := validation.IsDNS1123Subdomain(role); len(errs) > 0 {
		return fmt.Errorf("worker exec cluster role invalid: %s", strings.Join(errs, "; "))
	}
	if account == "" {
		return nil
	}
	if errs := validation.IsDNS1123Subdomain(account); len(errs) > 0 {
		return fmt.Errorf("controller service account invalid: %s", strings.Join(errs, "; "))
	}
	return nil
}

func (r *Reconciler) controllerServiceAccount() string {
	if r.Config.ControllerServiceAccount != "" {
		return r.Config.ControllerServiceAccount
	}
	return DefaultControllerServiceAccount
}

// ensureWorkerExecAccess binds the WorkerExecClusterRole to the controller's
// own ServiceAccount in this project namespace only. The controller then has
// pods/exec in the namespaces it creates and nowhere else, without a
// cluster-wide grant. Empty WorkerExecClusterRole leaves project RBAC
// unchanged (an installation that still grants exec cluster-wide).
//
// The roleRef of a RoleBinding is immutable, so a binding that points at
// another role is deleted and recreated; a binding not labelled for this
// project is never adopted.
func (r *Reconciler) ensureWorkerExecAccess(ctx context.Context, w *api.ProjectWorkspace) error {
	role := strings.TrimSpace(r.Config.WorkerExecClusterRole)
	if role == "" {
		return nil
	}
	if err := validateWorkerExec(role, r.Config.ControllerServiceAccount); err != nil {
		return err
	}
	want := rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: role}
	subjects := []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: r.controllerServiceAccount(), Namespace: r.Config.Namespace}}
	ns := namespace(w)
	binding := &rbacv1.RoleBinding{}
	err := r.Get(ctx, client.ObjectKey{Namespace: ns, Name: workerExecAccessName}, binding)
	switch {
	case apierrors.IsNotFound(err):
	case err != nil:
		return err
	case binding.Labels[api.OwnerLabel] != string(w.UID):
		return fmt.Errorf("worker exec binding is not owned by this project")
	case binding.RoleRef == want:
		if len(binding.Subjects) == 1 && binding.Subjects[0] == subjects[0] {
			return nil
		}
		binding.Subjects = subjects
		return r.Update(ctx, binding)
	default:
		if err := r.Delete(ctx, binding); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	return r.Create(ctx, &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: workerExecAccessName, Namespace: ns, Labels: map[string]string{api.OwnerLabel: string(w.UID)}},
		RoleRef:    want,
		Subjects:   subjects,
	})
}
