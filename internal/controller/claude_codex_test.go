package controller

import (
	"context"
	"slices"
	"strings"
	"testing"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/proxy"
)

// Claude's subscription token never reaches the worker, even with
// PROVIDER_CELL_TOKENS off; Codex keeps its ChatGPT account fields.
func TestClaudeAlwaysGetsACellTokenAndCodexKeepsItsAccount(t *testing.T) {
	ctx := context.Background()
	r, w := providerFixture(t, "claude", "codex")
	if len(r.Config.CellTokens) != 0 {
		t.Fatal("fixture has cell tokens on")
	}
	if err := r.validate(ctx, w); err != nil {
		t.Fatal(err)
	}
	if err := r.ensure(ctx, w); err != nil {
		t.Fatal(err)
	}
	model := secretData(t, r, w, "model-access")
	worker := grantsOf(t, model["provider-access"])
	if strings.Contains(string(model["provider-access"]), "sk-ant-oat01-claude-lease") {
		t.Fatal("real Claude token in the worker", string(model["provider-access"]))
	}
	c := worker["claude"]
	if !c.Enabled || !strings.HasPrefix(c.Access.Token, claudeCellTokenPrefix) || c.Access.CellToken != "" {
		t.Fatalf("worker claude = %#v", c.Access)
	}
	if x := worker["codex"]; !x.Enabled || x.Access.Token != "codex-lease" || x.Access.Account != "acct-1" || x.Access.Plan != "pro" {
		t.Fatalf("worker codex = %#v", x.Access)
	}
	if err := RejectRefreshMaterial(model); err != nil {
		t.Fatal("controller wrote a grant its own validation refuses", err)
	}
	gateway := grantsOf(t, secretData(t, r, w, "proxy-ca")["provider-access"])
	if g := gateway["claude"]; g.Access.Token != "sk-ant-oat01-claude-lease" || g.Access.CellToken != c.Access.Token {
		t.Fatalf("proxy claude = %#v", g.Access)
	}
	if hosts := providerHostsOf(t, r, w); !slices.Contains(hosts, "api.anthropic.com") || !slices.Contains(hosts, "chatgpt.com") {
		t.Fatal("provider policy hosts", hosts)
	}
}

// All four providers fit; a fixed HTTPS rule for a brokered host is refused,
// because it would bypass the grant check and the token swap.
func TestFourProvidersAndHostConflicts(t *testing.T) {
	r, w := providerFixture(t, "grok", "cursor", "codex", "claude")
	if err := r.validateProviders(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	for host, name := range map[string]string{"chatgpt.com": "codex", "api.anthropic.com": "claude"} {
		r, w := providerFixture(t, name)
		w.Spec.HTTPS = []api.HTTPSRule{{Host: host, Methods: []string{"POST"}, Paths: []string{"/"}}}
		if err := r.validateProviders(context.Background(), w); err == nil || !strings.Contains(err.Error(), "remove that HTTPS rule") {
			t.Fatal(host, err)
		}
	}
	for name := range proxy.BrokeredProviders {
		if !CellTokenProviders[name] {
			t.Fatal("brokered provider without cell token support", name)
		}
	}
}
