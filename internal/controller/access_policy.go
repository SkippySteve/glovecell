package controller

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/proxy"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const HTTPSLedgerKey = "https-policy"

// policyConfiguration keeps mandatory repository grants separate from optional egress.
func (r *Reconciler) policyConfiguration(w *api.ProjectWorkspace, https []api.HTTPSRule) (proxy.Config, error) {
	rules, private, err := AccessRules(r.Config.GitHost, r.Config.ForgejoOrigin, w.Spec.Repository)
	if err != nil {
		return proxy.Config{}, err
	}
	reserved := map[string]bool{}
	var privateGitHub []string
	for _, p := range w.Spec.Repositories {
		var extra []api.HTTPSRule
		if p.Host == "forgejo" {
			extra, _, err = AccessRules("forgejo", r.Config.ForgejoOrigin, p.Repository)
			if err == nil {
				extra[0].Paths = append(extra[0].Paths, "/"+p.Repository+"/info/refs", "/"+p.Repository+"/git-upload-pack", "/"+p.Repository+"/git-receive-pack")
			}
		} else {
			extra = []api.HTTPSRule{
				{Host: "github.com", Methods: []string{"GET"}, Paths: []string{"/" + p.Repository + ".git/info/refs"}},
				{Host: "github.com", Methods: []string{"POST"}, Paths: []string{"/" + p.Repository + ".git/git-upload-pack"}},
			}
			if !p.Public {
				privateGitHub = append(privateGitHub, p.Repository)
			}
		}
		if err != nil {
			return proxy.Config{}, err
		}
		rules = append(rules, extra...)
	}
	for _, rule := range rules {
		reserved[rule.Host] = true
	}
	for _, rule := range https {
		if reserved[rule.Host] {
			return proxy.Config{}, errors.New("repository access is controller-managed; HTTPS rules cannot override Git-host permissions")
		}
		if rule.Source != "" {
			return proxy.Config{}, errors.New("a project's HTTPS rules cannot carry a source label; reference rules come from the controller")
		}
	}
	cfg := proxy.Config{Rules: append(append([]api.HTTPSRule{}, rules...), https...), PrivateHosts: private, PrivateGitHubRepositories: privateGitHub}
	cfg.IndependentCodex = r.IndependentCodexWorkspaceUID != "" && r.IndependentCodexWorkspaceUID == string(w.UID)
	if cfg.IndependentCodex {
		cfg.BrokeredProviders = true
	}
	for _, rule := range https {
		if rule.Host == "cli-chat-proxy.grok.com" || rule.Host == "api2.cursor.sh" || rule.Host == "agentn.global.api5.cursor.sh" || rule.Host == "api.meta.ai" {
			cfg.BrokeredProviders = true
		}
		if rule.Host == "api.meta.ai" {
			cfg.MuseSubscriptionGateway = true
		}
	}
	if r.ReaderRoute != nil && r.ReaderWorkspaceUID == string(w.UID) {
		cfg.Reader = r.ReaderRoute
		cfg.Rules = append(cfg.Rules, api.HTTPSRule{Host: r.ReaderRoute.Host, Methods: []string{"GET", "HEAD"}, Paths: []string{"/k8s"}}, api.HTTPSRule{Host: r.ReaderRoute.Host, Methods: []string{"POST"}, Paths: []string{"/mcp"}})
	}
	// The deployment's reference rules come last (reference_rules.go).
	cfg = r.withReference(cfg)
	return cfg, proxy.Validate(cfg)
}

func (r *Reconciler) processesGone(ctx context.Context, w *api.ProjectWorkspace) (bool, error) {
	var pods corev1.PodList
	if err := r.List(ctx, &pods, client.InNamespace(namespace(w)), client.MatchingLabels{api.OwnerLabel: string(w.UID)}); err != nil {
		return false, err
	}
	return len(pods.Items) == 0, nil
}

