package orchestrator

import (
	"context"
	"reflect"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	"k8s.io/apimachinery/pkg/api/meta"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ClassReconciler sets the Accepted condition on OrchestratorClasses whose
// controllerName this controller owns (prefix projects.agentwork.dev/).
// Classes for other controllers are left alone. Workspaces do not depend on
// this status: they resolve their class directly with the same check, so a
// stale condition never grants access.
type ClassReconciler struct {
	Client   client.Client
	Registry *Registry
}

func (r *ClassReconciler) Setup(m ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(m).Named("orchestratorclass").For(&api.OrchestratorClass{}).Complete(r)
}

func (r *ClassReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	class := &api.OrchestratorClass{}
	if err := r.Client.Get(ctx, req.NamespacedName, class); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !Owned(class.Spec.ControllerName) {
		return ctrl.Result{}, nil
	}
	old := class.DeepCopyObject().(*api.OrchestratorClass)
	meta.SetStatusCondition(&class.Status.Conditions, r.Registry.AcceptedCondition(class))
	if reflect.DeepEqual(old.Status, class.Status) {
		return ctrl.Result{}, nil
	}
	return ctrl.Result{}, r.Client.Status().Patch(ctx, class, client.MergeFrom(old))
}
