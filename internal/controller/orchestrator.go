package controller

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/orchestrator"
)

// orchestratorBound is the audit action recorded before a workspace's named
// orchestrator class is first asked to grant access. Removal uses it to tell
// a class that never granted anything from one whose revocation is pending.
const orchestratorBound = "orchestrator-bound"

// orchestratorAccess resolves the workspace's orchestrator. An empty
// spec.orchestratorClassName uses r.Access, the deployment default (T3 Connect
// when enabled, which is the default t3 class by translation; otherwise
// none). A named class goes through r.Orchestrators and fails closed.
func (r *Reconciler) orchestratorAccess(ctx context.Context, w *api.ProjectWorkspace) (ProjectAccess, error) {
	return orchestrator.Resolve(ctx, r.Client, r.Orchestrators, r.Access, w)
}

// usesOrchestrator reports whether the worker is built for an orchestrator
// (it then carries the project name as its hostname). It needs no API call,
// so pod construction does not depend on class lookups.
func (r *Reconciler) usesOrchestrator(w *api.ProjectWorkspace) bool {
	return r.Access != nil || w.Spec.OrchestratorClassName != ""
}

// suspendOrchestrators stops orchestrator access for a workspace. It suspends
// the resolved adapter and the deployment default (a class may be set on a
// workspace that used the default before). When the class cannot be
// resolved it suspends every installed adapter: Suspend is cheap where
// nothing is held, and an unknown class must not leave access running.
func (r *Reconciler) suspendOrchestrators(ctx context.Context, w *api.ProjectWorkspace, resolved ProjectAccess, resolveErr error) error {
	var targets []ProjectAccess
	add := func(a ProjectAccess) {
		if a == nil {
			return
		}
		for _, t := range targets {
			if sameAccess(t, a) {
				return
			}
		}
		targets = append(targets, a)
	}
	add(resolved)
	add(r.Access)
	if resolveErr != nil {
		for _, a := range r.Orchestrators.Adapters() {
			add(a)
		}
	}
	for _, a := range targets {
		if e := a.Suspend(ctx, w); e != nil {
			return e
		}
	}
	return nil
}

// removeOrchestrator revokes orchestrator access before storage is deleted.
// The default path (no class) is unchanged. For a named class the adapter's
// Remove must succeed, the deployment default is revoked too (the workspace
// may have used it before the class was set), and a class that cannot be
// resolved blocks removal once it was ever bound. The caller still checks
// T3 history when T3 Connect is disabled.
func (r *Reconciler) removeOrchestrator(ctx context.Context, w *api.ProjectWorkspace) (err error) {
	access, resolveErr := r.orchestratorAccess(ctx, w)
	if resolveErr != nil {
		bound, e := r.orchestratorWasBound(ctx, w)
		if e != nil {
			return e
		}
		if bound {
			err = fmt.Errorf("%w; removal waits until the class is restored and its adapter confirms revocation", resolveErr)
		}
		// Never bound: the class granted nothing, so there is nothing to revoke.
	} else if access != nil {
		err = access.Remove(ctx, w)
	}
	if r.Access != nil && !sameAccess(r.Access, access) {
		err = errors.Join(err, r.Access.Remove(ctx, w))
	}
	return err
}

// sameAccess compares adapters without panicking on non-comparable types.
func sameAccess(a, b ProjectAccess) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	ta, tb := reflect.TypeOf(a), reflect.TypeOf(b)
	return ta == tb && ta.Comparable() && a == b
}

func (r *Reconciler) orchestratorWasBound(ctx context.Context, w *api.ProjectWorkspace) (bool, error) {
	return r.auditLog().Has(ctx, string(w.UID), orchestratorBound)
}

// classRejection returns the Ready reason for a refused workspace.
func classRejection(e error) string {
	var ce *orchestrator.ClassError
	if errors.As(e, &ce) {
		return ce.Reason
	}
	return "PolicyRejected"
}
