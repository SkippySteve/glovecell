package controller

import (
	"unicode/utf8"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
)

// Lifecycle transitions are published as Kubernetes Events (events.k8s.io/v1)
// regarding the workspace, for `kubectl describe` and `kubectl events --for
// projectworkspace/<name>`: phase changes and new not-ready reasons only.
// status.audit no longer grows (D12). Events expire (kube-apiserver
// --event-ttl, 1h by default), so they are not the durable history: audit
// records are (internal/auditlog: PostgreSQL, with audit ConfigMaps before the
// database or while it is down), and publish no Event of their own.
const (
	// ActionChangePhase reports a status.phase transition.
	ActionChangePhase = "ChangePhase"
	// ActionUpdateCondition reports a new reason or message on the Ready
	// condition while the phase stays the same.
	ActionUpdateCondition = "UpdateCondition"

	// EventReportingController is reportingController on every Event this
	// controller publishes.
	EventReportingController = "projects.agentwork.dev/agent-project-controller"

	// MaxEventNote is the API server's limit on events.k8s.io/v1 note.
	MaxEventNote = 1024
)

// event publishes one Event regarding the workspace. A nil recorder (tests,
// tools) records nothing. Publication is asynchronous and best effort, like
// every Kubernetes Event; the audit log remains the durable record.
func (r *Reconciler) event(w *api.ProjectWorkspace, eventtype, reason, action, note string) {
	if r.Events == nil {
		return
	}
	r.Events.Eventf(w, nil, eventtype, reason, action, "%s", truncateNote(note))
}

func phaseEventType(phase, reason string) string {
	if phase == "Blocked" || reason == "CleanupPending" {
		return corev1.EventTypeWarning
	}
	return corev1.EventTypeNormal
}

func truncateNote(note string) string {
	if len(note) <= MaxEventNote {
		return note
	}
	cut := MaxEventNote - len("…")
	for cut > 0 && !utf8.RuneStart(note[cut]) {
		cut--
	}
	return note[:cut] + "…"
}
