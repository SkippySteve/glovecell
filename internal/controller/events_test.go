package controller

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type recordedEvent struct {
	regarding                       runtime.Object
	related                         runtime.Object
	eventtype, reason, action, note string
}

// captureRecorder implements events.EventRecorder and keeps every Event.
type captureRecorder struct {
	mu     sync.Mutex
	events []recordedEvent
}

func (c *captureRecorder) Eventf(regarding, related runtime.Object, eventtype, reason, action, note string, args ...interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, recordedEvent{regarding, related, eventtype, reason, action, fmt.Sprintf(note, args...)})
}

func (c *captureRecorder) take() []recordedEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.events
	c.events = nil
	return out
}

func TestLifecycleTransitionsPublishEventsNotAudit(t *testing.T) {
	ctx := context.Background()
	r, w, _ := fixture(t)
	rec := &captureRecorder{}
	r.Events = rec
	// An entry written by an older controller stays readable and unchanged.
	legacy := api.AuditEvent{Time: metav1.Now(), Action: "Preparing", Generation: 1}
	old := w.DeepCopyObject().(*api.ProjectWorkspace)
	w.Status.Audit = []api.AuditEvent{legacy}
	if err := r.Status().Patch(ctx, w, client.MergeFrom(old)); err != nil {
		t.Fatal(err)
	}

	type step struct {
		phase           string
		ready           bool
		reason, message string
		wantAction      string // "" means no Event
		wantType        string
	}
	steps := []step{
		{"Preparing", false, "PreparationPending", "Waiting for inspected clone and ready Sandbox", ActionChangePhase, corev1.EventTypeNormal},
		// Same phase, same condition: a retry, not a transition.
		{"Preparing", false, "PreparationPending", "Waiting for inspected clone and ready Sandbox", "", ""},
		// Same phase, new reason: the condition changed.
		{"Preparing", false, "T3ConnectPending", "connector not ready", ActionUpdateCondition, corev1.EventTypeNormal},
		{"Ready", true, "ProjectReady", "Inspected clone completed and project environment is ready", ActionChangePhase, corev1.EventTypeNormal},
		{"Ready", true, "ProjectReady", "Inspected clone completed and project environment is ready", "", ""},
		{"Pausing", false, "PausePending", "Waiting for project processes to terminate", ActionChangePhase, corev1.EventTypeNormal},
		{"Paused", false, "Paused", "Project paused; files and repository credentials retained", ActionChangePhase, corev1.EventTypeNormal},
		{"Blocked", false, "PolicyRejected", "HTTPS rules require exact lowercase DNS hosts", ActionChangePhase, corev1.EventTypeWarning},
		{"Removing", false, "CleanupPending", "namespace deletion pending", ActionChangePhase, corev1.EventTypeWarning},
		{"Removed", false, "Removed", "Access revoked and owned Kubernetes storage removed", ActionChangePhase, corev1.EventTypeNormal},
	}
	for i, s := range steps {
		if err := r.status(ctx, w, s.phase, s.ready, s.reason, s.message); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		got := rec.take()
		if s.wantAction == "" {
			if len(got) != 0 {
				t.Fatalf("step %d (%s): repeat published %#v", i, s.phase, got)
			}
			continue
		}
		if len(got) != 1 {
			t.Fatalf("step %d (%s): want one Event, got %#v", i, s.phase, got)
		}
		e := got[0]
		regarding, ok := e.regarding.(*api.ProjectWorkspace)
		if !ok || regarding.UID != w.UID || regarding.Name != w.Name {
			t.Fatalf("step %d: Event not regarding the workspace: %#v", i, e.regarding)
		}
		if e.reason != s.reason || e.action != s.wantAction || e.eventtype != s.wantType || !strings.Contains(e.note, s.message) || !strings.Contains(e.note, "generation 1") {
			t.Fatalf("step %d: Event = %+v", i, e)
		}
		if s.wantAction == ActionChangePhase && !strings.Contains(e.note, "-> "+s.phase) {
			t.Fatalf("step %d: note does not name the transition: %q", i, e.note)
		}
	}

	stored := &api.ProjectWorkspace{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(w), stored); err != nil {
		t.Fatal(err)
	}
	if len(stored.Status.Audit) != 1 || stored.Status.Audit[0].Action != legacy.Action {
		t.Fatalf("status.audit changed: %#v", stored.Status.Audit)
	}
	ready := stored.Status.Conditions
	if len(ready) != 1 || ready[0].Type != "Ready" || ready[0].ObservedGeneration != w.Generation || stored.Status.ObservedGeneration != w.Generation {
		t.Fatalf("conditions = %#v", ready)
	}
}

func TestAuditRecordsPublishOneEventWithRelatedRecord(t *testing.T) {
	ctx := context.Background()
	r, w, _ := fixture(t)
	rec := &captureRecorder{}
	r.Events = rec
	if err := r.ensure(ctx, w); err != nil {
		t.Fatal(err)
	}
	got := rec.take()
	reasons := map[string]recordedEvent{}
	for _, e := range got {
		reasons[e.reason] = e
	}
	for _, want := range []struct{ reason, action string }{{"GrantRequested", "RequestGrant"}, {"AccessGranted", "GrantAccess"}} {
		e, ok := reasons[want.reason]
		if !ok || e.action != want.action || e.eventtype != corev1.EventTypeNormal {
			t.Fatalf("missing %s Event: %#v", want.reason, got)
		}
		cm, ok := e.related.(*corev1.ConfigMap)
		if !ok || !strings.HasPrefix(cm.Name, "audit-") || cm.Namespace != "control" {
			t.Fatalf("%s Event does not name its audit record: %#v", want.reason, e.related)
		}
		if e.regarding.(*api.ProjectWorkspace).UID != w.UID {
			t.Fatal("Event not regarding the workspace")
		}
	}
	// A retry finds the records and publishes nothing again.
	if err := r.ensure(ctx, w); err != nil {
		t.Fatal(err)
	}
	if again := rec.take(); len(again) != 0 {
		t.Fatalf("retry republished: %#v", again)
	}
}

func TestAuditEventReasons(t *testing.T) {
	for action, want := range map[string]string{
		"lan-hosts-applied-0123456789ab": "LANHostsApplied",
		"lan-hosts-withdrawn":            "LANHostsWithdrawn",
		"providers-applied-abcdef":       "ProvidersApplied",
		"reference-rules-0123":           "ReferenceRulesApplied",
		"ca-2":                           "CARotated",
		"dns-abcdef012345":               "DNSUpdated",
		"cleanup-verified":               "CleanupVerified",
		"something-new":                  "AuditRecorded",
	} {
		if got, _, ok := AuditEventReason(action); !ok || got != want {
			t.Errorf("%s: %s, want %s", action, got, want)
		}
	}
	for _, action := range []string{"phase-ready", "condition-0123456789abcdef"} {
		if _, _, ok := AuditEventReason(action); ok {
			t.Errorf("%s published twice", action)
		}
	}
	long := strings.Repeat("é", MaxEventNote)
	if n := truncateNote(long); len(n) > MaxEventNote || !strings.HasSuffix(n, "…") {
		t.Fatalf("note not bounded: %d bytes", len(n))
	}
}
