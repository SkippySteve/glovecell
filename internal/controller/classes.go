package controller

import (
	"context"
	"fmt"
	"reflect"
	"time"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/forge"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/providerbroker"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// AcceptedCondition reports whether this controller can use a class.
const AcceptedCondition = "Accepted"

// classRecheck re-reads classes whose inputs (a credential Secret, the
// controller's lease source) can change without a class event.
const classRecheck = 5 * time.Minute

// ClassReconciler reports in status.conditions[type=Accepted] whether each
// ProviderClass and ForgeClass is usable by this controller. It only writes
// class status; workspaces resolve classes themselves on every
// reconciliation, so a status that lags never grants anything.
type ClassReconciler struct {
	client.Client
	ProviderLeases *types.NamespacedName
	Forges         *forge.Resolver
}

// ClassesServed reports whether the cluster serves both class kinds. A
// controller started against an older CRD set runs without class status.
func ClassesServed(mapper meta.RESTMapper) bool {
	for _, kind := range []string{"ProviderClass", "ForgeClass"} {
		if _, err := mapper.RESTMapping(schema.GroupKind{Group: api.GroupVersion.Group, Kind: kind}, api.GroupVersion.Version); err != nil {
			return false
		}
	}
	return true
}

func (c *ClassReconciler) Setup(m ctrl.Manager) error {
	if err := ctrl.NewControllerManagedBy(m).For(&api.ProviderClass{}).Named("providerclass").Complete(reconcile.Func(c.reconcileProvider)); err != nil {
		return err
	}
	return ctrl.NewControllerManagedBy(m).For(&api.ForgeClass{}).Named("forgeclass").Complete(reconcile.Func(c.reconcileForge))
}

// ProviderAccepted validates one ProviderClass, including that it is not
// one of several defaults for its provider.
func (c *ClassReconciler) ProviderAccepted(ctx context.Context, pc *api.ProviderClass) error {
	if _, err := providerbroker.ValidateClass(pc, c.ProviderLeases); err != nil {
		return err
	}
	if pc.Annotations[api.DefaultClassAnnotation] != "true" {
		return nil
	}
	var list api.ProviderClassList
	if err := c.List(ctx, &list); err != nil {
		return fmt.Errorf("provider classes unavailable")
	}
	for _, other := range list.Items {
		if other.Name != pc.Name && other.Spec.Provider == pc.Spec.Provider && other.Annotations[api.DefaultClassAnnotation] == "true" && other.DeletionTimestamp.IsZero() {
			return fmt.Errorf("ProviderClass %q is also the default for %q; bare grants fail closed until one default remains", other.Name, pc.Spec.Provider)
		}
	}
	return nil
}

func (c *ClassReconciler) reconcileProvider(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	var pc api.ProviderClass
	if err := c.Get(ctx, req.NamespacedName, &pc); err != nil {
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}
	before := pc.DeepCopyObject().(*api.ProviderClass)
	setAccepted(&pc.Status.Conditions, pc.Generation, c.ProviderAccepted(ctx, &pc))
	pc.Status.ObservedGeneration = pc.Generation
	if reflect.DeepEqual(before.Status, pc.Status) {
		return reconcile.Result{RequeueAfter: classRecheck}, nil
	}
	return reconcile.Result{RequeueAfter: classRecheck}, c.Status().Patch(ctx, &pc, client.MergeFrom(before))
}

func (c *ClassReconciler) reconcileForge(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	var fc api.ForgeClass
	if err := c.Get(ctx, req.NamespacedName, &fc); err != nil {
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}
	before := fc.DeepCopyObject().(*api.ForgeClass)
	var err error
	if c.Forges == nil {
		err = fmt.Errorf("this controller resolves no forge classes")
	} else {
		// Resolution reads the credential and builds the client; it never
		// calls the forge and never reports the credential.
		_, err = c.Forges.Resolve(ctx, fc.Name)
	}
	setAccepted(&fc.Status.Conditions, fc.Generation, err)
	fc.Status.ObservedGeneration = fc.Generation
	if reflect.DeepEqual(before.Status, fc.Status) {
		return reconcile.Result{RequeueAfter: classRecheck}, nil
	}
	return reconcile.Result{RequeueAfter: classRecheck}, c.Status().Patch(ctx, &fc, client.MergeFrom(before))
}

func setAccepted(conditions *[]metav1.Condition, generation int64, err error) {
	condition := metav1.Condition{Type: AcceptedCondition, Status: metav1.ConditionTrue, Reason: "Accepted", Message: "Usable by this controller", ObservedGeneration: generation}
	if err != nil {
		condition.Status, condition.Reason, condition.Message = metav1.ConditionFalse, "Invalid", err.Error()
	}
	meta.SetStatusCondition(conditions, condition)
}
