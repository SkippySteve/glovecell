package controller

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/sessionhub"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

var testHubAgents = []sessionhub.Agent{{ID: "cell-codex", DisplayName: "Codex", Command: "/usr/local/bin/codex-acp"}}

const testHubImage = "example.com/session-hub@sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"

func workerPodSpec(t *testing.T, r *Reconciler, ns string) corev1.PodSpec {
	t.Helper()
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(sandboxGVK)
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: "project"}, u); err != nil {
		t.Fatal(err)
	}
	pod, _, _ := unstructured.NestedMap(u.Object, "spec", "podTemplate", "spec")
	b, _ := json.Marshal(pod)
	var spec corev1.PodSpec
	if err := json.Unmarshal(b, &spec); err != nil {
		t.Fatal(err)
	}
	return spec
}

func container(spec corev1.PodSpec, name string) *corev1.Container {
	for i := range spec.Containers {
		if spec.Containers[i].Name == name {
			return &spec.Containers[i]
		}
	}
	return nil
}

func mounts(c *corev1.Container) map[string]bool {
	m := map[string]bool{}
	for _, v := range c.VolumeMounts {
		m[v.Name] = true
	}
	return m
}

func hubSecret(t *testing.T, r *Reconciler, w *api.ProjectWorkspace) *corev1.Secret {
	t.Helper()
	s := &corev1.Secret{}
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: namespace(w), Name: sessionHubSecret}, s); err != nil {
		t.Fatal(err)
	}
	return s
}

func hubOn(r *Reconciler, w *api.ProjectWorkspace) {
	r.Config.SessionHubEnabled = true
	r.Config.SessionHubImage = testHubImage
	r.Config.SessionHubAgents = testHubAgents
	w.Spec.SessionHub = &api.SessionHubSpec{Enabled: true}
}

func TestSessionHubOffByDefault(t *testing.T) {
	ctx := context.Background()
	r, w, _ := fixture(t)
	r.Config.SessionHubEnabled = true
	r.Config.SessionHubImage = testHubImage
	r.Config.SessionHubAgents = testHubAgents
	if err := r.ensure(ctx, w); err != nil {
		t.Fatal(err)
	}
	spec := workerPodSpec(t, r, namespace(w))
	if len(spec.Containers) != 1 || container(spec, sessionHubContainer) != nil {
		t.Fatalf("a workspace that did not opt in got hub containers: %d", len(spec.Containers))
	}
	err := r.Get(ctx, types.NamespacedName{Namespace: namespace(w), Name: sessionHubSecret}, &corev1.Secret{})
	if !apierrors.IsNotFound(err) {
		t.Fatalf("hub Secret without opt-in: %v", err)
	}
}

// Fail closed: a workspace that asks for the hub on an installation with the
// switch off gets no worker rather than a worker without the hub.
func TestSessionHubRefusedWhenInstallationOff(t *testing.T) {
	ctx := context.Background()
	r, w, _ := fixture(t)
	w.Spec.SessionHub = &api.SessionHubSpec{Enabled: true}
	if err := r.ensure(ctx, w); err == nil || !strings.Contains(err.Error(), "sessionHub.enabled") {
		t.Fatalf("installation off: %v", err)
	}
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(sandboxGVK)
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace(w), Name: "project"}, u); !apierrors.IsNotFound(err) {
		t.Fatalf("worker created without the requested hub: %v", err)
	}
}

