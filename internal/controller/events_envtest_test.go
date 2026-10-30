package controller

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

// TestEventsReachAPIServer publishes through the real events.k8s.io/v1
// recorder, as main.go wires it, and reads the Events back by the
// regarding.uid field selector.
func TestEventsReachAPIServer(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("set KUBEBUILDER_ASSETS to run a local Kubernetes API server")
	}
	env := &envtest.Environment{CRDDirectoryPaths: []string{filepath.Join("..", "..", "config", "crd")}, ErrorIfCRDPathMissing: true}
	cfg, err := env.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := env.Stop(); err != nil {
			t.Error(err)
		}
	}()
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = api.AddToScheme(scheme)
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err = c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "control"}}); err != nil {
		t.Fatal(err)
	}
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	broadcaster := events.NewBroadcaster(&events.EventSinkImpl{Interface: clientset.EventsV1()})
	broadcaster.StartRecordingToSink(ctx.Done())
	defer broadcaster.Shutdown()
	w := &api.ProjectWorkspace{ObjectMeta: metav1.ObjectMeta{Name: "project", Namespace: "control"}, Spec: api.WorkspaceSpec{Repository: "steve/project", DesiredState: "Running"}}
	if err = c.Create(ctx, w); err != nil {
		t.Fatal(err)
	}
	// A typed Get leaves TypeMeta empty; the recorder resolves the kind from the scheme.
	if err = c.Get(ctx, client.ObjectKeyFromObject(w), w); err != nil {
		t.Fatal(err)
	}
	r := &Reconciler{Client: c, Config: Config{Namespace: "control"}, Events: broadcaster.NewRecorder(scheme, EventReportingController)}
	if err = r.status(ctx, w, "Preparing", false, "PreparationPending", "Waiting for inspected clone and ready Sandbox"); err != nil {
		t.Fatal(err)
	}
	if err = r.status(ctx, w, "Blocked", false, "PolicyRejected", "HTTPS rules require exact lowercase DNS hosts"); err != nil {
		t.Fatal(err)
	}
	for {
		var list eventsv1.EventList
		err = c.List(ctx, &list, client.InNamespace("control"), client.MatchingFields{"regarding.uid": string(w.UID)})
		if err == nil && len(list.Items) == 2 {
			byReason := map[string]eventsv1.Event{}
			for _, e := range list.Items {
				byReason[e.Reason] = e
			}
			blocked, ok := byReason["PolicyRejected"]
			if !ok || blocked.Type != corev1.EventTypeWarning || blocked.Action != ActionChangePhase || blocked.ReportingController != EventReportingController ||
				blocked.Regarding.Kind != "ProjectWorkspace" || blocked.Regarding.Name != "project" || blocked.Regarding.APIVersion != api.GroupVersion.String() {
				t.Fatalf("events = %#v", list.Items)
			}
			if _, ok = byReason["PreparationPending"]; !ok {
				t.Fatalf("events = %#v", list.Items)
			}
			break
		}
		if ctx.Err() != nil {
			t.Fatalf("events not published: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	stored := &api.ProjectWorkspace{}
	if err = c.Get(ctx, client.ObjectKeyFromObject(w), stored); err != nil {
		t.Fatal(err)
	}
	if len(stored.Status.Audit) != 0 {
		t.Fatalf("status.audit appended: %#v", stored.Status.Audit)
	}
}
