package controller

import (
	"context"
	"errors"
	"strings"
	"testing"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/auditlog"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/store"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func withDatabase(t *testing.T, r *Reconciler) *store.Memory {
	t.Helper()
	m := store.NewMemory()
	r.AuditLog = &auditlog.Log{Client: r.Client, Namespace: r.Config.Namespace, Store: m}
	if _, err := r.AuditLog.ImportLegacy(context.Background()); err != nil {
		t.Fatal(err)
	}
	return m
}

func auditConfigMaps(t *testing.T, r *Reconciler, w *api.ProjectWorkspace) []corev1.ConfigMap {
	t.Helper()
	var list corev1.ConfigMapList
	if err := r.List(context.Background(), &list, client.InNamespace("control"), client.MatchingLabels{api.OwnerLabel: string(w.UID)}); err != nil {
		t.Fatal(err)
	}
	var out []corev1.ConfigMap
	for _, cm := range list.Items {
		if strings.HasPrefix(cm.Name, auditlog.Prefix) {
			out = append(out, cm)
		}
	}
	return out
}

// With a database, lifecycle records go there, not to ConfigMaps, and the
// missing-ledger safety check still finds the grant.
func TestDatabaseHistoryKeepsLedgerSafety(t *testing.T) {
	ctx := context.Background()
	r, w, _ := fixture(t)
	m := withDatabase(t, r)
	if e := r.ensure(ctx, w); e != nil {
		t.Fatal(e)
	}
	if cms := auditConfigMaps(t, r, w); len(cms) != 0 {
		t.Fatalf("audit ConfigMaps written with the database up: %d", len(cms))
	}
	if ok, _ := m.HasAction(ctx, string(w.UID), "access-granted"); !ok {
		t.Fatal("grant not recorded in the database")
	}
	s := &corev1.Secret{}
	if e := r.Get(ctx, types.NamespacedName{Namespace: "control", Name: identity(w)}, s); e != nil {
		t.Fatal(e)
	}
	if e := r.Delete(ctx, s); e != nil {
		t.Fatal(e)
	}
	w.Generation = 3
	if e := r.remove(ctx, w); e == nil || !strings.Contains(e.Error(), "credential ledger missing") {
		t.Fatalf("missing ledger after a database-recorded grant: %v", e)
	}
	var ns corev1.Namespace
	if e := r.Get(ctx, types.NamespacedName{Name: namespace(w)}, &ns); e != nil {
		t.Fatal("storage deleted without verified revocation")
	}
}

// With the database down, lifecycle work continues (records are spooled),
// but a safety check that only the database could answer fails closed.
func TestDatabaseDownSpoolsLifecycleAndBlocksUnverifiableRemoval(t *testing.T) {
	ctx := context.Background()
	r, w, _ := fixture(t)
	m := withDatabase(t, r)
	if e := r.ensure(ctx, w); e != nil {
		t.Fatal(e)
	}
	m.SetDown(true)
	// Reconciling again writes its records to the spool.
	if e := r.ensure(ctx, w); e != nil {
		t.Fatalf("lifecycle blocked by the database outage: %v", e)
	}
	spooled := auditConfigMaps(t, r, w)
	if len(spooled) == 0 {
		t.Fatal("nothing spooled during the outage")
	}
	for _, cm := range spooled {
		if cm.Labels[auditlog.SpoolLabel] != "true" {
			t.Fatalf("unlabelled audit ConfigMap %s", cm.Name)
		}
	}
	// The identity ledger disappears; the spooled records say a grant was
	// requested, so removal is refused.
	s := &corev1.Secret{}
	_ = r.Get(ctx, types.NamespacedName{Namespace: "control", Name: identity(w)}, s)
	_ = r.Delete(ctx, s)
	if e := r.remove(ctx, w); e == nil {
		t.Fatal("removal completed without a ledger")
	}
	// Drain the spool, then clear the ConfigMaps: only the database knows,
	// and while it is down the check fails rather than answering "no".
	m.SetDown(false)
	if _, err := r.AuditLog.DrainSpool(ctx); err != nil {
		t.Fatal(err)
	}
	if left := auditConfigMaps(t, r, w); len(left) != 0 {
		t.Fatalf("spool not drained: %d", len(left))
	}
	m.SetDown(true)
	e := r.remove(ctx, w)
	if e == nil || !errors.Is(e, store.ErrUnavailable) && !strings.Contains(e.Error(), "unavailable") {
		t.Fatalf("removal with an unreadable ledger history: %v", e)
	}
	var ns corev1.Namespace
	if e := r.Get(ctx, types.NamespacedName{Name: namespace(w)}, &ns); e != nil {
		t.Fatal("storage deleted while the history was unreadable")
	}
	m.SetDown(false)
	if e := r.remove(ctx, w); e == nil || !strings.Contains(e.Error(), "credential ledger missing") {
		t.Fatalf("after recovery: %v", e)
	}
}
