package providerbroker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

// fourOwnerFixture adds Codex and Claude owners to leaseFixture. account is
// the ChatGPT account the Codex owner holds; claude is the Claude owner's file.
func fourOwnerFixture(t *testing.T, providers ...string) (*Broker, *string, *map[string]any) {
	t.Helper()
	b, _ := leaseFixture(t, providers...)
	now := b.now()
	account := "codex-account-1"
	claude := map[string]any{"accessToken": "sk-ant-oat01-real-owner-token", "expiresAt": now.Add(300 * 24 * time.Hour).Unix()}
	b.Config.Owners["codex"], b.Config.Owners["claude"] = Owner{}, Owner{}
	inner := b.Execute
	b.Execute = func(ctx context.Context, owner Owner, program string, input []byte) ([]byte, error) {
		if program == claudeOwnerRead {
			if claude == nil {
				return nil, errors.New("no owner file")
			}
			return json.Marshal(claude)
		}
		var args map[string]string
		_ = json.Unmarshal(input, &args)
		if program == oauthOwnerRead && args["provider"] == "codex" {
			c, _ := json.Marshal(map[string]any{"exp": now.Add(time.Hour).Unix(), "https://api.openai.com/auth": map[string]string{"chatgpt_account_id": account, "chatgpt_plan_type": "pro"}})
			token := "header." + base64.RawURLEncoding.EncodeToString(c) + ".signature"
			return json.Marshal(map[string]any{"sha256": "codex-sha", "document": map[string]any{"tokens": map[string]any{"access_token": token, "refresh_token": "private-refresh", "account_id": account}}})
		}
		return inner(ctx, owner, program, input)
	}
	return b, &account, &claude
}

func TestLeaseModePublishesCodexAndClaude(t *testing.T) {
	b, account, claude := fourOwnerFixture(t, "codex", "claude")
	if err := b.TickLeases(context.Background()); err != nil {
		t.Fatal(err)
	}
	grants, _ := published(t, b)
	limit := float64(b.now().Add(5 * time.Minute).Unix())
	if g := grants["codex"]; !g.Enabled || g.Access.Account != "codex-account-1" || g.Access.Plan != "pro" || g.Access.Expires != limit {
		t.Fatalf("codex lease = %#v", g.Access)
	}
	if g := grants["claude"]; !g.Enabled || g.Access.Token != "sk-ant-oat01-real-owner-token" || g.Access.Expires != limit || g.Access.Account != "" {
		t.Fatalf("claude lease = %#v", g.Access)
	}
	if g := grants["grok"]; g.Enabled {
		t.Fatal("unconfigured provider published")
	}
	var journal corev1.Secret
	_ = b.Client.Get(context.Background(), types.NamespacedName{Namespace: "broker", Name: "journal"}, &journal)
	if string(journal.Data[codexAccountKey]) != "codex-account-1" {
		t.Fatal("Codex account not pinned on first use")
	}

	// A different ChatGPT account is refused; the old lease runs out.
	*account = "someone-else"
	if err := b.TickLeases(context.Background()); err == nil {
		t.Fatal("changed Codex account accepted")
	}
	if grants, _ = published(t, b); grants["codex"].Enabled && grants["codex"].Access.Account != "codex-account-1" {
		t.Fatal("other account published", grants["codex"].Access)
	}

	// A Claude owner whose saved token expired, or that is not a subscription
	// token, publishes nothing new.
	(*claude)["expiresAt"] = b.now().Add(-time.Minute).Unix()
	if _, err := b.claudeOwner(context.Background()); err == nil {
		t.Fatal("expired Claude owner token accepted")
	}
	(*claude)["expiresAt"], (*claude)["accessToken"] = b.now().Add(time.Hour).Unix(), "sk-ant-api03-an-api-key"
	if _, err := b.claudeOwner(context.Background()); err == nil {
		t.Fatal("Claude API key accepted as a subscription token")
	}
	*claude = nil
	if _, err := b.claudeOwner(context.Background()); err == nil {
		t.Fatal("missing Claude owner accepted")
	}
}

func TestCurrentLeasesKeepsOnlyCodexAccount(t *testing.T) {
	exp := float64(time.Now().Add(time.Hour).Unix())
	raw, _ := json.Marshal(map[string]Grant{
		"codex":  {Enabled: true, Access: &Access{Token: "t", Expires: exp, Account: "a", Plan: "pro"}},
		"claude": {Enabled: true, Access: &Access{Token: "sk-ant-oat01-x", Expires: exp, Account: "smuggled"}},
	})
	got := currentLeases(raw)
	if got["codex"].Access == nil || got["codex"].Access.Account != "a" {
		t.Fatal("codex account dropped", got)
	}
	if _, ok := got["claude"]; ok {
		t.Fatal("account fields carried for a provider without accounts")
	}
	raw, _ = json.Marshal(map[string]Grant{"codex": {Enabled: true, Access: &Access{Token: "t", Expires: exp}}})
	if _, ok := currentLeases(raw)["codex"]; ok {
		t.Fatal("codex lease without an account carried forward")
	}
}
