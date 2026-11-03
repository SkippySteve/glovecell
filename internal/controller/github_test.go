package controller

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/githubpolicy"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/proxy"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

var githubLeaseKey = types.NamespacedName{Namespace: "github-broker", Name: "github-lease"}

const githubToken = "ghu_ControllerTestToken0000000000001"

func githubFixture(t *testing.T, expires time.Time) (*Reconciler, *api.ProjectWorkspace) {
	t.Helper()
	r, w, _ := fixture(t)
	r.GitHubLease = &githubLeaseKey
	lease, _ := json.Marshal(proxy.GitHubLease{Enabled: true, Token: githubToken, ExpiresAt: expires.Unix(), Login: "SkippySteve"})
	if err := r.Create(context.Background(), &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: githubLeaseKey.Name, Namespace: githubLeaseKey.Namespace}, Data: map[string][]byte{proxy.GitHubAccessKey: lease}}); err != nil {
		t.Fatal(err)
	}
	return r, w
}

func TestGitHubOffWithoutLeaseSourceOrOptIn(t *testing.T) {
	ctx := context.Background()
	r, w, _ := fixture(t)
	w.Spec.GitHub = &api.GitHubAccess{Repositories: []string{"SkippySteve/app"}}
	if err := r.validate(ctx, w); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatal("spec.github without a lease source was not refused", err)
	}
	// With a lease source, a project that does not opt in gets nothing.
	r, w = githubFixture(t, time.Now().Add(5*time.Minute))
	if err := r.ensure(ctx, w); err != nil {
		t.Fatal(err)
	}
	ca := secretData(t, r, w, "proxy-ca")
	if ca[proxy.GitHubAccessKey] != nil || ca[proxy.GitHubPolicyKey] != nil || slices.Contains(providerHostsOf(t, r, w), "github.com") {
		t.Fatal("project without spec.github received GitHub access")
	}
}

func TestGitHubValidation(t *testing.T) {
	ctx := context.Background()
	r, w := githubFixture(t, time.Now().Add(5*time.Minute))
	for _, spec := range []*api.GitHubAccess{
		{Repositories: nil},
		{Repositories: []string{"no-slash"}},
		{Repositories: []string{"a/b"}, ProtectedBranches: []string{"*"}},
		{Repositories: []string{"a/b"}, Approvals: []api.GitHubApproval{{Action: "delete-repository", Name: "x", ExpiresAt: metav1.Now()}}},
	} {
		w.Spec.GitHub = spec
		if err := r.validate(ctx, w); err == nil {
			t.Error("accepted", spec)
		}
	}
}

func TestGitHubGrantCopiedOnlyToOptedInProxyAndRevoked(t *testing.T) {
	ctx := context.Background()
	r, w := githubFixture(t, time.Now().Add(5*time.Minute))
	w.Spec.GitHub = &api.GitHubAccess{Repositories: []string{"SkippySteve/app"}, PushBranches: []string{"agent/*"}}
	if err := r.validate(ctx, w); err != nil {
		t.Fatal(err)
	}
	if err := r.ensure(ctx, w); err != nil {
		t.Fatal(err)
	}
	ca := secretData(t, r, w, "proxy-ca")
	lease, err := proxy.ParseGitHubLease(ca[proxy.GitHubAccessKey])
	if err != nil || lease.Token != githubToken {
		t.Fatal("lease not copied", err)
	}
	policy, err := githubpolicy.Parse(ca[proxy.GitHubPolicyKey])
	if err != nil || policy.ProjectUID != string(w.UID) || !policy.RepoAllowed("SkippySteve/app") {
		t.Fatal("policy not written", err)
	}
	hosts := providerHostsOf(t, r, w)
	if !slices.Contains(hosts, "github.com") || !slices.Contains(hosts, "api.github.com") {
		t.Fatal("proxy cannot reach GitHub", hosts)
	}
	// The worker never receives the token.
	for _, name := range []string{"model-access", "git-identity"} {
		for _, v := range secretData(t, r, w, name) {
			if strings.Contains(string(v), githubToken) {
				t.Fatal("token in worker Secret", name)
			}
		}
	}
	// An approval applies without a restart.
	proxyPod(t, r, w)
	w.Spec.GitHub.Approvals = []api.GitHubApproval{{Action: "create-repository", Name: "new", Visibility: "private", ExpiresAt: metav1.NewTime(time.Now().Add(time.Hour))}}
	if _, err := r.applyProviders(ctx, w, nil, w.Spec.GitHub); err != nil {
		t.Fatal(err)
	}
	if !proxyPodExists(t, r, w) {
		t.Fatal("approval restarted the proxy")
	}
	// Revocation withdraws keys and policy and restarts the proxy.
	w.Spec.GitHub = nil
	if _, err := r.applyProviders(ctx, w, nil, nil); err != nil {
		t.Fatal(err)
	}
	ca = secretData(t, r, w, "proxy-ca")
	if ca[proxy.GitHubAccessKey] != nil || ca[proxy.GitHubPolicyKey] != nil || slices.Contains(providerHostsOf(t, r, w), "github.com") {
		t.Fatal("revocation left GitHub access")
	}
	if proxyPodExists(t, r, w) {
		t.Fatal("revocation did not restart the proxy")
	}
}

func TestGitHubExpiringLeaseCopiedDisabled(t *testing.T) {
	ctx := context.Background()
	r, w := githubFixture(t, time.Now().Add(30*time.Second))
	w.Spec.GitHub = &api.GitHubAccess{Repositories: []string{"SkippySteve/*"}}
	if err := r.ensure(ctx, w); err != nil {
		t.Fatal(err)
	}
	lease, err := proxy.ParseGitHubLease(secretData(t, r, w, "proxy-ca")[proxy.GitHubAccessKey])
	if err != nil || lease.Enabled || lease.Token != "" {
		t.Fatal("expiring lease copied", lease, err)
	}
}