// updateAccessPolicy accepts only a saved policy transition with all project processes stopped.
// The ledger remains on the old policy until objects() succeeds. After interruption, each
// resource must match either that old policy or the requested policy; arbitrary drift fails closed.
func (r *Reconciler) updateAccessPolicy(ctx context.Context, w *api.ProjectWorkspace, ledger *corev1.Secret) error {
	var previous []api.HTTPSRule
	desired, err := r.policyConfiguration(w, w.Spec.HTTPS)
	if err != nil {
		return err
	}
	desiredRaw, err := json.Marshal(desired)
	if err != nil {
		return err
	}
	cm := &corev1.ConfigMap{}
	cmErr := r.Get(ctx, types.NamespacedName{Namespace: namespace(w), Name: "proxy-policy"}, cm)
	if cmErr != nil && !apierrors.IsNotFound(cmErr) {
		return cmErr
	}
	if len(ledger.Data[HTTPSLedgerKey]) == 0 {
		// Existing installations initialize only from a policy that already matches the request.
		if cmErr == nil {
			if err = owner(w, cm); err != nil {
				return err
			}
			if !reflect.DeepEqual(cm.Data, map[string]string{"policy.json": string(desiredRaw)}) {
				return errors.New("access policy ledger missing; reconcile the original policy before editing access")
			}
		}
		return nil
	}
	if err = json.Unmarshal(ledger.Data[HTTPSLedgerKey], &previous); err != nil {
		return errors.New("access policy ledger is invalid")
	}
	var previousRepositories []api.RepositoryProject
	if raw := ledger.Data[RepositoriesLedgerKey]; len(raw) > 0 && json.Unmarshal(raw, &previousRepositories) != nil {
		return errors.New("saved repository policy is invalid")
	}
	oldWorkspace := *w
	oldWorkspace.Spec.Repositories = previousRepositories
	old, err := r.policyConfiguration(&oldWorkspace, previous)
	if err != nil {
		return errors.New("saved access policy is invalid")
	}
	oldRaw, _ := json.Marshal(old)
	if string(oldRaw) == string(desiredRaw) {
		return nil
	}
	if w.Spec.DesiredState != "Paused" {
		return errors.New("pause the project before changing HTTPS access")
	}
	if err = r.suspend(ctx, w); err != nil {
		return err
	}
	// Stop the proxy first, including streams that started under the previous policy.
	deployment := &appsv1.Deployment{}
	err = r.Get(ctx, types.NamespacedName{Namespace: namespace(w), Name: "proxy"}, deployment)
	if err == nil {
		if err = owner(w, deployment); err != nil {
			return err
		}
		if deployment.Spec.Replicas == nil || *deployment.Spec.Replicas != 0 {
			before := deployment.DeepCopy()
			deployment.Spec.Replicas = int32p(0)
			if err = r.Patch(ctx, deployment, client.MergeFrom(before)); err != nil {
				return err
			}
		}
	} else if !apierrors.IsNotFound(err) {
		return err
	}
	gone, err := r.processesGone(ctx, w)
	if err != nil {
		return err
	}
	if !gone {
		return errors.New("waiting for worker and proxy processes to stop before applying access")
	}
	oldObjects := r.Network.Objects(namespace(w), string(w.UID), policyHosts(old))
	newObjects := r.Network.Objects(namespace(w), string(w.UID), policyHosts(desired))
	if len(oldObjects) != len(newObjects) {
		return errors.New("network provider changed during access update")
	}
	// Validate every resource before any update, so tampered policy is never adopted.
	currentObjects := make([]*unstructured.Unstructured, len(newObjects))
	for i, want := range newObjects {
		if oldObjects[i].GetName() != want.GetName() || oldObjects[i].GroupVersionKind() != want.GroupVersionKind() {
			return errors.New("network provider changed during access update")
		}
		current := want.DeepCopy()
		if err = r.Get(ctx, client.ObjectKeyFromObject(want), current); err != nil {
			return err
		}
		if err = owner(w, current); err != nil {
			return err
		}
		if !same(current.Object["spec"], oldObjects[i].Object["spec"]) && !same(current.Object["spec"], want.Object["spec"]) {
			return errors.New("network policy drift detected during access update")
		}
		currentObjects[i] = current
	}
	if cmErr != nil {
		return errors.New("proxy policy missing during access update")
	}
	if err = owner(w, cm); err != nil {
		return err
	}
	oldData := map[string]string{"policy.json": string(oldRaw)}
	newData := map[string]string{"policy.json": string(desiredRaw)}
	if !reflect.DeepEqual(cm.Data, oldData) && !reflect.DeepEqual(cm.Data, newData) {
		return errors.New("proxy policy drift detected during access update")
	}
	for i, current := range currentObjects {
		if err = r.current(ctx, w); err != nil {
			return err
		}
		if same(current.Object["spec"], newObjects[i].Object["spec"]) {
			continue
		}
		current.Object["spec"] = newObjects[i].Object["spec"]
		if err = r.Update(ctx, current); err != nil {
			return err
		}
	}
	if err = r.current(ctx, w); err != nil {
		return err
	}
	if !reflect.DeepEqual(cm.Data, newData) {
		cm.Data = newData
		if err = r.Update(ctx, cm); err != nil {
			return err
		}
	}
	return nil
}

func (r *Reconciler) saveAccessPolicy(ctx context.Context, w *api.ProjectWorkspace, ledger *corev1.Secret) error {
	raw, err := json.Marshal(w.Spec.HTTPS)
	if err != nil {
		return err
	}
	repositories, _ := json.Marshal(w.Spec.Repositories)
	if string(ledger.Data[HTTPSLedgerKey]) == string(raw) && (len(w.Spec.Repositories) == 0 || string(ledger.Data[RepositoriesLedgerKey]) == string(repositories)) {
		return r.audit(ctx, w, "access-policy-applied")
	}
	if err = r.current(ctx, w); err != nil {
		return err
	}
	before := ledger.DeepCopy()
	ledger.Data[HTTPSLedgerKey] = raw
	if len(w.Spec.Repositories) > 0 {
		ledger.Data[RepositoriesLedgerKey] = repositories
	}
	if err = r.Patch(ctx, ledger, client.MergeFrom(before)); err != nil {
		return err
	}
	return r.audit(ctx, w, "access-policy-applied")
}
