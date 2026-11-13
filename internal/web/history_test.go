package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/auditlog"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/store"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func historyRequest(t *testing.T, s *Server) *httptest.ResponseRecorder {
	t.Helper()
	s.Password, s.Origin, s.AllowBasic, s.Namespace, s.Owner, s.Host = strings.Repeat("h", 32), "https://projects.example.com", true, "control", "steve", host{}
	h, err := s.Handler()
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/project/audit", nil)
	req.Header.Set("Authorization", "Bearer "+s.Password)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func historyWorkspace(uid types.UID, legacy ...api.AuditEvent) *api.ProjectWorkspace {
	return &api.ProjectWorkspace{ObjectMeta: metav1.ObjectMeta{Name: "project", Namespace: "control", UID: uid}, Spec: api.WorkspaceSpec{Repository: "steve/project", DesiredState: "Running"}, Status: api.WorkspaceStatus{Audit: legacy}}
}

func TestHistoryReadsAuditRecordsNewestFirst(t *testing.T) {
	uid := types.UID("12345678-1234-1234-1234-123456789012")
	base := time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC)
	record := func(name, action string, at time.Time, owner types.UID) *corev1.ConfigMap {
		return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "control", Labels: map[string]string{api.OwnerLabel: string(owner)}}, Data: map[string]string{"action": action, "time": at.Format(time.RFC3339)}}
	}
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = api.AddToScheme(scheme)
	// The deprecated status.audit list is not a history source.
	w := historyWorkspace(uid, api.AuditEvent{Time: metav1.NewTime(base), Action: "LegacyOnly", Generation: 1})
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(w,
		record("audit-1-grant-requested", "grant-requested", base, uid),
		record("audit-1-phase-ready", "phase-ready", base.Add(2*time.Minute), uid),
		record("audit-1-access-granted", "access-granted", base.Add(time.Minute), uid),
		record("audit-0-cleanup-verified", "cleanup-verified", base.Add(time.Hour), "00000000-0000-0000-0000-000000000000"),
		record("not-audit", "other", base.Add(time.Hour), uid),
	).Build()
	rec := historyRequest(t, &Server{Client: c})
	if rec.Code != 200 {
		t.Fatalf("history: %d %s", rec.Code, rec.Body.String())
	}
	var items []map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, it := range items {
		got = append(got, it["action"])
	}
	if strings.Join(got, ",") != "phase-ready,access-granted,grant-requested" {
		t.Fatalf("history = %v", got)
	}
}

type stubHistory struct {
	items []map[string]string
	err   error
	asked types.UID
}

func (s *stubHistory) History(_ context.Context, w *api.ProjectWorkspace, _ store.HistoryFilter) ([]map[string]string, auditlog.Source, error) {
	s.asked = w.UID
	return s.items, auditlog.SourceDatabase, s.err
}

func TestHistoryUsesConfiguredStore(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = api.AddToScheme(scheme)
	uid := types.UID("12345678-1234-1234-1234-123456789012")
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(historyWorkspace(uid)).Build()
	stub := &stubHistory{items: []map[string]string{{"time": "2026-10-29T10:00:00Z", "action": "from-store"}}}
	rec := historyRequest(t, &Server{Client: c, History: stub})
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "from-store") || stub.asked != uid || rec.Header().Get("X-History-Source") != "database" {
		t.Fatalf("history: %d %s (asked %q)", rec.Code, rec.Body.String(), stub.asked)
	}
	stub.err = errors.New("store down")
	if rec = historyRequest(t, &Server{Client: c, History: stub}); rec.Code != 503 {
		t.Fatalf("store failure served as %d", rec.Code)
	}
}
