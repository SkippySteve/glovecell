// Package auditlog is the durable history of each workspace: lifecycle
// steps, policy changes, grants and console actions.
//
// With a database (D13 item 2) records go to PostgreSQL. Without one, or
// while the database is down, they are written as immutable audit-*
// ConfigMaps in the control namespace, as before. Records written during an
// outage carry the spool label and are moved into the database (then
// deleted) when it returns, so lifecycle work such as pausing or revoking
// access never waits for the database.
//
// On first start with a database, the existing audit ConfigMaps are imported
// once. The import is idempotent (each record keeps its ConfigMap name as
// its key), so an interrupted import simply runs again. Until it completes,
// history reads merge the ConfigMaps in.
package auditlog

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/store"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// Prefix starts every audit ConfigMap name.
	Prefix = "audit-"
	// SpoolLabel marks a ConfigMap written while the database was down.
	SpoolLabel = "projects.agentwork.dev/audit-spool"
	// LegacyImport names the one-time import of pre-database ConfigMaps.
	LegacyImport = "audit-configmaps-v1"
)

// Fields every record has; anything else is a detail.
var standard = []string{"workspace", "workspaceUID", "generation", "action", "actor", "time", "message"}

// Store is what the log needs from the database.
type Store interface {
	store.HistoryStore
	store.ImportStore
}

// Log writes and reads workspace history. The zero Store means ConfigMaps
// only (no database configured).
type Log struct {
	Client    client.Client
	Namespace string
	Store     Store

	imported atomic.Bool
}

// Source describes where a History read came from.
type Source string

const (
	SourceConfigMaps Source = "configmaps"
	SourceDatabase   Source = "database"
	// SourceMerged is the database plus ConfigMaps not yet imported or
	// drained.
	SourceMerged Source = "database+configmaps"
	// SourceFallback means the database was unavailable, so only the
	// ConfigMaps were read and newer records may be missing.
	SourceFallback Source = "configmaps-fallback"
)

// Imported reports whether the legacy import has completed.
func (l *Log) Imported() bool { return l.imported.Load() }

// Record writes one history record. key is the record's stable name
// (audit-<uid>-...); writing the same key again is a no-op. data uses the
// audit ConfigMap fields (workspace, workspaceUID, generation, action, actor,
// time, message and details).
func (l *Log) Record(ctx context.Context, uid, key string, data map[string]string, source string) error {
	if uid == "" || !strings.HasPrefix(key, Prefix) || len(key) > 253 {
		return errors.New("audit record needs a workspace UID and an audit- name")
	}
	if l.Store == nil {
		return l.writeConfigMap(ctx, uid, key, data, false)
	}
	if !l.imported.Load() {
		// Not imported yet: an existing ConfigMap with this name is the
		// record (with its original time); never write a second one.
		cm := &corev1.ConfigMap{}
		err := l.Client.Get(ctx, types.NamespacedName{Namespace: l.Namespace, Name: key}, cm)
		if err == nil {
			return ownedBy(cm, uid)
		}
		if !apierrors.IsNotFound(err) {
			return err
		}
	}
	e, err := eventFrom(key, uid, data, source, time.Now().UTC())
	if err != nil {
		return err
	}
	_, err = l.Store.AppendEvent(ctx, e)
	if err == nil {
		return nil
	}
	if !errors.Is(err, store.ErrUnavailable) {
		return err
	}
	slog.WarnContext(ctx, "database unavailable; audit record spooled to a ConfigMap", "record", key, "workspace_uid", uid, "action", data["action"])
	return l.writeConfigMap(ctx, uid, key, data, true)
}

func ownedBy(cm *corev1.ConfigMap, uid string) error {
	if cm.Labels[api.OwnerLabel] != uid {
		return errors.New("resource ownership mismatch; refusing adoption or deletion")
	}
	return nil
}

func (l *Log) writeConfigMap(ctx context.Context, uid, key string, data map[string]string, spool bool) error {
	existing := &corev1.ConfigMap{}
	err := l.Client.Get(ctx, types.NamespacedName{Namespace: l.Namespace, Name: key}, existing)
	if err == nil {
		return ownedBy(existing, uid)
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	labels := map[string]string{api.OwnerLabel: uid}
	if spool {
		labels[SpoolLabel] = "true"
	}
	immutable := true
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: key, Namespace: l.Namespace, Labels: labels}, Immutable: &immutable, Data: data}
	err = l.Client.Create(ctx, cm)
	if apierrors.IsAlreadyExists(err) {
		if err = l.Client.Get(ctx, types.NamespacedName{Namespace: l.Namespace, Name: key}, existing); err == nil {
			return ownedBy(existing, uid)
		}
	}
	return err
}

