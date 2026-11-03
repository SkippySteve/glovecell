package auditlog_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/auditlog"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/store"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/store/storetest"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestMain(m *testing.M) { os.Exit(storetest.Main(m)) }

var ctx = context.Background()

const (
	ns  = "control"
	uid = "11111111-2222-3333-4444-555555555555"
)

var base = time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)

func legacy(name, action string, at time.Time, withUID bool) *corev1.ConfigMap {
	data := map[string]string{"workspace": "demo", "generation": "2", "action": action, "actor": "operator", "time": at.Format(time.RFC3339), "message": "legacy " + action}
	if withUID {
		data["workspaceUID"] = uid
	}
	immutable := true
	return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: map[string]string{api.OwnerLabel: uid}}, Immutable: &immutable, Data: data}
}

func fakeClient(objs ...client.Object) client.Client {
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
}

func legacyObjects() []client.Object {
	return []client.Object{
		legacy("audit-1-grant-requested", "grant-requested", base, true),
		legacy("audit-1-access-granted", "access-granted", base.Add(time.Minute), true),
		// Older records may lack workspaceUID in data; the owner label has it.
		legacy("audit-1-phase-ready", "phase-ready", base.Add(2*time.Minute), false),
		// Not an audit record.
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "registration-x", Namespace: ns, Labels: map[string]string{api.OwnerLabel: uid}}, Data: map[string]string{"action": "x"}},
	}
}

func backends(t *testing.T, fn func(t *testing.T, s store.Store)) {
	t.Run("memory", func(t *testing.T) { fn(t, store.NewMemory()) })
	t.Run("postgres", func(t *testing.T) { fn(t, storetest.New(t)) })
}

func record(action string, at time.Time) map[string]string {
	return map[string]string{"workspace": "demo", "workspaceUID": uid, "generation": "3", "action": action, "actor": "operator", "time": at.Format(time.RFC3339)}
}

func actions(items []map[string]string) string {
	var out []string
	for _, it := range items {
		out = append(out, it["action"])
	}
	return strings.Join(out, ",")
}

func TestWithoutDatabaseWritesAndReadsConfigMaps(t *testing.T) {
	c := fakeClient(legacyObjects()...)
	l := &auditlog.Log{Client: c, Namespace: ns}
	if err := l.Record(ctx, uid, "audit-1-lan-host-added", record("lan-host-added", base.Add(time.Hour)), "console"); err != nil {
		t.Fatal(err)
	}
	// Same record again: a no-op, as the controller's retries need.
	if err := l.Record(ctx, uid, "audit-1-lan-host-added", record("lan-host-added", base.Add(2*time.Hour)), "console"); err != nil {
		t.Fatalf("idempotent record: %v", err)
	}
	cm := &corev1.ConfigMap{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: "audit-1-lan-host-added"}, cm); err != nil || cm.Immutable == nil || !*cm.Immutable || cm.Labels[auditlog.SpoolLabel] != "" {
		t.Fatalf("legacy record: %+v %v", cm, err)
	}
	if err := l.Record(ctx, "another-uid", "audit-1-lan-host-added", record("lan-host-added", base), "console"); err == nil {
		t.Fatal("record adopted another workspace's ConfigMap")
	}
	items, source, err := l.History(ctx, uid, store.HistoryFilter{})
	if err != nil || source != auditlog.SourceConfigMaps || actions(items) != "lan-host-added,phase-ready,access-granted,grant-requested" {
		t.Fatalf("history %s %q %v", source, actions(items), err)
	}
	if ok, err := l.Has(ctx, uid, "access-granted"); err != nil || !ok {
		t.Fatalf("Has: %v %v", ok, err)
	}
}

func TestLegacyImportRunsOnceAndKeepsOriginalTimes(t *testing.T) {
	backends(t, func(t *testing.T, s store.Store) {
		c := fakeClient(legacyObjects()...)
		l := &auditlog.Log{Client: c, Namespace: ns, Store: s}
		// Before the import, reads merge the ConfigMaps in, and a write for
		// an existing ConfigMap key is not duplicated with a new time.
		if err := l.Record(ctx, uid, "audit-1-access-granted", record("access-granted", base.Add(time.Hour)), "controller"); err != nil {
			t.Fatal(err)
		}
		items, source, err := l.History(ctx, uid, store.HistoryFilter{})
		if err != nil || source != auditlog.SourceMerged || actions(items) != "phase-ready,access-granted,grant-requested" {
			t.Fatalf("pre-import history %s %q %v", source, actions(items), err)
		}
		n, err := l.ImportLegacy(ctx)
		if err != nil || n != 3 || !l.Imported() {
			t.Fatalf("import: %d %v", n, err)
		}
		if n, err := l.ImportLegacy(ctx); err != nil || n != 0 {
			t.Fatalf("second import: %d %v", n, err)
		}
		// A restarted controller sees the import as done.
		restarted := &auditlog.Log{Client: c, Namespace: ns, Store: s}
		if n, err := restarted.ImportLegacy(ctx); err != nil || n != 0 || !restarted.Imported() {
			t.Fatalf("import after restart: %d %v", n, err)
		}
		events, err := s.Events(ctx, uid, store.HistoryFilter{})
		if err != nil || len(events) != 3 {
			t.Fatalf("imported events: %+v %v", events, err)
		}
		for _, e := range events {
			if e.Source != "import" || e.Action == "access-granted" && !e.Time.Equal(base.Add(time.Minute)) || e.Generation != 2 || e.Message == "" {
				t.Fatalf("imported event: %+v", e)
			}
		}
		// New records now go only to the database.
		if err := restarted.Record(ctx, uid, "audit-1-phase-paused", record("phase-paused", base.Add(time.Hour)), "controller"); err != nil {
			t.Fatal(err)
		}
		if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: "audit-1-phase-paused"}, &corev1.ConfigMap{}); err == nil {
			t.Fatal("record written to a ConfigMap with the database up")
		}
		items, source, err = restarted.History(ctx, uid, store.HistoryFilter{})
		if err != nil || source != auditlog.SourceDatabase || actions(items) != "phase-paused,phase-ready,access-granted,grant-requested" || items[0]["generation"] != "3" {
			t.Fatalf("post-import history %s %q %v", source, actions(items), err)
		}
		if items, _, _ := restarted.History(ctx, uid, store.HistoryFilter{ActionPrefix: "phase-", Limit: 1}); actions(items) != "phase-paused" {
			t.Fatalf("filtered history %q", actions(items))
		}
	})
}

