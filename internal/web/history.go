package web

import (
	"context"
	"net/http"
	"strconv"
	"time"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/auditlog"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/store"
	"k8s.io/apimachinery/pkg/types"
)

// HistoryStore reads one project's access history, newest first. Each entry
// has at least "time" (RFC 3339) and "action"; "message" is optional.
//
// The history is the durable operational record: PostgreSQL when a database
// is configured (D13 item 2), with audit ConfigMaps merged in until they are
// imported, and read alone as a fallback while the database is down.
// Kubernetes Events are not a source: kube-apiserver expires them
// (--event-ttl, 1h by default). status.audit is deprecated and not read.
type HistoryStore interface {
	History(ctx context.Context, w *api.ProjectWorkspace, f store.HistoryFilter) ([]map[string]string, auditlog.Source, error)
}

// logHistory reads history from the audit log.
type logHistory struct{ log *auditlog.Log }

func (h logHistory) History(ctx context.Context, w *api.ProjectWorkspace, f store.HistoryFilter) ([]map[string]string, auditlog.Source, error) {
	return h.log.History(ctx, string(w.UID), f)
}

func (s *Server) historyStore() HistoryStore {
	if s.History != nil {
		return s.History
	}
	return logHistory{s.auditLog()}
}

// auditLog is where console actions are recorded: s.Audit, or the audit
// ConfigMaps in Namespace when no database is configured.
func (s *Server) auditLog() *auditlog.Log {
	if s.Audit != nil {
		return s.Audit
	}
	return &auditlog.Log{Client: s.Client, Namespace: s.Namespace}
}

// historyFilter reads ?action=<prefix>&actor=&since=&before=&limit= (times
// in RFC 3339; limit at most store.MaxPageSize).
func historyFilter(r *http.Request) (store.HistoryFilter, bool) {
	q := r.URL.Query()
	f := store.HistoryFilter{ActionPrefix: q.Get("action"), Actor: q.Get("actor")}
	for key, dst := range map[string]*time.Time{"since": &f.Since, "before": &f.Before} {
		if v := q.Get(key); v != "" {
			t, err := time.Parse(time.RFC3339Nano, v)
			if err != nil {
				return f, false
			}
			*dst = t
		}
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > store.MaxPageSize {
			return f, false
		}
		f.Limit = n
	}
	return f, len(f.ActionPrefix) <= 128 && len(f.Actor) <= 512
}

// audit serves GET /api/v1/workspaces/{name}/audit, the console's history.
// X-History-Source says where it came from; configmaps-fallback means the
// database was unavailable and newer records may be missing.
func (s *Server) audit(w http.ResponseWriter, r *http.Request) {
	obj := &api.ProjectWorkspace{}
	if e := s.Client.Get(r.Context(), types.NamespacedName{Namespace: s.Namespace, Name: r.PathValue("name")}, obj); e != nil {
		http.Error(w, "project unavailable", 404)
		return
	}
	f, ok := historyFilter(r)
	if !ok {
		http.Error(w, "invalid history filter", 400)
		return
	}
	items, source, e := s.historyStore().History(r.Context(), obj, f)
	if e != nil {
		http.Error(w, "audit unavailable", 503)
		return
	}
	if source != "" {
		w.Header().Set("X-History-Source", string(source))
	}
	writeJSON(w, items)
}