// EventFromData converts audit ConfigMap fields to a history Event. A
// missing or unreadable time becomes fallback.
func EventFromData(key string, data map[string]string, source string, fallback time.Time) (store.Event, error) {
	return eventFrom(key, "", data, source, fallback)
}

// EventFromConfigMap converts one audit ConfigMap. Older records may lack
// workspaceUID in their data; the owner label supplies it.
func EventFromConfigMap(cm *corev1.ConfigMap, source string) (store.Event, error) {
	return eventFrom(cm.Name, cm.Labels[api.OwnerLabel], cm.Data, source, cm.CreationTimestamp.UTC())
}

func eventFrom(key, owner string, data map[string]string, source string, fallback time.Time) (store.Event, error) {
	uid := data["workspaceUID"]
	if uid == "" {
		uid = owner
	}
	e := store.Event{Key: key, WorkspaceUID: uid, Workspace: data["workspace"], Action: data["action"], Actor: data["actor"], Message: data["message"], Source: source, Details: map[string]string{}}
	if e.WorkspaceUID == "" || e.Action == "" {
		return e, fmt.Errorf("audit record %s needs workspaceUID and action", key)
	}
	e.Generation, _ = strconv.ParseInt(data["generation"], 10, 64)
	at, err := time.Parse(time.RFC3339Nano, data["time"])
	if err != nil {
		at = fallback
	}
	e.Time = at.UTC()
	for k, v := range data {
		if !slices.Contains(standard, k) {
			e.Details[k] = v
		}
	}
	return e, nil
}

// EventData converts an Event back to the ConfigMap fields the console reads.
func EventData(e store.Event) map[string]string {
	out := make(map[string]string, len(e.Details)+len(standard))
	for k, v := range e.Details {
		out[k] = v
	}
	out["workspaceUID"], out["action"], out["actor"], out["time"] = e.WorkspaceUID, e.Action, e.Actor, e.Time.UTC().Format(time.RFC3339)
	if e.Workspace != "" {
		out["workspace"] = e.Workspace
	}
	if e.Generation != 0 {
		out["generation"] = strconv.FormatInt(e.Generation, 10)
	}
	if e.Message != "" {
		out["message"] = e.Message
	}
	return out
}

func (l *Log) configMaps(ctx context.Context, opts ...client.ListOption) ([]corev1.ConfigMap, error) {
	var out []corev1.ConfigMap
	opts = append(opts, client.InNamespace(l.Namespace), client.Limit(500))
	cont := ""
	for {
		var list corev1.ConfigMapList
		if err := l.Client.List(ctx, &list, append(opts, client.Continue(cont))...); err != nil {
			return nil, err
		}
		for _, cm := range list.Items {
			if strings.HasPrefix(cm.Name, Prefix) {
				out = append(out, cm)
			}
		}
		if cont = list.Continue; cont == "" {
			return out, nil
		}
	}
}

// Has reports whether the workspace has a record with one of the actions,
// in the ConfigMaps or the database. The controller uses it as safety
// evidence, so a database error is returned, never read as "no".
func (l *Log) Has(ctx context.Context, uid string, actions ...string) (bool, error) {
	cms, err := l.configMaps(ctx, client.MatchingLabels{api.OwnerLabel: uid})
	if err != nil {
		return false, err
	}
	for _, cm := range cms {
		if slices.Contains(actions, cm.Data["action"]) {
			return true, nil
		}
	}
	if l.Store == nil {
		return false, nil
	}
	return l.Store.HasAction(ctx, uid, actions...)
}

func matches(f store.HistoryFilter, e store.Event) bool {
	return (f.ActionPrefix == "" || strings.HasPrefix(e.Action, f.ActionPrefix)) && (f.Actor == "" || e.Actor == f.Actor) &&
		(f.Since.IsZero() || !e.Time.Before(f.Since)) && (f.Before.IsZero() || e.Time.Before(f.Before))
}

