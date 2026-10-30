package web

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// HistoryStore reads one project's access history, newest first. Each entry
// has at least "time" (RFC 3339) and "action"; "message" is optional.
//
// The history is the durable operational record. Kubernetes Events are not a
// source: kube-apiserver expires them (--event-ttl, 1h by default), so they
// only serve `kubectl describe` recency. status.audit is deprecated and not
// read either.
//
// TODO(D13 item 2): a PostgreSQL implementation becomes the durable history
// store in a separate pull request; ConfigMapHistory stays until it lands.
type HistoryStore interface {
	History(ctx context.Context, w *api.ProjectWorkspace) ([]map[string]string, error)
}

// ConfigMapHistory reads the immutable audit-* ConfigMaps the controller and
// console write in the control namespace, labelled with the workspace UID.
type ConfigMapHistory struct {
	Reader    client.Reader
	Namespace string
}

func (h ConfigMapHistory) History(ctx context.Context, w *api.ProjectWorkspace) ([]map[string]string, error) {
	var list corev1.ConfigMapList
	if e := h.Reader.List(ctx, &list, client.InNamespace(h.Namespace), client.MatchingLabels{api.OwnerLabel: string(w.UID)}); e != nil {
		return nil, e
	}
	type entry struct {
		at   time.Time
		data map[string]string
	}
	var entries []entry
	for _, cm := range list.Items {
		if strings.HasPrefix(cm.Name, "audit-") {
			at, _ := time.Parse(time.RFC3339Nano, cm.Data["time"])
			entries = append(entries, entry{at, cm.Data})
		}
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].at.After(entries[j].at) })
	items := make([]map[string]string, 0, len(entries))
	for _, e := range entries {
		items = append(items, e.data)
	}
	return items, nil
}

func (s *Server) historyStore() HistoryStore {
	if s.History != nil {
		return s.History
	}
	return ConfigMapHistory{Reader: s.Client, Namespace: s.Namespace}
}

// audit serves GET /api/v1/workspaces/{name}/audit, the console's history.
func (s *Server) audit(w http.ResponseWriter, r *http.Request) {
	obj := &api.ProjectWorkspace{}
	if e := s.Client.Get(r.Context(), types.NamespacedName{Namespace: s.Namespace, Name: r.PathValue("name")}, obj); e != nil {
		http.Error(w, "project unavailable", 404)
		return
	}
	items, e := s.historyStore().History(r.Context(), obj)
	if e != nil {
		http.Error(w, "audit unavailable", 503)
		return
	}
	writeJSON(w, items)
}
