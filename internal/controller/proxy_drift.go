package controller

import (
	"reflect"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

// restartAnnotation is the only pod template annotation an operator may add
// to a proxy Deployment (kubectl rollout restart). Any other annotation is
// drift: some, such as AppArmor's, change the pod's confinement.
const restartAnnotation = "kubectl.kubernetes.io/restartedAt"

// clearAPIDefaults removes the fields the Kubernetes API server fills in on a
// Deployment the controller created, so the proxy Deployment can be compared
// in both directions: any other field present on only one side is drift.
// The list is proven against a real API server by the envtest suite; when the
// cluster's Kubernetes minor version changes, run that suite with it first.
func clearAPIDefaults(spec *appsv1.DeploymentSpec) {
	spec.RevisionHistoryLimit = nil
	spec.ProgressDeadlineSeconds = nil
	spec.Strategy = appsv1.DeploymentStrategy{}
	pod := &spec.Template.Spec
	if pod.RestartPolicy == corev1.RestartPolicyAlways {
		pod.RestartPolicy = ""
	}
	if pod.SchedulerName == corev1.DefaultSchedulerName {
		pod.SchedulerName = ""
	}
	if pod.DeprecatedServiceAccount == pod.ServiceAccountName {
		pod.DeprecatedServiceAccount = ""
	}
	if pod.SecurityContext != nil && reflect.DeepEqual(*pod.SecurityContext, corev1.PodSecurityContext{}) {
		pod.SecurityContext = nil
	}
	for _, list := range [][]corev1.Container{pod.InitContainers, pod.Containers} {
		for i := range list {
			c := &list[i]
			c.ImagePullPolicy = ""
			if c.TerminationMessagePath == corev1.TerminationMessagePathDefault {
				c.TerminationMessagePath = ""
			}
			if c.TerminationMessagePolicy == corev1.TerminationMessageReadFile {
				c.TerminationMessagePolicy = ""
			}
			for j := range c.Ports {
				if c.Ports[j].Protocol == corev1.ProtocolTCP {
					c.Ports[j].Protocol = ""
				}
			}
			for _, p := range []*corev1.Probe{c.ReadinessProbe, c.LivenessProbe, c.StartupProbe} {
				clearProbeDefaults(p)
			}
		}
	}
	for i := range pod.Volumes {
		v := &pod.Volumes[i]
		if v.ConfigMap != nil && v.ConfigMap.DefaultMode != nil && *v.ConfigMap.DefaultMode == corev1.ConfigMapVolumeSourceDefaultMode {
			v.ConfigMap.DefaultMode = nil
		}
		if v.Secret != nil && v.Secret.DefaultMode != nil && *v.Secret.DefaultMode == corev1.SecretVolumeSourceDefaultMode {
			v.Secret.DefaultMode = nil
		}
	}
	if spec.Template.Annotations != nil {
		delete(spec.Template.Annotations, restartAnnotation)
		if len(spec.Template.Annotations) == 0 {
			spec.Template.Annotations = nil
		}
	}
}

func clearProbeDefaults(p *corev1.Probe) {
	if p == nil {
		return
	}
	if p.TimeoutSeconds == 1 {
		p.TimeoutSeconds = 0
	}
	if p.PeriodSeconds == 10 {
		p.PeriodSeconds = 0
	}
	if p.SuccessThreshold == 1 {
		p.SuccessThreshold = 0
	}
	if p.FailureThreshold == 3 {
		p.FailureThreshold = 0
	}
	if p.HTTPGet != nil && p.HTTPGet.Scheme == corev1.URISchemeHTTP {
		p.HTTPGet.Scheme = ""
	}
}
