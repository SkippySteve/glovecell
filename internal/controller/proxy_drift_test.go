package controller

import (
	"context"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

// Any field the controller never sets on the proxy Deployment is drift, not
// only a changed one: each of these would run code in, or loosen, the
// cell's only egress path.
func TestProxyDeploymentAddedFieldsAreDrift(t *testing.T) {
	ctx := context.Background()
	proxyOf := func(d *appsv1.Deployment) *corev1.Container { return &d.Spec.Template.Spec.Containers[0] }
	for _, tc := range []struct {
		name   string
		mutate func(*appsv1.Deployment)
	}{
		{"env", func(d *appsv1.Deployment) {
			proxyOf(d).Env = []corev1.EnvVar{{Name: "APC_TRACE_PROXY_ROUTES", Value: "1"}}
		}},
		{"envFrom", func(d *appsv1.Deployment) {
			proxyOf(d).EnvFrom = []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "model-access"}}}}
		}},
		{"command", func(d *appsv1.Deployment) { proxyOf(d).Command = []string{"/bin/sh", "-c", "id"} }},
		{"working dir", func(d *appsv1.Deployment) { proxyOf(d).WorkingDir = "/tmp" }},
		{"lifecycle hook", func(d *appsv1.Deployment) {
			proxyOf(d).Lifecycle = &corev1.Lifecycle{PostStart: &corev1.LifecycleHandler{Exec: &corev1.ExecAction{Command: []string{"id"}}}}
		}},
		{"liveness exec probe", func(d *appsv1.Deployment) {
			proxyOf(d).LivenessProbe = &corev1.Probe{ProbeHandler: corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: []string{"id"}}}}
		}},
		{"readiness probe handler", func(d *appsv1.Deployment) {
			proxyOf(d).ReadinessProbe.ProbeHandler = corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: []string{"id"}}}
		}},
		{"container seLinux", func(d *appsv1.Deployment) {
			proxyOf(d).SecurityContext.SELinuxOptions = &corev1.SELinuxOptions{Type: "spc_t"}
		}},
		{"init container", func(d *appsv1.Deployment) {
			d.Spec.Template.Spec.InitContainers = []corev1.Container{{Name: "x", Image: proxyOf(d).Image, SecurityContext: proxyOf(d).SecurityContext}}
		}},
		{"host alias", func(d *appsv1.Deployment) {
			d.Spec.Template.Spec.HostAliases = []corev1.HostAlias{{IP: "203.0.113.9", Hostnames: []string{"api.github.com"}}}
		}},
		{"image pull secret", func(d *appsv1.Deployment) {
			d.Spec.Template.Spec.ImagePullSecrets = []corev1.LocalObjectReference{{Name: "x"}}
		}},
		{"runtime class", func(d *appsv1.Deployment) { rc := "runc-unconfined"; d.Spec.Template.Spec.RuntimeClassName = &rc }},
		{"node name", func(d *appsv1.Deployment) { d.Spec.Template.Spec.NodeName = "kube-node-01" }},
		{"pod run as root", func(d *appsv1.Deployment) { root := int64(0); d.Spec.Template.Spec.SecurityContext.RunAsUser = &root }},
		{"apparmor annotation", func(d *appsv1.Deployment) {
			d.Spec.Template.Annotations = map[string]string{"container.apparmor.security.beta.kubernetes.io/proxy": "unconfined"}
		}},
		{"template label", func(d *appsv1.Deployment) { d.Spec.Template.Labels["extra"] = "x" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, w, _ := fixture(t)
			if err := r.ensure(ctx, w); err != nil {
				t.Fatal(err)
			}
			var d appsv1.Deployment
			if err := r.Get(ctx, types.NamespacedName{Namespace: namespace(w), Name: "proxy"}, &d); err != nil {
				t.Fatal(err)
			}
			tc.mutate(&d)
			if err := r.Update(ctx, &d); err != nil {
				t.Fatal(err)
			}
			if err := r.ensure(ctx, w); err == nil || !strings.Contains(err.Error(), "proxy deployment drift") {
				t.Fatalf("added %s accepted: %v", tc.name, err)
			}
		})
	}
}

func TestProxyDeploymentAcceptsAPIDefaultsAndRolloutRestart(t *testing.T) {
	ctx := context.Background()
	r, w, _ := fixture(t)
	if err := r.ensure(ctx, w); err != nil {
		t.Fatal(err)
	}
	var d appsv1.Deployment
	key := types.NamespacedName{Namespace: namespace(w), Name: "proxy"}
	if err := r.Get(ctx, key, &d); err != nil {
		t.Fatal(err)
	}
	// What the API server fills in (observed on Kubernetes 1.36.2).
	ten, six := int32(10), int32(600)
	d.Spec.RevisionHistoryLimit, d.Spec.ProgressDeadlineSeconds = &ten, &six
	d.Spec.Strategy = appsv1.DeploymentStrategy{Type: appsv1.RollingUpdateDeploymentStrategyType}
	p := &d.Spec.Template.Spec
	p.RestartPolicy, p.SchedulerName, p.DeprecatedServiceAccount = corev1.RestartPolicyAlways, corev1.DefaultSchedulerName, p.ServiceAccountName
	c := &p.Containers[0]
	c.ImagePullPolicy, c.TerminationMessagePath, c.TerminationMessagePolicy = corev1.PullIfNotPresent, corev1.TerminationMessagePathDefault, corev1.TerminationMessageReadFile
	c.Ports[0].Protocol = corev1.ProtocolTCP
	c.ReadinessProbe.TimeoutSeconds, c.ReadinessProbe.PeriodSeconds, c.ReadinessProbe.SuccessThreshold, c.ReadinessProbe.FailureThreshold = 1, 10, 1, 3
	for i := range p.Volumes {
		if p.Volumes[i].ConfigMap != nil {
			mode := corev1.ConfigMapVolumeSourceDefaultMode
			p.Volumes[i].ConfigMap.DefaultMode = &mode
		}
	}
	d.Spec.Template.Annotations = map[string]string{restartAnnotation: "2026-11-11T00:00:00Z"}
	if err := r.Update(ctx, &d); err != nil {
		t.Fatal(err)
	}
	if err := r.ensure(ctx, w); err != nil {
		t.Fatalf("API defaults or rollout restart treated as drift: %v", err)
	}
}
