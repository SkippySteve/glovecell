package network

import (
	"time"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// DownloadPolicyPrefix names the short-lived policies "Download into sandbox"
// creates, one per check or download: download-<id>.
const DownloadPolicyPrefix = "download-"

// DownloadLabel marks those policies (value: the check or download id) so the
// controller can sweep expired ones and admission can recognise them.
const DownloadLabel = "projects.agentwork.dev/download"

// DownloadExpiresAnnotation is the RFC 3339 time after which the controller
// deletes a download policy that its operation did not remove.
const DownloadExpiresAnnotation = "projects.agentwork.dev/download-expires"

// DownloadPolicy is implemented by providers that can let a project's proxy
// pod (never the worker) resolve and reach named hosts on port 443 for the
// duration of one download.
type DownloadPolicy interface {
	DownloadObject(ns, uid, id string, hosts []string, expires time.Time) *unstructured.Unstructured
}

// DownloadObject selects only the project's proxy pod and allows DNS for, and
// TLS to, the exact host names of one approved redirect chain. No patterns,
// CIDRs or other ports. The proxy still refuses every request that does not
// present the matching one-time grant, so this policy alone opens nothing to
// the worker.
func (c Cilium) DownloadObject(ns, uid, id string, hosts []string, expires time.Time) *unstructured.Unstructured {
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
		"metadata": map[string]any{"name": DownloadPolicyPrefix + id, "namespace": ns,
			"labels":      map[string]any{api.OwnerLabel: uid, DownloadLabel: id},
			"annotations": map[string]any{DownloadExpiresAnnotation: expires.UTC().Format(time.RFC3339)}},
		"spec": map[string]any{
			"endpointSelector": map[string]any{"matchLabels": map[string]any{"projects.agentwork.dev/role": "proxy", api.OwnerLabel: uid}},
			"egress":           []any{dns, tls},
		},
	}}
}