func TestSessionHubCellBoundaries(t *testing.T) {
	ctx := context.Background()
	r, w, _ := fixture(t)
	hubOn(r, w)
	if err := r.ensure(ctx, w); err != nil {
		t.Fatal(err)
	}
	spec := workerPodSpec(t, r, namespace(w))
	workspace, hub, launcher := container(spec, "workspace"), container(spec, sessionHubContainer), container(spec, sessionHubLauncher)
	if workspace == nil || hub == nil || launcher == nil {
		t.Fatalf("containers: %+v", spec.Containers)
	}
	if hub.Image != testHubImage || launcher.Image != workspace.Image {
		t.Fatalf("images: hub %s launcher %s workspace %s", hub.Image, launcher.Image, workspace.Image)
	}
	if *hub.SecurityContext.RunAsUser != sessionHubHubUser || *launcher.SecurityContext.RunAsUser != *workspace.SecurityContext.RunAsUser {
		t.Fatal("hub must run as its own user, the launcher as the workspace user")
	}
	for _, c := range []*corev1.Container{hub, launcher} {
		s := c.SecurityContext
		if s == nil || !*s.ReadOnlyRootFilesystem || *s.AllowPrivilegeEscalation || len(s.Capabilities.Drop) == 0 {
			t.Fatalf("%s lacks the worker security context", c.Name)
		}
	}
	// Only the hub sees the token; the launcher (where agents run) sees the
	// allowlist only; the workspace sees neither, nor the socket.
	if m := mounts(hub); !m["session-hub-config"] || m["session-hub-agents"] || m["workspace"] {
		t.Fatalf("hub mounts %v", m)
	}
	if m := mounts(launcher); m["session-hub-config"] || !m["session-hub-agents"] || !m["session-hub-run"] || !m["workspace"] {
		t.Fatalf("launcher mounts %v", m)
	}
	if m := mounts(workspace); m["session-hub-config"] || m["session-hub-agents"] || m["session-hub-run"] {
		t.Fatalf("workspace mounts %v", m)
	}
	for _, v := range spec.Volumes {
		if v.Secret != nil && v.Secret.SecretName == sessionHubSecret {
			if len(v.Secret.Items) != 1 || v.Secret.Items[0].Key == "token" {
				t.Fatalf("volume %s projects %v", v.Name, v.Secret.Items)
			}
		}
	}
	for _, e := range hub.Env {
		if strings.Contains(strings.ToUpper(e.Name), "PROXY") {
			t.Fatalf("hub has egress settings: %s", e.Name)
		}
	}
	if ports := hub.Ports; len(ports) != 0 {
		t.Fatalf("hub exposes ports %v", ports)
	}

	s := hubSecret(t, r, w)
	token := string(s.Data["token"])
	if !sessionhub.ValidToken(token) || !strings.Contains(string(s.Data["gateway.json"]), token) {
		t.Fatal("gateway config lacks the cell's token")
	}
	if strings.Contains(string(s.Data["agents.json"]), token) {
		t.Fatal("allowlist carries the token")
	}

	// Reconciling again keeps the token and raises no drift; an allowlist
	// change reaches the Secret without a new token.
	r.Config.SessionHubAgents = append(append([]sessionhub.Agent{}, testHubAgents...), sessionhub.Agent{ID: "cell-grok", DisplayName: "Grok", Command: "/usr/local/bin/grok", Args: []string{"acp"}})
	if err := r.ensure(ctx, w); err != nil {
		t.Fatal(err)
	}
	s = hubSecret(t, r, w)
	if string(s.Data["token"]) != token || !strings.Contains(string(s.Data["agents.json"]), "cell-grok") || !strings.Contains(string(s.Data["gateway.json"]), "cell-grok") {
		t.Fatal("allowlist update changed the token or did not apply")
	}
}

// A token someone overwrote with junk is replaced, never served.
func TestSessionHubReplacesInvalidToken(t *testing.T) {
	ctx := context.Background()
	r, w, _ := fixture(t)
	hubOn(r, w)
	if err := r.ensure(ctx, w); err != nil {
		t.Fatal(err)
	}
	s := hubSecret(t, r, w)
	s.Data["token"] = []byte("guessable")
	if err := r.Update(ctx, s); err != nil {
		t.Fatal(err)
	}
	if err := r.ensure(ctx, w); err != nil {
		t.Fatal(err)
	}
	if got := string(hubSecret(t, r, w).Data["token"]); !sessionhub.ValidToken(got) {
		t.Fatalf("token after tampering: %q", got)
	}
}

// Workspace and hub image promotions roll the hub containers with the worker,
// like the workspace image itself, instead of failing as drift.
func TestSessionHubImagesRoll(t *testing.T) {
	ctx := context.Background()
	r, w, _ := fixture(t)
	hubOn(r, w)
	if err := r.ensure(ctx, w); err != nil {
		t.Fatal(err)
	}
	r.Config.Image = "example.com/worker@sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	r.Config.SessionHubImage = "example.com/session-hub@sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	if err := r.ensure(ctx, w); err == nil || !strings.Contains(err.Error(), "being updated") {
		t.Fatalf("image roll: %v", err)
	}
	if err := r.ensure(ctx, w); err != nil {
		t.Fatal(err)
	}
	spec := workerPodSpec(t, r, namespace(w))
	if container(spec, sessionHubLauncher).Image != r.orcaSpokeImage(w) || container(spec, sessionHubContainer).Image != r.Config.SessionHubImage {
		t.Fatalf("hub containers did not roll: %s %s", container(spec, sessionHubLauncher).Image, container(spec, sessionHubContainer).Image)
	}
}

func TestValidateSessionHub(t *testing.T) {
	if err := validateSessionHub(false, "", nil); err != nil {
		t.Fatal(err)
	}
	if err := validateSessionHub(true, "", testHubAgents); err == nil {
		t.Error("no image accepted")
	}
	if err := validateSessionHub(true, testHubImage, nil); err == nil {
		t.Error("no agents accepted")
	}
	if err := validateSessionHub(true, testHubImage, testHubAgents); err != nil {
		t.Fatal(err)
	}
}
