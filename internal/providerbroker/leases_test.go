package providerbroker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// leaseFixture is a lease-mode broker with Grok and Cursor owners. cursorSub
// is the account the Cursor owner currently holds.
func leaseFixture(t *testing.T, providers ...string) (*Broker, *string) {
	t.Helper()
	now := time.Now().UTC()
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	sub := "cursor-account"
	grok := map[string]map[string]any{"https://auth.x.ai::fixture-client": {"oidc_issuer": "https://auth.x.ai", "oidc_client_id": "fixture-client", "key": "grok-access", "refresh_token": "never-deliver-refresh", "expires_at": now.Add(time.Hour).Format(time.RFC3339Nano)}}
	b := &Broker{Client: c, Config: Config{Control: "broker", Journal: "journal", Leases: "provider-leases", Providers: providers, Owners: map[string]Owner{"grok": {}, "cursor": {}}}, Now: func() time.Time { return now }, Execute: func(_ context.Context, _ Owner, program string, input []byte) ([]byte, error) {
		if program == ownerWrite || program == oauthOwnerWrite {
			return []byte("saved"), nil
		}
		var args map[string]string
		_ = json.Unmarshal(input, &args)
		if args["provider"] == "cursor" {
			claims, _ := json.Marshal(map[string]any{"exp": now.Add(time.Hour).Unix(), "sub": sub})
			token := "header." + base64.RawURLEncoding.EncodeToString(claims) + ".signature"
			return json.Marshal(map[string]any{"sha256": "fixture-sha", "document": map[string]string{"accessToken": token, "refreshToken": "private-refresh"}})
		}
		return json.Marshal(snapshot{SHA: "fixture-sha", Document: grok})
	}}
	return b, &sub
}

func published(t *testing.T, b *Broker) (map[string]Grant, *corev1.Secret) {
	t.Helper()
	var leases corev1.Secret
	if err := b.Client.Get(context.Background(), types.NamespacedName{Namespace: "broker", Name: "provider-leases"}, &leases); err != nil {
		t.Fatal(err)
	}
	raw := string(leases.Data[LeasesKey])
	if strings.Contains(strings.ToLower(raw), "refresh") || strings.Contains(raw, "sub") {
		t.Fatal("refresh or account material published", raw)
	}
	var grants map[string]Grant
	if err := json.Unmarshal(leases.Data[LeasesKey], &grants); err != nil {
		t.Fatal(err)
	}
	return grants, &leases
}

func TestLeaseModePublishesBoundedLeases(t *testing.T) {
	b, _ := leaseFixture(t, "grok", "cursor")
	if err := b.TickLeases(context.Background()); err != nil {
		t.Fatal(err)
	}
	grants, leases := published(t, b)
	if leases.Labels[LeasesLabel] != "provider-leases" {
		t.Fatal("lease Secret not bound to this broker")
	}
	limit := float64(b.now().Add(5 * time.Minute).Unix())
	if g := grants["grok"]; !g.Enabled || g.Access.Token != "grok-access" || g.Access.Issuer != "https://auth.x.ai" || g.Access.Expires != limit {
		t.Fatalf("grok lease = %#v", g.Access)
	}
	if g := grants["cursor"]; !g.Enabled || g.Access.Expires != limit || g.Access.Issuer != "" {
		t.Fatalf("cursor lease = %#v", g.Access)
	}
	var journal corev1.Secret
	if err := b.Client.Get(context.Background(), types.NamespacedName{Namespace: "broker", Name: "journal"}, &journal); err != nil {
		t.Fatal(err)
	}
	if journal.Labels[LeasesLabel] != "provider-leases" || string(journal.Data[cursorSubjectKey]) != "cursor-account" {
		t.Fatal("journal not bound or Cursor account not pinned")
	}
}

func TestLeaseModeUnconfiguredProviderIsDisabled(t *testing.T) {
	b, _ := leaseFixture(t, "grok")
	b.Execute = func(_ context.Context, _ Owner, program string, input []byte) ([]byte, error) {
		if strings.Contains(string(input), "cursor") {
			t.Fatal("read an owner that is not configured")
		}
		original, _ := leaseFixture(t, "grok")
		return original.Execute(context.Background(), Owner{}, program, input)
	}
	if err := b.TickLeases(context.Background()); err != nil {
		t.Fatal(err)
	}
	grants, _ := published(t, b)
	if grants["cursor"].Enabled || grants["cursor"].Access != nil || !grants["grok"].Enabled {
		t.Fatalf("grants = %#v", grants)
	}
}

