package network

import (
	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// ProviderPolicyName is the one CiliumNetworkPolicy per project namespace that
// lets the project's proxy reach the hosts of the brokered providers the
// project opted into (spec.providers). The controller owns it.
const ProviderPolicyName = "providers"

// ProviderPolicy is implemented by network providers that can let a project's
// proxy pod (never the worker) reach named hosts on port 443 while the project
// runs, without changing its fixed proxy and worker policies.
type ProviderPolicy interface {
	ProviderObject(namespace, uid string, hosts []string) *unstructured.Unstructured
}

// ProviderObject selects only the project's proxy pod and allows DNS for, and
// TLS to, the exact provider host names. No patterns, CIDRs or other ports.
// The proxy still refuses every request without the project's live grant.
func (c Cilium) ProviderObject(ns, uid string, hosts []string) *unstructured.Unstructured {
	c = c.defaults()
	domain := c.ClusterDomain
	dnsNames := []any{}
	fqdns := []any{}
	for _, host := range hosts {
		fqdns = append(fqdns, map[string]any{"matchName": host})
		// The proxy's resolver uses the pod's search path (ndots 1), as in Objects.
		for _, name := range []string{host, host + "." + ns + ".svc." + domain, host + ".svc." + domain, host + "." + domain} {
			dnsNames = append(dnsNames, map[string]any{"matchName": name})
		}
	}
	dns := map[string]any{"toEndpoints": []any{map[string]any{"matchLabels": map[string]any{"k8s:io.kubernetes.pod.namespace": c.DNSNamespace, "k8s:k8s-app": "kube-dns"}}}, "toPorts": []any{map[string]any{"ports": []any{map[string]any{"port": "53", "protocol": "ANY"}}, "rules": map[string]any{"dns": dnsNames}}}}
	tls := map[string]any{"toFQDNs": fqdns, "toPorts": []any{map[string]any{"ports": []any{map[string]any{"port": "443", "protocol": "TCP"}}}}}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "cilium.io/v2", "kind": "CiliumNetworkPolicy",
		"metadata": map[string]any{"name": ProviderPolicyName, "namespace": ns,
			"labels": map[string]any{api.OwnerLabel: uid, LANPolicyLabel: ProviderPolicyName}},
		"spec": map[string]any{
			"endpointSelector": map[string]any{"matchLabels": map[string]any{"projects.agentwork.dev/role": "proxy", api.OwnerLabel: uid}},
			"egress":           []any{dns, tls},
		},
	}}
}
