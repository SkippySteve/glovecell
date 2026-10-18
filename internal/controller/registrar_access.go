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
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const registrarServiceAccount = "registrar"
const registrarAccessName = "registrar-exec"

// validateRegistrarNamespace rejects a control-client namespace that would put
// the privileged registrar in the restricted control namespace.
func validateRegistrarNamespace(registrar, control string) error {
	registrar = strings.TrimSpace(registrar)
	if registrar == "" {
		return nil
	}
	if errs := validation.IsDNS1123Label(registrar); len(errs) > 0 {
		return fmt.Errorf("registrar namespace invalid: %s", strings.Join(errs, "; "))
	}
	if registrar == control {
		return fmt.Errorf("registrar namespace must be separate from the control namespace")
	}
	return nil
}

func registrarPodRules() []rbacv1.PolicyRule {
	return []rbacv1.PolicyRule{
		{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"get", "list", "watch"}},
		{APIGroups: []string{""}, Resources: []string{"pods/exec"}, Verbs: []string{"create"}},
	}
}

// ensureRegistrarAccess grants the control-client service account exec into this
// project namespace only. Empty RegistrarNamespace leaves project RBAC unchanged.
func (r *Reconciler) ensureRegistrarAccess(ctx context.Context, w *api.ProjectWorkspace) error {
	target := strings.TrimSpace(r.Config.RegistrarNamespace)
	if target == "" {
		return nil
	}
	if err := validateRegistrarNamespace(target, r.Config.Namespace); err != nil {
		return err
	}
	ns := namespace(w)
	role := &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: registrarAccessName, Namespace: ns}}
	if err := r.Get(ctx, client.ObjectKeyFromObject(role), role); err == nil {
		if role.Labels[api.OwnerLabel] != string(w.UID) {
			return fmt.Errorf("registrar access role is not owned by this project")
		}
	} else if !apierrors.IsNotFound(err) {
		return err
	}
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, role, func() error {
		if role.Labels == nil {
			role.Labels = map[string]string{}
		}
		role.Labels[api.OwnerLabel] = string(w.UID)
		role.Rules = registrarPodRules()
		return nil
	}); err != nil {
		return err
	}
	binding := &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: registrarAccessName, Namespace: ns}}
	if err := r.Get(ctx, client.ObjectKeyFromObject(binding), binding); err == nil {
		if binding.Labels[api.OwnerLabel] != string(w.UID) {
			return fmt.Errorf("registrar access binding is not owned by this project")
		}
	} else if !apierrors.IsNotFound(err) {
		return err
	}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, binding, func() error {
		if binding.Labels == nil {
			binding.Labels = map[string]string{}
		}
		binding.Labels[api.OwnerLabel] = string(w.UID)
		binding.RoleRef = rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: registrarAccessName}
		binding.Subjects = []rbacv1.Subject{{
			Kind:      rbacv1.ServiceAccountKind,
			Name:      registrarServiceAccount,
			Namespace: target,
		}}
		return nil
	})
	return err
}
