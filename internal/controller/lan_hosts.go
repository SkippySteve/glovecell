package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"strings"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/network"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

var ciliumPolicyGVK = schema.GroupVersionKind{Group: "cilium.io", Version: "v2", Kind: "CiliumNetworkPolicy"}

// LANSummary renders grants for audit records and logs, e.g. "192.168.1.20 tcp/22,8080".
func LANSummary(hosts []api.LANHost) string {
	parts := make([]string, 0, len(hosts))
	for _, h := range hosts {
		ports := make([]string, 0, len(h.Ports))
		for _, p := range h.Ports {
			ports = append(ports, fmt.Sprint(p))
		}
		parts = append(parts, h.Address+" tcp/"+strings.Join(ports, ","))
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, "; ")
}

// validateLANHosts is the static check run with the rest of the project policy.
// A refused grant blocks the project, like an invalid HTTPS rule.
func (r *Reconciler) validateLANHosts(w *api.ProjectWorkspace) error {
	if len(w.Spec.LANHosts) == 0 {
		return nil
	}
	if _, ok := r.Network.(network.LANPolicy); !ok {
		return errors.New("the network provider cannot enforce LAN host grants; remove them from this project")
	}
	if err := r.LAN.Check(w.Spec.LANHosts, nil); err != nil {
		return fmt.Errorf("%w; remove or correct the grant", err)
	}
	return nil
}

// applyLANHosts makes policy lan-hosts match spec.lanHosts. It runs while the
// project runs: adding or removing a grant needs no pause or restart.
// The policy is controller-owned; any other content is overwritten.
func (r *Reconciler) applyLANHosts(ctx context.Context, w *api.ProjectWorkspace) error {
	ns := namespace(w)
	provider, ok := r.Network.(network.LANPolicy)
	if !ok {
		// Other providers never create the policy and may lack its API.
		if len(w.Spec.LANHosts) > 0 {
			return errors.New("the network provider cannot enforce LAN host grants")
		}
		return nil
	}
	var want *unstructured.Unstructured
	if len(w.Spec.LANHosts) > 0 {
		nodes, err := network.NodeAddresses(ctx, r.Client)
		if err != nil {
			return err
		}
		if err = r.LAN.Check(w.Spec.LANHosts, nodes); err != nil {
			return err
		}
		want = provider.LANObject(ns, string(w.UID), w.Spec.LANHosts)
	}
	current := &unstructured.Unstructured{}
	current.SetGroupVersionKind(ciliumPolicyGVK)
	err := r.Get(ctx, types.NamespacedName{Namespace: ns, Name: network.LANPolicyName}, current)
	switch {
	case apierrors.IsNotFound(err):
		if want == nil {
			return nil
		}
		if err = r.Create(ctx, want); err != nil {
			return err
		}
	case err != nil:
		return err
	default:
		if err = owner(w, current); err != nil {
			return err
		}
		if want == nil {
			if err = r.Delete(ctx, current); err != nil && !apierrors.IsNotFound(err) {
				return err
			}
			break
		}
		labels := current.GetLabels()
		if same(current.Object["spec"], want.Object["spec"]) && labels[network.LANPolicyLabel] == network.LANPolicyName {
			return nil
		}
		merged := maps.Clone(labels)
		maps.Copy(merged, want.GetLabels())
		current.SetLabels(merged)
		current.Object["spec"] = want.Object["spec"]
		if err = r.Update(ctx, current); err != nil {
			return err
		}
	}
	summary := LANSummary(w.Spec.LANHosts)
	sum := sha256.Sum256([]byte(summary))
	slog.InfoContext(ctx, "LAN host policy applied", "workspace_uid", string(w.UID), "generation", w.Generation, "lan_hosts", summary)
	return r.auditDetails(ctx, w, "lan-hosts-applied-"+hex.EncodeToString(sum[:6]), map[string]string{"lanHosts": summary, "message": "LAN host policy now allows: " + summary})
}

// removeLANPolicy withdraws every LAN grant, for a project whose policy was refused.
func (r *Reconciler) removeLANPolicy(ctx context.Context, w *api.ProjectWorkspace) error {
	if _, ok := r.Network.(network.LANPolicy); !ok {
		return nil
	}
	current := &unstructured.Unstructured{}
	current.SetGroupVersionKind(ciliumPolicyGVK)
	err := r.Get(ctx, types.NamespacedName{Namespace: namespace(w), Name: network.LANPolicyName}, current)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if err = owner(w, current); err != nil {
		return err
	}
	if err = r.Delete(ctx, current); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return r.auditDetails(ctx, w, "lan-hosts-withdrawn", map[string]string{"message": "LAN host policy removed because the project policy was refused"})
}
