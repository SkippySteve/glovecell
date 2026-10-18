package controller

import (
	"context"
	"fmt"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/registration"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
)

// clientsPending reports whether external desktop clients still need revoke
// before namespace deletion. Missing ledger means no registrar ran.
func (r *Reconciler) clientsPending(ctx context.Context, w *api.ProjectWorkspace) error {
	cm := &corev1.ConfigMap{}
	name := registration.ConfigMapName(string(w.UID))
	err := r.Get(ctx, types.NamespacedName{Namespace: r.Config.Namespace, Name: name}, cm)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if cm.Labels[api.OwnerLabel] != string(w.UID) {
		return fmt.Errorf("client ledger ownership mismatch")
	}
	ledger, err := registration.DecodeConfigMapData(cm.Data)
	if err != nil {
		return err
	}
	blocked, reason := ledger.RemovalBlocked()
	if blocked {
		if reason != nil {
			return reason
		}
		return fmt.Errorf("client registrations still active")
	}
	return nil
}