func TestLeaseModeOwnerOutageLetsLeaseRunOut(t *testing.T) {
	b, _ := leaseFixture(t, "grok", "cursor")
	ctx := context.Background()
	if err := b.TickLeases(ctx); err != nil {
		t.Fatal(err)
	}
	original := b.Execute
	b.Execute = func(ctx context.Context, o Owner, program string, input []byte) ([]byte, error) {
		if strings.Contains(string(input), "grok") {
			return nil, errors.New("owner offline")
		}
		return original(ctx, o, program, input)
	}
	start := b.now()
	b.Now = func() time.Time { return start.Add(time.Minute) }
	if b.TickLeases(ctx) == nil {
		t.Fatal("outage not reported")
	}
	grants, _ := published(t, b)
	if !grants["grok"].Enabled || grants["grok"].Access.Expires != float64(start.Add(5*time.Minute).Unix()) {
		t.Fatal("previous lease was extended or dropped early", grants["grok"].Access)
	}
	if grants["cursor"].Access.Expires != float64(start.Add(6*time.Minute).Unix()) {
		t.Fatal("outage blocked the other provider")
	}
	b.Now = func() time.Time { return start.Add(6 * time.Minute) }
	_ = b.TickLeases(ctx)
	if grants, _ = published(t, b); grants["grok"].Enabled {
		t.Fatal("expired lease still published")
	}
}

func TestLeaseModeRefusesAnotherCursorAccount(t *testing.T) {
	b, sub := leaseFixture(t, "cursor")
	ctx := context.Background()
	if err := b.TickLeases(ctx); err != nil {
		t.Fatal(err)
	}
	*sub = "another-account"
	start := b.now()
	b.Now = func() time.Time { return start.Add(6 * time.Minute) }
	if err := b.TickLeases(ctx); err == nil {
		t.Fatal("account substitution accepted")
	}
	if grants, _ := published(t, b); grants["cursor"].Enabled {
		t.Fatal("substituted account published")
	}
}

func TestLeaseModeNeverAdoptsForeignSecrets(t *testing.T) {
	b, _ := leaseFixture(t, "grok")
	foreign := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "provider-leases", Namespace: "broker"}, Data: map[string][]byte{"other": []byte("x")}}
	if err := b.Client.Create(context.Background(), foreign); err != nil {
		t.Fatal(err)
	}
	b.Execute = func(context.Context, Owner, string, []byte) ([]byte, error) {
		t.Fatal("read an owner for a foreign Secret")
		return nil, nil
	}
	if b.TickLeases(context.Background()) == nil {
		t.Fatal("foreign Secret adopted")
	}
}

func TestLeaseModeConfiguration(t *testing.T) {
	set := func(env map[string]string) {
		for _, key := range []string{"PROVIDER_BROKER_CONTROL_NAMESPACE", "PROVIDER_BROKER_JOURNAL", "PROVIDER_BROKER_LEASES", "PROVIDER_BROKER_PROVIDERS", "PROVIDER_BROKER_WORKSPACE", "PROVIDER_BROKER_WORKSPACE_UID", "GROK_OWNER_NAMESPACE", "GROK_OWNER_POD", "GROK_OWNER_CONTAINER", "CURSOR_OWNER_NAMESPACE", "CURSOR_OWNER_POD", "CURSOR_OWNER_CONTAINER", "CODEX_OWNER_NAMESPACE", "CODEX_OWNER_POD", "CODEX_OWNER_CONTAINER"} {
			t.Setenv(key, env[key])
		}
	}
	base := map[string]string{"PROVIDER_BROKER_CONTROL_NAMESPACE": "broker", "PROVIDER_BROKER_JOURNAL": "journal", "PROVIDER_BROKER_LEASES": "provider-leases", "PROVIDER_BROKER_PROVIDERS": "grok", "GROK_OWNER_NAMESPACE": "owners", "GROK_OWNER_POD": "grok", "GROK_OWNER_CONTAINER": "grok", "PROVIDER_BROKER_WORKSPACE": "ignored"}
	set(base)
	cfg, err := configFromEnv()
	if err != nil || cfg.Leases != "provider-leases" || len(cfg.Owners) != 1 || cfg.Workspace != "" {
		t.Fatal("lease mode configuration", cfg, err)
	}
	for key, value := range map[string]string{"PROVIDER_BROKER_PROVIDERS": "codex", "PROVIDER_BROKER_LEASES": "journal", "GROK_OWNER_POD": ""} {
		bad := map[string]string{}
		for k, v := range base {
			bad[k] = v
		}
		bad[key] = value
		set(bad)
		if _, err := configFromEnv(); err == nil {
			t.Fatal("accepted", key, value)
		}
	}
	bad := map[string]string{}
	for k, v := range base {
		bad[k] = v
	}
	bad["PROVIDER_BROKER_PROVIDERS"] = "grok,grok"
	set(bad)
	if _, err := configFromEnv(); err == nil {
		t.Fatal("accepted repeated provider")
	}
}
