package controller

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/forgejo"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/proxy"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

type failingProjectHost struct {
	*host
	interrupted bool
}

func (h *failingProjectHost) Grant(ctx context.Context, repo string, c forgejo.Credentials) (string, error) {
	if repo == "steve/extra" && h.interrupted {
		return "", errors.New("grant interrupted")
	}
	return h.host.Grant(ctx, repo, c)
}
func TestEnrollmentRecoveryPreservesOriginalVolumeAndScopes(t *testing.T) {
	ctx := context.Background()
	r, w, h := fixture(t)
	if err := r.ensure(ctx, w); err != nil {
		t.Fatal(err)
	}
	var claim corev1.PersistentVolumeClaim
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace(w), Name: "workspace"}, &claim); err != nil {
		t.Fatal(err)
	}
	original := claim.DeepCopy()
	w.Spec.DesiredState = "Paused"
	w.Spec.Repositories = []api.RepositoryProject{{Name: "extra", Host: "forgejo", Repository: "steve/extra"}, {Name: "library", Host: "github", Repository: "upstream/library", Public: true}}
	w.Generation++
	if err := r.Update(ctx, w); err != nil {
		t.Fatal(err)
	}
	broken := &failingProjectHost{host: h, interrupted: true}
	r.Host = broken
	if err := r.ensure(ctx, w); err == nil {
		t.Fatal("interrupted grant reported success")
	}
	var ledger corev1.Secret
	key := types.NamespacedName{Namespace: r.Config.Namespace, Name: projectIdentity(w, w.Spec.Repositories[0])}
	if err := r.Get(ctx, key, &ledger); err != nil {
		t.Fatal("recovery intent missing", err)
	}
	password := string(ledger.Data["password"])
	broken.interrupted = false
	restarted := *r
	r = &restarted
	if err := r.ensure(ctx, w); err != nil {
		t.Fatal("retry", err)
	}
	if err := r.Get(ctx, key, &ledger); err != nil {
		t.Fatal(err)
	}
	if string(ledger.Data["password"]) != password || len(ledger.Data["token"]) == 0 {
		t.Fatal("recovery changed identity or failed to save grant")
	}
	var cfg proxy.Config
	var policy corev1.ConfigMap
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace(w), Name: "proxy-policy"}, &policy); err != nil {
		t.Fatal(err)
	}
	if json.Unmarshal([]byte(policy.Data["policy.json"]), &cfg) != nil {
		t.Fatal("invalid policy")
	}
	for _, test := range []struct {
		host, method, path string
		allowed            bool
	}{
		{"git.example.com", "POST", "/api/v1/repos/steve/extra/pulls", true},
		{"git.example.com", "GET", "/steve/extra/info/refs", true},
		{"git.example.com", "GET", "/api/v1/repos/steve/unapproved", false},
		{"git.example.com", "GET", "/api/v1/admin/users", false},
		{"github.com", "POST", "/upstream/library.git/git-upload-pack", true},
		{"github.com", "POST", "/upstream/library.git/git-receive-pack", false},
		{"github.com", "GET", "/upstream/other.git/info/refs", false},
	} {
		if proxy.Allowed(cfg, test.host, test.method, test.path) != test.allowed {
			t.Fatalf("unexpected access: %+v", test)
		}
	}
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace(w), Name: "workspace"}, &claim); err != nil {
		t.Fatal(err)
	}
	if claim.ResourceVersion != original.ResourceVersion {
		t.Fatal("existing volume changed during enrollment")
	}
	h.failRevoke = true
	if err := r.revokeRepositories(ctx, w); err == nil {
		t.Fatal("revocation failure ignored")
	}
	h.failRevoke = false
	if err := r.revokeRepositories(ctx, w); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(ctx, key, &ledger); err != nil {
		t.Fatal(err)
	}
	if string(ledger.Data["revoked"]) != "true" || len(ledger.Data["password"])+len(ledger.Data["token"]) != 0 {
		t.Fatal("revocation not verified or credentials retained")
	}
	if err := r.revokeRepositories(ctx, w); err != nil {
		t.Fatal("revocation retry", err)
	}
}
func TestUnsupportedRepositoryPoliciesFailClosed(t *testing.T) {
	for _, p := range []api.RepositoryProject{
		{Name: "private", Host: "github", Repository: "owner/private"},
		{Name: "other", Host: "forgejo", Repository: "other/private"},
		{Name: "../../escape", Host: "forgejo", Repository: "steve/extra"},
		{Name: "ssh", Host: "ssh", Repository: "steve/extra"},
	} {
		if ValidateProject(p, "steve", "forgejo") == nil {
			t.Fatalf("accepted unsupported enrollment %+v", p)
		}
	}
}

func TestPrivateGitHubTokenRemainsOnlyInGateway(t *testing.T) {
	ctx := context.Background()
	r, w, _ := fixture(t)
	r.Config.GitHubOwner = "github-owner"
	r.GitHubToken = "fixture-github-private-grant"
	p := api.RepositoryProject{Name: "private", Host: "github", Repository: "github-owner/private"}
	if err := ValidateProject(p, r.Config.Owner, r.Config.GitHost, r.Config.GitHubOwner); err != nil {
		t.Fatal(err)
	}
	w.Spec.DesiredState = "Paused"
	w.Spec.Repositories = []api.RepositoryProject{p}
	w.Generation++
	if err := r.Update(ctx, w); err != nil {
		t.Fatal(err)
	}
	// Supply a saved disposable authorization receipt, avoiding any real GitHub account in a unit test.
	s, err := r.state(ctx, w)
	if err != nil {
		t.Fatal(err)
	}
	s.Data[RepositoriesLedgerKey], _ = json.Marshal(w.Spec.Repositories)
	if err := r.Update(ctx, s); err != nil {
		t.Fatal(err)
	}
	if err := r.enrollRepositories(ctx, w, s); err != nil {
		t.Fatal(err)
	}
	// Prepare resources directly; no network grants or real repositories are contacted.
	if err := r.objects(ctx, w, s); err != nil {
		t.Fatal(err)
	}
	var worker, gateway corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace(w), Name: "git-identity"}, &worker); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace(w), Name: "proxy-ca"}, &gateway); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(worker.Data)
	if string(gateway.Data["github-token"]) != r.GitHubToken || strings.Contains(string(worker.Data["projects.json"]), r.GitHubToken) || len(worker.Data["github-token"]) > 0 || len(raw) == 0 {
		t.Fatal("GitHub credential entered worker identity")
	}
}
