package controller

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/proxy"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestVendorTelemetryToggleAndInterruptedRestart(t *testing.T) {
	ctx := context.Background()
	r, w, _ := fixture(t)
	if err := r.ensure(ctx, w); err != nil {
		t.Fatal(err)
	}
	for _, enabled := range []bool{true, false} {
		r.Config.VendorTelemetry = enabled
		_, current := proxyPolicy(t, r, w)
		want := r.withLabelled(current, nil)
		if want.VendorTelemetry != enabled {
			t.Fatal("controller switch did not reach proxy policy")
		}
		for _, host := range proxy.VendorTelemetryHosts() {
			if slices.Contains(policyHosts(want), host) != enabled {
				t.Fatal("network host did not follow switch", host)
			}
		}
		pod := runningProxyPod(t, r, w)
		if err := r.ensure(ctx, w); err != nil {
			t.Fatal(err)
		}
		if err := r.Get(ctx, types.NamespacedName{Namespace: pod.Namespace, Name: pod.Name}, pod); err == nil {
			t.Fatal("proxy was not restarted on telemetry change")
		}
		networkRaw, err := json.Marshal(proxyNetworkPolicy(t, r, w).Object["spec"])
		if err != nil {
			t.Fatal(err)
		}
		for _, host := range proxy.VendorTelemetryHosts() {
			if strings.Contains(string(networkRaw), `"matchName":"`+host+`"`) != enabled {
				t.Fatal("rendered Cilium policy did not follow switch", host)
			}
		}
		_, got := proxyPolicy(t, r, w)
		if proxy.Allowed(got, "ab.chatgpt.com", "POST", "/otlp/v1/metrics") != enabled {
			t.Fatal("collection did not follow switch")
		}
		// Simulate a crash after policy.json was saved but before the old
		// proxy was stopped: completion marker still names the old mode.
		cm := &corev1.ConfigMap{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: namespace(w), Name: "proxy-policy"}, cm); err != nil {
			t.Fatal(err)
		}
		if enabled {
			cm.Annotations[telemetryApplied] = "disabled"
		} else {
			cm.Annotations[telemetryApplied] = "enabled"
		}
		if err := r.Update(ctx, cm); err != nil {
			t.Fatal(err)
		}
		pod = runningProxyPod(t, r, w)
		if err := r.ensure(ctx, w); err != nil {
			t.Fatal(err)
		}
		if err := r.Get(ctx, types.NamespacedName{Namespace: pod.Namespace, Name: pod.Name}, pod); err == nil {
			t.Fatal("interrupted restart was not recovered")
		}
		pod = runningProxyPod(t, r, w)
		if err := r.ensure(ctx, w); err != nil {
			t.Fatal(err)
		}
		if err := r.Get(ctx, types.NamespacedName{Namespace: pod.Namespace, Name: pod.Name}, pod); err != nil {
			t.Fatal("unchanged mode restarted the proxy")
		}
		if err := r.Delete(ctx, pod); err != nil {
			t.Fatal(err)
		}
	}
}

func TestVendorTelemetryPolicySerialization(t *testing.T) {
	r, _, _ := fixture(t)
	r.Config.VendorTelemetry = true
	cfg := r.withLabelled(proxy.Config{Rules: []api.HTTPSRule{{Host: "git.example.com", Methods: []string{"GET"}, Paths: []string{"/"}}}}, nil)
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var restored proxy.Config
	if err := json.Unmarshal(raw, &restored); err != nil || !restored.VendorTelemetry {
		t.Fatal("telemetry mode lost in mounted policy", err)
	}
}
