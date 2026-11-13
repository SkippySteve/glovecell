package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"reflect"
	"strings"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const proxyRevisionAnnotation = "projects.agentwork.dev/proxy-revision"
const proxyStateVolume = "runtime-health"
const proxyStatePath = "/run/proxy-health"

func proxyHealthProbe() *corev1.Probe {
	return &corev1.Probe{ProbeHandler: corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: []string{"/usr/local/bin/agent-project-controller", "proxy-ready"}}}, PeriodSeconds: 2, TimeoutSeconds: 3, FailureThreshold: 1}
}

func configureProxyRuntimeHealth(deployment *appsv1.Deployment, policy, ca []byte) {
	c := &deployment.Spec.Template.Spec.Containers[0]
	c.Env = append(c.Env, corev1.EnvVar{Name: "APC_PROXY_RUNTIME_GUARD", Value: "true"}, corev1.EnvVar{Name: "APC_PROXY_STATE_DIR", Value: proxyStatePath})
	c.ReadinessProbe = proxyHealthProbe()
	c.VolumeMounts = append(c.VolumeMounts, corev1.VolumeMount{Name: proxyStateVolume, MountPath: proxyStatePath})
	limit := resource.MustParse("1Mi")
	deployment.Spec.Template.Spec.Volumes = append(deployment.Spec.Template.Spec.Volumes, corev1.Volume{Name: proxyStateVolume, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: &limit}}})
	digest := sha256.New()
	_, _ = digest.Write(policy)
	_, _ = digest.Write(ca)
	deployment.Spec.Template.Annotations = map[string]string{proxyRevisionAnnotation: hex.EncodeToString(digest.Sum(nil))}
}

// normalizeProxyRuntimeHealth accepts only the two generated profiles. It does
// not normalize arbitrary mounts, environment, probes or volumes into safety.
func normalizeProxyRuntimeHealth(spec *appsv1.DeploymentSpec) (bool, error) {
	var c *corev1.Container
	for i := range spec.Template.Spec.Containers {
		if spec.Template.Spec.Containers[i].Name == "proxy" {
			if c != nil {
				return false, errors.New("duplicate proxy container")
			}
			c = &spec.Template.Spec.Containers[i]
		}
	}
	if c == nil {
		return false, errors.New("proxy container missing")
	}
	var guard, state int
	env := c.Env[:0]
	for _, v := range c.Env {
		switch v.Name {
		case "APC_PROXY_RUNTIME_GUARD":
			if v.Value != "true" || v.ValueFrom != nil {
				return false, errors.New("invalid proxy guard environment")
			}
			guard++
		case "APC_PROXY_STATE_DIR":
			if v.Value != proxyStatePath || v.ValueFrom != nil {
				return false, errors.New("invalid proxy state environment")
			}
			state++
		default:
			env = append(env, v)
		}
	}
	var mounts, volumes int
	vm := c.VolumeMounts[:0]
	for _, v := range c.VolumeMounts {
		if v.Name == proxyStateVolume {
			if !reflect.DeepEqual(v, corev1.VolumeMount{Name: proxyStateVolume, MountPath: proxyStatePath}) {
				return false, errors.New("invalid proxy state mount")
			}
			mounts++
		} else {
			vm = append(vm, v)
		}
	}
	vs := spec.Template.Spec.Volumes[:0]
	for _, v := range spec.Template.Spec.Volumes {
		if v.Name == proxyStateVolume {
			limit := resource.MustParse("1Mi")
			expected := corev1.Volume{Name: proxyStateVolume, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: &limit}}}
			if !reflect.DeepEqual(v, expected) {
				return false, errors.New("invalid proxy state volume")
			}
			volumes++
		} else {
			vs = append(vs, v)
		}
	}
	enabled := guard+state+mounts+volumes > 0
	if !enabled {
		if spec.Template.Annotations[proxyRevisionAnnotation] != "" {
			return false, errors.New("unexpected proxy revision")
		}
		return false, nil
	}
	if guard != 1 || state != 1 || mounts != 1 || volumes != 1 || (c.ReadinessProbe == nil || !contains(*c.ReadinessProbe, *proxyHealthProbe())) {
		return false, errors.New("incomplete proxy health profile")
	}
	revision := spec.Template.Annotations[proxyRevisionAnnotation]
	if len(revision) != 64 {
		return false, errors.New("missing proxy revision")
	}
	if _, err := hex.DecodeString(revision); err != nil {
		return false, errors.New("invalid proxy revision")
	}
	c.Env = env
	if len(c.Env) == 0 {
		c.Env = nil
	}
	c.VolumeMounts = vm
	spec.Template.Spec.Volumes = vs
	c.ReadinessProbe = &corev1.Probe{ProbeHandler: corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{Port: fromInt(3128)}}}
	delete(spec.Template.Annotations, proxyRevisionAnnotation)
	if len(spec.Template.Annotations) == 0 {
		spec.Template.Annotations = nil
	}
	return true, nil
}

// proxyHealthIncident reads only sanitized termination markers from the owned
// proxy container. A stuck/restarted quarantine remains an incident until the
// controller observes a Ready replacement pod.
func (r *Reconciler) proxyHealthIncident(ctx context.Context, w *api.ProjectWorkspace) (bool, error) {
	var pods corev1.PodList
	if err := r.List(ctx, &pods, client.InNamespace(namespace(w)), client.MatchingLabels{api.OwnerLabel: string(w.UID), "projects.agentwork.dev/role": "proxy"}); err != nil {
		return false, err
	}
	for _, p := range pods.Items {
		if !p.DeletionTimestamp.IsZero() {
			continue
		}
		for _, c := range p.Status.ContainerStatuses {
			if c.Name != "proxy" {
				continue
			}
			for _, term := range []*corev1.ContainerStateTerminated{c.State.Terminated, c.LastTerminationState.Terminated} {
				if term != nil && strings.HasPrefix(term.Message, "proxy-quarantine:") {
					return true, nil
				}
			}
		}
	}
	return false, nil
}
