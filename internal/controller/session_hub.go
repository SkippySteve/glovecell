package controller

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/sessionhub"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
)

// Session hub (docs/session-hub.md). A workspace created with
// spec.sessionHub.enabled gets two more worker containers:
//
//   - session-hub: AHP-ACP Gateway, user 1001, loopback listener with the
//     cell's token. It reads gateway.json from the cell's Secret and never
//     runs an agent itself.
//   - session-hub-launcher: the workspace image with the workspace's mounts
//     and environment. It starts allowlisted agents for the hub over a unix
//     socket. It sees agents.json only, never the token.
const (
	sessionHubSecret    = "session-hub"
	sessionHubContainer = "session-hub"
	sessionHubLauncher  = "session-hub-launcher"
	sessionHubHubUser   = 1001
)

var errSessionHubOff = errors.New("spec.sessionHub.enabled needs the installation's sessionHub.enabled; this installation has it off")

// validateSessionHub checks the installation's switch, image and allowlist.
func validateSessionHub(enabled bool, image string, agents []sessionhub.Agent) error {
	if !enabled {
		return nil
	}
	if image == "" {
		return errors.New("SESSION_HUB_ENABLED needs SESSION_HUB_IMAGE")
	}
	return sessionhub.ValidateAgents(agents)
}

// usesSessionHub reports whether this workspace's worker carries the hub.
// A workspace that asks for it on an installation with the switch off is
// refused by objects(), never silently started without it.
func (r *Reconciler) usesSessionHub(w *api.ProjectWorkspace) bool {
	return w != nil && w.Spec.SessionHubEnabled()
}

// sessionHubSecretFor keeps the cell's token across reconciles and renders
// the gateway config and allowlist from the current installation settings.
func (r *Reconciler) sessionHubSecretFor(ctx context.Context, w *api.ProjectWorkspace) (*corev1.Secret, error) {
	token := ""
	current := &corev1.Secret{}
	err := r.Get(ctx, types.NamespacedName{Namespace: namespace(w), Name: sessionHubSecret}, current)
	switch {
	case err == nil:
		if current.Labels[api.OwnerLabel] != string(w.UID) {
			return nil, errors.New("session hub Secret is not owned by this workspace")
		}
		token = string(current.Data["token"])
	case apierrors.IsNotFound(err):
	default:
		return nil, err
	}
	if !sessionhub.ValidToken(token) {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return nil, err
		}
		token = hex.EncodeToString(b)
	}
	gateway, err := sessionhub.GatewayConfig(token, r.Config.SessionHubAgents)
	if err != nil {
		return nil, err
	}
	agents, err := json.Marshal(r.Config.SessionHubAgents)
	if err != nil {
		return nil, err
	}
	return &corev1.Secret{ObjectMeta: metadata(w, sessionHubSecret), Data: map[string][]byte{"token": []byte(token), "gateway.json": gateway, "agents.json": agents}}, nil
}

// addSessionHub adds the hub and launcher containers and their volumes to a
// worker pod spec. workspace is the workspace container as built, so the
// launcher gets the same image, mounts and environment (without telemetry).
func addSessionHub(spec *corev1.PodSpec, hubImage string, workspace corev1.Container, env []corev1.EnvVar, security *corev1.SecurityContext) {
	spec.Volumes = append(spec.Volumes,
		corev1.Volume{Name: "session-hub-run", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		corev1.Volume{Name: "session-hub-state", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		corev1.Volume{Name: "session-hub-config", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: sessionHubSecret, DefaultMode: int32p(0440), Items: []corev1.KeyToPath{{Key: "gateway.json", Path: "gateway.json"}}}}},
		corev1.Volume{Name: "session-hub-agents", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: sessionHubSecret, DefaultMode: int32p(0440), Items: []corev1.KeyToPath{{Key: "agents.json", Path: "agents.json"}}}}},
	)
	hubSecurity := security.DeepCopy()
	hubSecurity.RunAsUser = int64p(sessionHubHubUser)
	hubSecurity.RunAsGroup = int64p(sessionHubHubUser)
	hub := corev1.Container{
		Name: sessionHubContainer, Image: hubImage,
		Args:            []string{"--config", "/run/session-hub-config/gateway.json"},
		Env:             []corev1.EnvVar{{Name: "HOME", Value: "/var/lib/session-hub"}},
		SecurityContext: hubSecurity,
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("20m"), corev1.ResourceMemory: resource.MustParse("64Mi")},
			Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m"), corev1.ResourceMemory: resource.MustParse("256Mi")},
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: "session-hub-config", MountPath: "/run/session-hub-config", ReadOnly: true},
			{Name: "session-hub-run", MountPath: "/run/session-hub"},
			{Name: "session-hub-state", MountPath: "/var/lib/session-hub"},
		},
	}
	launcherEnv := append(append([]corev1.EnvVar{}, env...),
		corev1.EnvVar{Name: "SESSION_HUB_SOCKET", Value: sessionhub.SocketPath},
		corev1.EnvVar{Name: "SESSION_HUB_AGENTS_FILE", Value: "/run/session-hub-agents/agents.json"},
		corev1.EnvVar{Name: "SESSION_HUB_DIR", Value: "/workspace/repo"},
	)
	launcher := corev1.Container{
		Name: sessionHubLauncher, Image: workspace.Image,
		Command:         []string{sessionhub.Binary, "session-hub-launcher"},
		Env:             launcherEnv,
		WorkingDir:      workspace.WorkingDir,
		SecurityContext: security.DeepCopy(),
		Resources:       *workspace.Resources.DeepCopy(),
		VolumeMounts: append(append([]corev1.VolumeMount{}, workspace.VolumeMounts...),
			corev1.VolumeMount{Name: "session-hub-run", MountPath: "/run/session-hub"},
			corev1.VolumeMount{Name: "session-hub-agents", MountPath: "/run/session-hub-agents", ReadOnly: true},
		),
	}
	spec.Containers = append(spec.Containers, hub, launcher)
}
