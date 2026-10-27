package controller

// Reference rules (README "Reference rules"): a deployment-level list of
// read-only rules every project's proxy gets, from Config.ReferenceRules.
// They are ordinary rules in the project's proxy policy, labelled with
// Source and placed after every other rule. Changing the list does not need
// the project paused: syncReferenceRules swaps only the labelled rules in the
// proxy policy and its network policy, and restarts the proxy so open
// connections end under the new list.

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/proxy"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// policyHosts lists the hosts the network policy admits for cfg: every
// project and controller rule's host as before (order and repeats kept, so
// existing policies do not change), then each reference host not yet listed.
func policyHosts(cfg proxy.Config) []string {
	hosts := []string{}
	for _, r := range cfg.Rules {
		if r.Source == "" {
			hosts = append(hosts, r.Host)
		}
	}
	for _, r := range cfg.Rules {
		if r.Source != "" && !slices.Contains(hosts, r.Host) {
			hosts = append(hosts, r.Host)
		}
	}
	return hosts
}

// withReference replaces the labelled rules of cfg with the current list.
func (r *Reconciler) withReference(cfg proxy.Config) proxy.Config {
	rules := slices.DeleteFunc(slices.Clone(cfg.Rules), func(rule api.HTTPSRule) bool { return rule.Source != "" })
	cfg.Rules = append(rules, r.Config.ReferenceRules...)
	return cfg
}

// syncReferenceRules brings an existing project's proxy policy and network
// policy in step with the reference list when only the labelled rules
// differ. Anything else that differs is left to updateAccessPolicy and the
// drift checks, unchanged.
func (r *Reconciler) syncReferenceRules(ctx context.Context, w *api.ProjectWorkspace) error {
	cm := &corev1.ConfigMap{}
	err := r.Get(ctx, types.NamespacedName{Namespace: namespace(w), Name: "proxy-policy"}, cm)
	if apierrors.IsNotFound(err) {
		return nil // objects() creates it with the current list
	}
	if err != nil {
		return err
	}
	if err = owner(w, cm); err != nil {
		return err
	}
	var current proxy.Config
	if len(cm.Data) != 1 || json.Unmarshal([]byte(cm.Data["policy.json"]), &current) != nil {
		return errors.New("proxy policy drift detected")
	}
	want := r.withReference(current)
	currentRaw, _ := json.Marshal(current)
	wantRaw, _ := json.Marshal(want)
	if string(currentRaw) == string(wantRaw) {
		return nil
	}
	if err = proxy.Validate(want); err != nil {
		return err
	}
	ns := namespace(w)
	oldObjects := r.Network.Objects(ns, string(w.UID), policyHosts(current))
	newObjects := r.Network.Objects(ns, string(w.UID), policyHosts(want))
	if len(oldObjects) != len(newObjects) {
		return errors.New("network provider changed during reference update")
	}
	// Check every network policy before changing anything: each must match
	// the policy the proxy runs now or the one it moves to.
	currentObjects := make([]*unstructured.Unstructured, len(newObjects))
	for i, next := range newObjects {
		if oldObjects[i].GetName() != next.GetName() || oldObjects[i].GroupVersionKind() != next.GroupVersionKind() {
			return errors.New("network provider changed during reference update")
		}
		got := next.DeepCopy()
		if err = r.Get(ctx, client.ObjectKeyFromObject(next), got); apierrors.IsNotFound(err) {
			currentObjects[i] = nil // objects() creates it
			continue
		} else if err != nil {
			return err
		}
		if err = owner(w, got); err != nil {
			return err
		}
		if !same(got.Object["spec"], oldObjects[i].Object["spec"]) && !same(got.Object["spec"], next.Object["spec"]) {
			return errors.New("network policy drift detected during reference update")
		}
		currentObjects[i] = got
	}
	// Network policy first, then the proxy policy, then a proxy restart: in
	// between, the running proxy still enforces its old rules, so a removed
	// rule is refused by one layer or the other and an added one waits.
	for i, got := range currentObjects {
		if got == nil || same(got.Object["spec"], newObjects[i].Object["spec"]) {
			continue
		}
		got.Object["spec"] = newObjects[i].Object["spec"]
		if err = r.Update(ctx, got); err != nil {
			return err
		}
	}
	if !reflect.DeepEqual(cm.Data, map[string]string{"policy.json": string(wantRaw)}) {
		cm.Data = map[string]string{"policy.json": string(wantRaw)}
		if err = r.Update(ctx, cm); err != nil {
			return err
		}
	}
	// The proxy reads its policy at start.
	if err = r.restartRole(ctx, w, "proxy"); err != nil {
		return err
	}
	digest := r.Config.ReferenceDigest
	if digest == "" {
		digest = "none"
	}
	return r.auditDetails(ctx, w, "reference-rules-"+digest, map[string]string{"message": "Reference rules applied: " + digest})
}
