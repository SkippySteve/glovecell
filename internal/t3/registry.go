package t3

import (
	"context"
	"errors"
	"strings"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// The ledger is in the control namespace. It must survive a worker, a partially
// completed POST, or controller replacement. It is never mounted in a worker.
type Registry struct {
	Client    client.Client
	Namespace string
	Cloud     *Cloud
}

func ledgerName(w *api.ProjectWorkspace) string {
	return "t3-connect-" + strings.ReplaceAll(string(w.UID), "-", "")
}
func (r *Registry) Ledger(ctx context.Context, w *api.ProjectWorkspace) (*corev1.Secret, error) {
	s := &corev1.Secret{}
	e := r.Client.Get(ctx, client.ObjectKey{Namespace: r.Namespace, Name: ledgerName(w)}, s)
	if e != nil {
		return nil, e
	}
	if s.Labels[api.OwnerLabel] != string(w.UID) || string(s.Data["workspace"]) != w.Name || string(s.Data["environment-id"]) != string(w.UID) || string(s.Data["account-id"]) == "" {
		return nil, errors.New("T3 registration ledger ownership mismatch")
	}
	return s, nil
}
func (r *Registry) Intent(ctx context.Context, w *api.ProjectWorkspace, id, key, accountID string) (*corev1.Secret, error) {
	if w.UID == "" || id != string(w.UID) || key == "" || accountID == "" {
		return nil, errors.New("T3 registration identity required")
	}
	s, e := r.Ledger(ctx, w)
	if apierrors.IsNotFound(e) {
		s = &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: r.Namespace, Name: ledgerName(w), Labels: map[string]string{api.OwnerLabel: string(w.UID)}}, Data: map[string][]byte{"workspace": []byte(w.Name), "account-id": []byte(accountID), "environment-id": []byte(id), "public-key": []byte(key), "state": []byte("linking")}}
		if e = r.Client.Create(ctx, s); e != nil {
			return nil, e
		}
	} else if e != nil {
		return nil, e
	}
	if string(s.Data["environment-id"]) != id || string(s.Data["public-key"]) != key || string(s.Data["account-id"]) != accountID {
		return nil, errors.New("T3 project identity changed; refusing registration")
	}
	// Independently retain the intent marker. Missing ledgers cannot silently
	// bypass revocation during removal, even after a failed link request.
	audit := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: r.Namespace, Name: ledgerName(w) + "-intent", Labels: map[string]string{api.OwnerLabel: string(w.UID)}}, Data: map[string]string{"action": "t3-link-requested", "environment-id": id, "account-id": accountID}}
	if e = r.Client.Create(ctx, audit); e != nil && !apierrors.IsAlreadyExists(e) {
		return nil, e
	}
	if apierrors.IsAlreadyExists(e) {
		current := &corev1.ConfigMap{}
		if e = r.Client.Get(ctx, client.ObjectKeyFromObject(audit), current); e != nil {
			return nil, e
		}
		if current.Labels[api.OwnerLabel] != string(w.UID) || current.Data["environment-id"] != id {
			return nil, errors.New("T3 registration audit ownership mismatch")
		}
	}
	if string(s.Data["state"]) != "linking" {
		old := s.DeepCopy()
		s.Data["state"] = []byte("linking")
		if e = r.Client.Patch(ctx, s, client.MergeFrom(old)); e != nil {
			return nil, e
		}
	}
	return s, nil
}
func (r *Registry) Linked(ctx context.Context, s *corev1.Secret, link Link) error {
	if string(s.Data["environment-id"]) != link.EnvironmentID {
		return errors.New("T3 relay identity changed")
	}
	old := s.DeepCopy()
	s.Data["state"] = []byte("linked")
	s.Data["endpoint"] = []byte(link.Endpoint.HTTP)
	return r.Client.Patch(ctx, s, client.MergeFrom(old))
}
func (r *Registry) Remove(ctx context.Context, w *api.ProjectWorkspace) error {
	s, e := r.Ledger(ctx, w)
	if apierrors.IsNotFound(e) {
		// Read the exact marker, rather than treating a failed list as empty history.
		record := &corev1.ConfigMap{}
		e = r.Client.Get(ctx, client.ObjectKey{Namespace: r.Namespace, Name: ledgerName(w) + "-intent"}, record)
		if apierrors.IsNotFound(e) {
			return nil
		}
		if e != nil {
			return e
		}
		return errors.New("T3 registration ledger missing; revocation recovery required")
	}
	if e != nil {
		return e
	}
	// Verify absence on every removal retry, including after a previous successful
	// revoke. A re-registered environment must not escape the finalizer.
	snapshot, _, e := r.Cloud.ForAccount(ctx, string(s.Data["account-id"]))
	if e != nil {
		return e
	}
	if e = snapshot.Revoke(ctx, string(s.Data["environment-id"])); e != nil {
		return e
	}
	old := s.DeepCopy()
	s.Data["state"] = []byte("revoked")
	delete(s.Data, "endpoint")
	return r.Client.Patch(ctx, s, client.MergeFrom(old))
}