// History returns one workspace's records, newest first, and where they came
// from.
func (l *Log) History(ctx context.Context, uid string, f store.HistoryFilter) ([]map[string]string, Source, error) {
	var events []store.Event
	source := SourceConfigMaps
	cmOpts := []client.ListOption{client.MatchingLabels{api.OwnerLabel: uid}}
	if l.Store != nil {
		var err error
		events, err = l.Store.Events(ctx, uid, f)
		switch {
		case err == nil && l.imported.Load():
			// Only records spooled during an outage are still outside.
			source = SourceDatabase
			cmOpts = []client.ListOption{client.MatchingLabels{api.OwnerLabel: uid, SpoolLabel: "true"}}
		case err == nil:
			source = SourceMerged
		case errors.Is(err, store.ErrUnavailable):
			source = SourceFallback
		default:
			return nil, "", err
		}
	}
	cms, err := l.configMaps(ctx, cmOpts...)
	if err != nil {
		return nil, "", err
	}
	seen := map[string]bool{}
	for _, e := range events {
		seen[e.Key] = true
	}
	extra := 0
	for _, cm := range cms {
		if seen[cm.Name] {
			continue
		}
		e, err := EventFromConfigMap(&cm, "configmap")
		if err != nil || !matches(f, e) {
			continue
		}
		events = append(events, e)
		extra++
	}
	if source == SourceDatabase && extra > 0 {
		source = SourceMerged
	}
	sort.SliceStable(events, func(i, j int) bool { return events[i].Time.After(events[j].Time) })
	if n := f.Limit; n > 0 && len(events) > n {
		events = events[:n]
	}
	out := make([]map[string]string, 0, len(events))
	for _, e := range events {
		out = append(out, EventData(e))
	}
	return out, source, nil
}

// ImportLegacy copies every audit ConfigMap into the database once. It is
// idempotent and safe to interrupt: records keep their ConfigMap name as
// key, and completion is marked only after all were written.
func (l *Log) ImportLegacy(ctx context.Context) (int, error) {
	if l.Store == nil || l.imported.Load() {
		return 0, nil
	}
	done, err := l.Store.ImportDone(ctx, LegacyImport)
	if err != nil {
		return 0, err
	}
	if done {
		l.imported.Store(true)
		return 0, nil
	}
	cms, err := l.configMaps(ctx, client.HasLabels{api.OwnerLabel})
	if err != nil {
		return 0, err
	}
	inserted := 0
	for _, cm := range cms {
		source := "import"
		if cm.Labels[SpoolLabel] == "true" {
			source = "spool"
		}
		e, err := EventFromConfigMap(&cm, source)
		if err != nil {
			slog.WarnContext(ctx, "audit ConfigMap skipped by import", "configmap", cm.Name, "error", err.Error())
			continue
		}
		ok, err := l.Store.AppendEvent(ctx, e)
		if err != nil {
			return inserted, err
		}
		if ok {
			inserted++
		}
	}
	if err := l.Store.MarkImported(ctx, LegacyImport, len(cms)); err != nil {
		return inserted, err
	}
	l.imported.Store(true)
	slog.InfoContext(ctx, "audit ConfigMaps imported into the database", "configmaps", len(cms), "inserted", inserted)
	return inserted, nil
}

// DrainSpool moves records written during a database outage into the
// database and deletes each ConfigMap once its record is stored.
func (l *Log) DrainSpool(ctx context.Context) (int, error) {
	if l.Store == nil {
		return 0, nil
	}
	cms, err := l.configMaps(ctx, client.MatchingLabels{SpoolLabel: "true"})
	if err != nil {
		return 0, err
	}
	moved := 0
	for i := range cms {
		cm := &cms[i]
		e, err := EventFromConfigMap(cm, "spool")
		if err != nil || cm.Labels[api.OwnerLabel] != e.WorkspaceUID {
			slog.WarnContext(ctx, "spooled audit ConfigMap left in place", "configmap", cm.Name)
			continue
		}
		if _, err := l.Store.AppendEvent(ctx, e); err != nil {
			return moved, err
		}
		uid := cm.UID
		if err := l.Client.Delete(ctx, cm, client.Preconditions{UID: &uid}); err != nil && !apierrors.IsNotFound(err) {
			return moved, err
		}
		moved++
	}
	return moved, nil
}

// Run imports the legacy ConfigMaps (retrying until it succeeds) and drains
// the spool every interval, until ctx ends.
func (l *Log) Run(ctx context.Context, interval time.Duration) {
	if l.Store == nil {
		return
	}
	if interval <= 0 {
		interval = 30 * time.Second
	}
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		step, cancel := context.WithTimeout(ctx, 5*time.Minute)
		if _, err := l.ImportLegacy(step); err != nil {
			slog.Warn("audit ConfigMap import incomplete; retrying", "error", err.Error())
		} else if n, err := l.DrainSpool(step); err != nil {
			slog.Warn("audit spool drain incomplete; retrying", "error", err.Error())
		} else if n > 0 {
			slog.Info("spooled audit records moved into the database", "records", n)
		}
		cancel()
		timer.Reset(interval)
	}
}
