package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/util/validation"
)

// hubLogsAccessName is the RoleBinding that lets the Orca hub read worker
// pods and their logs (one-time pairing offers) in one project namespace.
const hubLogsAccessName = "orca-hub-logs"

// DefaultHubServiceAccount is the hub's ServiceAccount in OrcaHubNamespace.
const DefaultHubServiceAccount = "orca-hub"

func validateHubLogs(role, account string, hubSpoke bool) error {
	role = strings.TrimSpace(role)
	if role == "" {
		return nil
	}
	if !hubSpoke {
		return errors.New("HUBSPOKE_HUB_LOGS_CLUSTER_ROLE requires HUBSPOKE_ORCA_ENABLED=true")
	}
	if errs := validation.IsDNS1123Subdomain(role); len(errs) > 0 {
		return fmt.Errorf("hub logs cluster role invalid: %s", strings.Join(errs, "; "))
	}
	if account != "" {
		if errs := validation.IsDNS1123Subdomain(account); len(errs) > 0 {
			return fmt.Errorf("hub service account invalid: %s", strings.Join(errs, "; "))
		}
	}
	return nil
}

func (r *Reconciler) hubServiceAccount() string {
	if a := strings.TrimSpace(r.Config.HubServiceAccount); a != "" {
		return a
	}
	return DefaultHubServiceAccount
}

func (r *Reconciler) hubNamespace() string {
	if ns := strings.TrimSpace(r.Config.OrcaHubNamespace); ns != "" {
		return ns
	}
	return "agent-project-control-orca-hub"
}

// ensureHubLogsAccess binds HubLogsClusterRole to the hub's ServiceAccount in
// this project namespace only, so the hub can read worker logs in cells and
// nowhere else (no cluster-wide pods/log grant). Empty HubLogsClusterRole
// leaves project RBAC unchanged (an installation that still grants the hub
// cluster-wide read).
func (r *Reconciler) ensureHubLogsAccess(ctx context.Context, w *api.ProjectWorkspace) error {
	role := strings.TrimSpace(r.Config.HubLogsClusterRole)
	if role == "" || !r.Config.OrcaHubSpoke {
		return nil
	}
	if err := validateHubLogs(role, r.Config.HubServiceAccount, r.Config.OrcaHubSpoke); err != nil {
		return err
	}
	return r.ensureProjectBinding(ctx, w, hubLogsAccessName, role, rbacv1.Subject{
		Kind:      rbacv1.ServiceAccountKind,
		Name:      r.hubServiceAccount(),
		Namespace: r.hubNamespace(),
	})
}