// flaky fails every AppendEvent after the first n.
type flaky struct {
	store.Store
	n int
}

func (f *flaky) AppendEvent(ctx context.Context, e store.Event) (bool, error) {
	if f.n <= 0 {
		return false, fmt.Errorf("%w: connection reset", store.ErrUnavailable)
	}
	f.n--
	return f.Store.AppendEvent(ctx, e)
}

func TestInterruptedImportResumesWithoutDuplicates(t *testing.T) {
	backends(t, func(t *testing.T, s store.Store) {
		c := fakeClient(legacyObjects()...)
		l := &auditlog.Log{Client: c, Namespace: ns, Store: &flaky{Store: s, n: 1}}
		if _, err := l.ImportLegacy(ctx); !errors.Is(err, store.ErrUnavailable) || l.Imported() {
			t.Fatalf("interrupted import: %v (imported %v)", err, l.Imported())
		}
		if done, _ := s.ImportDone(ctx, auditlog.LegacyImport); done {
			t.Fatal("interrupted import marked done")
		}
		// Reads still include every ConfigMap.
		if items, _, err := l.History(ctx, uid, store.HistoryFilter{}); err != nil || len(items) != 3 {
			t.Fatalf("history during interrupted import: %d %v", len(items), err)
		}
		resumed := &auditlog.Log{Client: c, Namespace: ns, Store: s}
		if _, err := resumed.ImportLegacy(ctx); err != nil || !resumed.Imported() {
			t.Fatalf("resumed import: %v", err)
		}
		if events, _ := s.Events(ctx, uid, store.HistoryFilter{}); len(events) != 3 {
			t.Fatalf("resumed import has %d events, want 3", len(events))
		}
	})
}

func TestDatabaseDownSpoolsAndDrains(t *testing.T) {
	m := store.NewMemory()
	c := fakeClient(legacyObjects()...)
	l := &auditlog.Log{Client: c, Namespace: ns, Store: m}
	if _, err := l.ImportLegacy(ctx); err != nil {
		t.Fatal(err)
	}
	m.SetDown(true)
	// Lifecycle work never waits for the database: the record is spooled.
	if err := l.Record(ctx, uid, "audit-1-access-revoked", record("access-revoked", base.Add(time.Hour)), "controller"); err != nil {
		t.Fatalf("record while down: %v", err)
	}
	cm := &corev1.ConfigMap{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: "audit-1-access-revoked"}, cm); err != nil || cm.Labels[auditlog.SpoolLabel] != "true" {
		t.Fatalf("spool ConfigMap: %+v %v", cm.Labels, err)
	}
	// Safety checks still find ConfigMap records, and fail rather than
	// answer "no" when only the database could know.
	if ok, err := l.Has(ctx, uid, "access-revoked"); err != nil || !ok {
		t.Fatalf("Has spooled: %v %v", ok, err)
	}
	if _, err := l.Has(ctx, uid, "orchestrator-bound"); !errors.Is(err, store.ErrUnavailable) {
		t.Fatalf("Has with database down answered: %v", err)
	}
	items, source, err := l.History(ctx, uid, store.HistoryFilter{})
	if err != nil || source != auditlog.SourceFallback || !strings.HasPrefix(actions(items), "access-revoked") {
		t.Fatalf("fallback history %s %q %v", source, actions(items), err)
	}
	if _, err := l.DrainSpool(ctx); !errors.Is(err, store.ErrUnavailable) {
		t.Fatalf("drain while down: %v", err)
	}
	if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: "audit-1-access-revoked"}, cm); err != nil {
		t.Fatal("spool deleted before the record was stored")
	}
	m.SetDown(false)
	if n, err := l.DrainSpool(ctx); err != nil || n != 1 {
		t.Fatalf("drain: %d %v", n, err)
	}
	if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: "audit-1-access-revoked"}, cm); err == nil {
		t.Fatal("spool ConfigMap kept after drain")
	}
	events, _ := m.Events(ctx, uid, store.HistoryFilter{ActionPrefix: "access-revoked"})
	if len(events) != 1 || events[0].Source != "spool" || !events[0].Time.Equal(base.Add(time.Hour)) {
		t.Fatalf("drained event: %+v", events)
	}
	if ok, err := l.Has(ctx, uid, "access-revoked"); err != nil || !ok {
		t.Fatalf("Has after drain: %v %v", ok, err)
	}
	// Legacy ConfigMaps are never deleted by the drain.
	if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: "audit-1-access-granted"}, cm); err != nil {
		t.Fatal("legacy ConfigMap deleted")
	}
	if items, source, _ := l.History(ctx, uid, store.HistoryFilter{}); source != auditlog.SourceDatabase || len(items) != 4 {
		t.Fatalf("history after drain %s %q", source, actions(items))
	}
}
