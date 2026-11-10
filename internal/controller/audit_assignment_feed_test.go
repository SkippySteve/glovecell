package controller

import (
	"context"
	"encoding/json"
	"errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"strings"
	"testing"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestSyncAuditAssignmentFeedDisabledIsNoOp(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &Reconciler{Client: c, Config: Config{Namespace: "control", AuditAssignmentFeed: false}}
	w := &api.ProjectWorkspace{ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: types.UID("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee")}}
	if err := r.syncAuditAssignmentFeed(context.Background(), w, true); err != nil {
		t.Fatal(err)
	}
	var list corev1.ConfigMapList
	if err := c.List(context.Background(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 0 {
		t.Fatalf("disabled feed wrote ConfigMaps: %d", len(list.Items))
	}
}

func TestSyncAuditAssignmentFeedUpsertsAndRemoves(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &Reconciler{Client: c, Config: Config{Namespace: "control", AuditAssignmentFeed: true}}
	w := &api.ProjectWorkspace{ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: types.UID("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee")}}
	ctx := context.Background()
	if err := r.syncAuditAssignmentFeed(ctx, w, true); err != nil {
		t.Fatal(err)
	}
	cm := &corev1.ConfigMap{}
	if err := c.Get(ctx, types.NamespacedName{Namespace: "control", Name: defaultAuditAssignmentFeedConfigMap}, cm); err != nil {
		t.Fatal(err)
	}
	var doc map[string]auditAssignment
	if err := json.Unmarshal([]byte(cm.Data[auditAssignmentFeedKey]), &doc); err != nil {
		t.Fatal(err)
	}
	key := "apc-aaaaaaaabbbbccccddddeeeeeeeeeeee/project"
	got, ok := doc[key]
	if !ok || got.Project != "demo" || got.Slot != "glovecell" {
		t.Fatalf("unexpected feed entry: %#v", doc)
	}
	if err := r.syncAuditAssignmentFeed(ctx, w, false); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, types.NamespacedName{Namespace: "control", Name: defaultAuditAssignmentFeedConfigMap}, cm); err != nil {
		t.Fatal(err)
	}
	doc = nil
	if err := json.Unmarshal([]byte(cm.Data[auditAssignmentFeedKey]), &doc); err != nil {
		t.Fatal(err)
	}
	if _, ok := doc[key]; ok {
		t.Fatalf("assignment left after remove: %#v", doc)
	}
}

func TestDecodeAuditAssignmentFeedRefusesForeignKeys(t *testing.T) {
	_, err := decodeAuditAssignmentFeed(`{"agent-workspace-orca-pilot/orca":{"slot":"x","project":"y","task":"z","provider":"p","model":"m"}}`)
	if err == nil {
		t.Fatal("expected foreign key refusal")
	}
}

func TestAuditAssignmentRemovalFailureStillStopsAndRevokes(t *testing.T) {
	for _, failure := range []string{"malformed", "update-denied"} {
		t.Run(failure, func(t *testing.T) {
			ctx := context.Background()
			r, w, h := fixture(t)
			r.Config.AuditAssignmentFeed = true
			if err := r.ensure(ctx, w); err != nil {
				t.Fatal(err)
			}
			cm := &corev1.ConfigMap{}
			if err := r.Get(ctx, types.NamespacedName{Namespace: "control", Name: defaultAuditAssignmentFeedConfigMap}, cm); err != nil {
				t.Fatal(err)
			}
			if failure == "malformed" {
				cm.Data[auditAssignmentFeedKey] = "invalid-json"
				if err := r.Update(ctx, cm); err != nil {
					t.Fatal(err)
				}
			} else {
				r.Client = &denyFeedUpdate{Client: r.Client}
			}
			if err := r.remove(ctx, w); err == nil {
				t.Fatal("feed failure reported cleanup complete")
			}
			if h.revokeCalls != 1 {
				t.Fatalf("feed failure blocked revocation: %d", h.revokeCalls)
			}
			sb := &unstructured.Unstructured{}
			sb.SetGroupVersionKind(sandboxGVK)
			if err := r.Get(ctx, types.NamespacedName{Namespace: namespace(w), Name: "project"}, sb); err != nil {
				t.Fatal(err)
			}
			mode, _, _ := unstructured.NestedString(sb.Object, "spec", "operatingMode")
			if mode != "Suspended" {
				t.Fatal("feed failure blocked suspension")
			}
			stored := &api.ProjectWorkspace{}
			if err := r.Get(ctx, types.NamespacedName{Namespace: w.Namespace, Name: w.Name}, stored); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(strings.Join(stored.Finalizers, ","), api.Finalizer) {
				t.Fatal("cleanup failure removed finalizer")
			}
		})
	}
}

type denyFeedUpdate struct{ client.Client }

func (c *denyFeedUpdate) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	if obj.GetName() == defaultAuditAssignmentFeedConfigMap {
		return errors.New("feed update denied")
	}
	return c.Client.Update(ctx, obj, opts...)
}
