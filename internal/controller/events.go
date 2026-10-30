package controller

import (
	"strconv"
	"strings"
	"unicode/utf8"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// Lifecycle transitions are published as Kubernetes Events (events.k8s.io/v1)
// regarding the workspace, for `kubectl describe` and `kubectl events --for
// projectworkspace/<name>` recency. status.audit no longer grows (D12).
// Events expire (kube-apiserver --event-ttl, 1h by default), so they are not
// the durable history: that stays in the immutable audit ConfigMaps, which a
// PostgreSQL store replaces later (D13). An Event backed by an audit record
// names that ConfigMap as its related object.
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

// auditEvents maps durable audit actions (or their prefixes, for actions
// that end in a digest or counter) to an Event reason and action.
var auditEvents = []struct{ prefix, reason, action string }{
	{"grant-requested", "GrantRequested", "RequestGrant"},
	{"access-granted", "AccessGranted", "GrantAccess"},
	{"remove-requested", "RemoveRequested", "RequestRemoval"},
	{"access-revoked", "AccessRevoked", "RevokeAccess"},
	{"cleanup-verified", "CleanupVerified", "VerifyCleanup"},
	{"access-policy-applied", "AccessPolicyApplied", "ApplyAccessPolicy"},
	{"lan-hosts-applied-", "LANHostsApplied", "ApplyLANHosts"},
	{"lan-hosts-withdrawn", "LANHostsWithdrawn", "WithdrawLANHosts"},
	{"providers-applied-", "ProvidersApplied", "ApplyProviders"},
	{"reference-rules-", "ReferenceRulesApplied", "ApplyReferenceRules"},
	{"ca-", "CARotated", "RotateCA"},
	{"dns-", "DNSUpdated", "UpdateDNS"},
}

// AuditEventReason returns the Event reason and action for a durable audit
// action. Phase and condition records return ok=false: status() publishes
// those transitions itself, once per transition rather than per generation.
func AuditEventReason(action string) (reason, eventAction string, ok bool) {
	if strings.HasPrefix(action, "phase-") || strings.HasPrefix(action, "condition-") {
		return "", "", false
	}
	for _, e := range auditEvents {
		if action == e.prefix || strings.HasSuffix(e.prefix, "-") && strings.HasPrefix(action, e.prefix) {
			return e.reason, e.action, true
		}
	}
	return "AuditRecorded", "RecordAudit", true
}

// event publishes one Event regarding the workspace. A nil recorder (tests,
// tools) records nothing. Publication is asynchronous and best effort, like
// every Kubernetes Event; the audit ConfigMaps remain the durable record.
func (r *Reconciler) event(w *api.ProjectWorkspace, related runtime.Object, eventtype, reason, action, note string) {
	if r.Events == nil {
		return
	}
	r.Events.Eventf(w, related, eventtype, reason, action, "%s", truncateNote(note))
}

// auditRecordEvent publishes the Event for a newly written audit record.
func (r *Reconciler) auditRecordEvent(w *api.ProjectWorkspace, record *corev1.ConfigMap, action, message string) {
	reason, eventAction, ok := AuditEventReason(action)
	if !ok {
		return
	}
	note := message
	if note == "" {
		note = "Audit record " + action
	}
	r.event(w, record, corev1.EventTypeNormal, reason, eventAction, note+" (generation "+strconv.FormatInt(w.Generation, 10)+")")
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
