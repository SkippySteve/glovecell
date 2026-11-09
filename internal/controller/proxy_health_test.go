package controller

import (
	"context"
	"reflect"
	"strings"
	"testing"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestProxyRuntimeHealthTemplateIsPrivateAndPodScoped(t *testing.T) {
	r, w, _ := fixture(t)
	r.Config.ProxyRuntimeGuard = true
	if err := r.ensure(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	var d appsv1.Deployment
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: namespace(w), Name: "proxy"}, &d); err != nil {
		t.Fatal(err)
	}
	if d.Spec.Template.Spec.AutomountServiceAccountToken == nil || *d.Spec.Template.Spec.AutomountServiceAccountToken {
		t.Fatal("proxy gained API token")
	}
	if !reflect.DeepEqual(d.Spec.Template.Spec.Containers[0].ReadinessProbe, proxyHealthProbe()) {
		t.Fatal("TCP readiness remains")
	}
	canonical := d.Spec.DeepCopy()
	enabled, err := normalizeProxyRuntimeHealth(canonical)
	if err != nil || !enabled {
		t.Fatal(enabled, err)
	}
	worker := mustSandbox(t, r, namespace(w))
	spec := worker.Object["spec"].(map[string]any)["podTemplate"].(map[string]any)["spec"].(map[string]any)
	for _, raw := range spec["volumes"].([]any) {
		if raw.(map[string]any)["name"] == proxyStateVolume {
			t.Fatal("worker can access recovery state")
		}
	}
	if len(d.Spec.Template.Annotations[proxyRevisionAnnotation]) != 64 {
		t.Fatal("missing policy rollout revision")
	}
	// API-defaulted probe fields do not make the generated template look corrupt.
	d.Spec.Template.Spec.Containers[0].ReadinessProbe.SuccessThreshold = 1
	if _, err := normalizeProxyRuntimeHealth(d.Spec.DeepCopy()); err != nil {
		t.Fatal(err)
	}
}

func TestProxyHealthModeTransitionRequiresStoppedProjectAndPreservesDriftChecks(t *testing.T) {
	ctx := context.Background()
	r, w, _ := fixture(t)
	if err := r.ensure(ctx, w); err != nil {
		t.Fatal(err)
	}
	r.Config.ProxyRuntimeGuard = true
	if err := r.ensure(ctx, w); err == nil || !strings.Contains(err.Error(), "pause the project") {
		t.Fatal(err)
	}
	w.Spec.DesiredState = "Paused"
	w.Generation++
	if err := r.Update(ctx, w); err != nil {
		t.Fatal(err)
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "worker", Namespace: namespace(w), Labels: labels(w)}}
	if err := r.Create(ctx, pod); err != nil {
		t.Fatal(err)
	}
	if err := r.ensure(ctx, w); err == nil || !strings.Contains(err.Error(), "waiting for project processes") {
		t.Fatal(err)
	}
	if err := r.Delete(ctx, pod); err != nil {
		t.Fatal(err)
	}
	if err := r.ensure(ctx, w); err != nil {
		t.Fatal(err)
	}
	if err := r.ensure(ctx, w); err != nil {
		t.Fatal("interrupted migration not retryable", err)
	}
	var d appsv1.Deployment
	key := types.NamespacedName{Namespace: namespace(w), Name: "proxy"}
	if err := r.Get(ctx, key, &d); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(d.Spec.Template.Spec.Containers[0].ReadinessProbe, proxyHealthProbe()) {
		t.Fatal("migration did not set health probe")
	}
	// Runtime health may not normalize a PVC into an emptyDir.
	d.Spec.Template.Spec.Volumes[len(d.Spec.Template.Spec.Volumes)-1].EmptyDir = nil
	d.Spec.Template.Spec.Volumes[len(d.Spec.Template.Spec.Volumes)-1].PersistentVolumeClaim = &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "workspace"}
	if err := r.Update(ctx, &d); err != nil {
		t.Fatal(err)
	}
	if err := r.ensure(ctx, w); err == nil || !strings.Contains(err.Error(), "drift") {
		t.Fatal("unsafe recovery storage accepted", err)
	}
}

func TestProxyPolicyRevisionChangesRollDeploymentAndPreserveOtherFields(t *testing.T) {
	ctx := context.Background()
	r, w, _ := fixture(t)
	r.Config.ProxyRuntimeGuard = true
	if err := r.ensure(ctx, w); err != nil {
		t.Fatal(err)
	}
	var current appsv1.Deployment
	key := types.NamespacedName{Namespace: namespace(w), Name: "proxy"}
	if err := r.Get(ctx, key, &current); err != nil {
		t.Fatal(err)
	}
	desired := current.DeepCopy()
	configureProxyRuntimeHealthForRevision := func() { // change only a known configuration revision
		desired.Spec.Template.Annotations[proxyRevisionAnnotation] = strings.Repeat("b", 64)
	}
	configureProxyRuntimeHealthForRevision()
	if err := r.apply(ctx, w, desired); err != nil {
		t.Fatal(err)
	}
	var updated appsv1.Deployment
	if err := r.Get(ctx, key, &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Spec.Template.Annotations[proxyRevisionAnnotation] != strings.Repeat("b", 64) {
		t.Fatal("policy change did not roll pod template")
	}
	// An unknown command mutation remains drift even alongside a valid revision.
	updated.Spec.Template.Spec.Containers[0].Args = []string{"shell"}
	if err := r.Update(ctx, &updated); err != nil {
		t.Fatal(err)
	}
	if err := r.apply(ctx, w, desired); err == nil {
		t.Fatal("collateral mutation accepted")
	}
}

func TestProxyQuarantineIsAProjectIncidentRatherThanStarting(t *testing.T) {
	ctx := context.Background()
	r, w, _ := fixture(t)
	r.Config.ProxyRuntimeGuard = true
	if err := r.ensure(ctx, w); err != nil {
		t.Fatal(err)
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "proxy-failed", Namespace: namespace(w), Labels: map[string]string{api.OwnerLabel: string(w.UID), "projects.agentwork.dev/role": "proxy"}}, Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{Name: "proxy", LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Message: "proxy-quarantine:credential-input-invalid\n", ExitCode: 1}}}}}}
	if err := r.Create(ctx, pod); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(w)}); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(w), w); err != nil {
		t.Fatal(err)
	}
	if w.Status.Phase != "Blocked" || len(w.Status.Conditions) == 0 || w.Status.Conditions[0].Reason != "ProxyQuarantined" {
		t.Fatalf("status %#v", w.Status)
	}
}
