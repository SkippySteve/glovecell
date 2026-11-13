package controller

import (
	"context"
	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"strings"
	"testing"
)

func TestBootstrapSourceDefaultsAndValidation(t *testing.T) {
	r, _, _ := fixture(t)
	r.Config.ProxyImage = "registry.example/runtime@sha256:" + strings.Repeat("b", 64)
	for source, expected := range map[string]string{"": r.Config.Image, "workspace": r.Config.Image, "proxy": r.Config.ProxyImage} {
		r.Config.BootstrapImageSource = source
		if r.Config.bootstrapImage() != expected {
			t.Fatal(source, "selected wrong image")
		}
		if err := r.Config.Validate(); err != nil {
			t.Fatal(source, err)
		}
	}
	r.Config.BootstrapImageSource = "untrusted"
	if r.Config.Validate() == nil {
		t.Fatal("invalid source accepted")
	}
}

func TestLeanBootstrapKeepsWorkerImageAndAuthority(t *testing.T) {
	r, w, _ := fixture(t)
	r.Config.ProxyImage = "registry.example/runtime@sha256:" + strings.Repeat("b", 64)
	r.Config.BootstrapImageSource = "proxy"
	if err := r.ensure(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	clone, worker := sandboxImages(t, r, namespace(w))
	if clone != r.Config.ProxyImage || worker != r.Config.Image {
		t.Fatal("incorrect image split", clone, worker)
	}
	sandbox := mustSandbox(t, r, namespace(w))
	spec := sandbox.Object["spec"].(map[string]any)["podTemplate"].(map[string]any)["spec"].(map[string]any)
	if spec["automountServiceAccountToken"] != false {
		t.Fatal("bootstrap gained API credentials")
	}
	init := spec["initContainers"].([]any)[0].(map[string]any)
	args := init["args"].([]any)
	if len(args) != 1 || args[0] != "bootstrap" {
		t.Fatal("unexpected bootstrap command")
	}
	security := init["securityContext"].(map[string]any)
	if security["allowPrivilegeEscalation"] != false {
		t.Fatal("bootstrap can escalate")
	}
}

func TestLeanBootstrapRejectsRunningImageDrift(t *testing.T) {
	ctx := context.Background()
	r, w, _ := fixture(t)
	r.Config.BootstrapImageSource = "proxy"
	r.Config.ProxyImage = "registry.example/runtime@sha256:" + strings.Repeat("b", 64)
	if err := r.ensure(ctx, w); err != nil {
		t.Fatal(err)
	}
	// An unexpected image in a running cell is still drift, not an authorized
	// transition to the configured lean bootstrap image.
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "worker", Namespace: namespace(w), Labels: map[string]string{api.OwnerLabel: string(w.UID), "projects.agentwork.dev/role": "worker"}}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "workspace", Image: r.Config.Image}}}}
	if err := r.Create(ctx, pod); err != nil {
		t.Fatal(err)
	}
	setSandboxImage(t, r, namespace(w), "evil.example/workspace:latest")
	if err := r.ensure(ctx, w); err == nil || !strings.Contains(err.Error(), "drift") {
		t.Fatal("running mutation was not rejected", err)
	}
}
