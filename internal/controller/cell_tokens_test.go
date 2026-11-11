package controller

import (
	"context"
	"strings"
	"testing"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestParseCellTokenProviders(t *testing.T) {
	if got, err := ParseCellTokenProviders(""); err != nil || got != nil {
		t.Fatal("empty setting", got, err)
	}
	got, err := ParseCellTokenProviders(" grok, cursor ,")
	if err != nil || !got["grok"] || !got["cursor"] || len(got) != 2 {
		t.Fatal(got, err)
	}
	for _, bad := range []string{"codex", "muse", "grok,openai"} {
		if _, err := ParseCellTokenProviders(bad); err == nil {
			t.Fatal("accepted", bad)
		}
	}
}

func TestCellTokenShape(t *testing.T) {
	key, _ := cellTokenKey([]byte("ca key one"))
	other, _ := cellTokenKey([]byte("ca key two"))
	opaque := cellToken(key, "grok", "grok-lease")
	if !strings.HasPrefix(opaque, cellTokenPrefix) || strings.Contains(opaque, "grok-lease") {
		t.Fatal("opaque token", opaque)
	}
	if opaque != cellToken(key, "grok", "grok-lease") {
		t.Fatal("cell token is not stable")
	}
	if opaque == cellToken(other, "grok", "grok-lease") || opaque == cellToken(key, "cursor", "grok-lease") || opaque == cellToken(key, "grok", "grok-next") {
		t.Fatal("cell token does not depend on cell, provider and real token")
	}
	jwt := "eyJhbGciOiJIUzI1NiJ9.eyJleHAiOjF9.realsignature"
	got := cellToken(key, "cursor", jwt)
	parts := strings.Split(got, ".")
	if len(parts) != 3 || parts[0]+"."+parts[1] != "eyJhbGciOiJIUzI1NiJ9.eyJleHAiOjF9" || parts[2] == "realsignature" {
		t.Fatal("JWT cell token", got)
	}
	if _, err := cellTokenKey(nil); err == nil {
		t.Fatal("empty CA key accepted")
	}
}

// Two cells opted into the same provider get different worker tokens, never
// the real one; each proxy holds the real token and its own cell token.
func TestCellTokensKeepTheRealTokenOutOfWorkers(t *testing.T) {
	ctx := context.Background()
	r, a := providerFixture(t, "grok", "cursor")
	r.Config.CellTokens = map[string]bool{"grok": true}
	b := &api.ProjectWorkspace{ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "control", UID: types.UID("abcdefab-1234-1234-1234-123456789012"), Generation: 1, Finalizers: []string{api.Finalizer}}, Spec: api.WorkspaceSpec{Repository: "steve/other", DesiredState: "Running", Providers: []api.ProviderGrant{{Name: "grok"}}}}
	if err := r.Create(ctx, b); err != nil {
		t.Fatal(err)
	}
	tokens := map[string]string{}
	for _, w := range []*api.ProjectWorkspace{a, b, a} { // a twice: stable across reconciles
		if err := r.ensure(ctx, w); err != nil {
			t.Fatal(err)
		}
		model := secretData(t, r, w, "model-access")
		if err := RejectRefreshMaterial(model); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(model["provider-access"]), "cellToken") || strings.Contains(string(model["provider-access"]), "grok-lease") {
			t.Fatalf("worker sees the real token or the gateway field: %s", model["provider-access"])
		}
		worker := grantsOf(t, model["provider-access"])["grok"]
		if !worker.Enabled || !strings.HasPrefix(worker.Access.Token, cellTokenPrefix) || worker.Access.Issuer != "https://auth.x.ai" {
			t.Fatalf("worker grok grant %#v", worker.Access)
		}
		gateway := grantsOf(t, secretData(t, r, w, "proxy-ca")["provider-access"])["grok"]
		if gateway.Access.Token != "grok-lease" || gateway.Access.CellToken != worker.Access.Token {
			t.Fatalf("proxy grok grant %#v", gateway.Access)
		}
		if prev, ok := tokens[w.Name]; ok && prev != worker.Access.Token {
			t.Fatal("cell token changed between reconciles")
		}
		tokens[w.Name] = worker.Access.Token
	}
	if tokens["project"] == tokens["other"] {
		t.Fatal("two cells share a token")
	}
	// Cursor is not listed: unchanged shared behaviour.
	model := secretData(t, r, a, "model-access")
	if c := grantsOf(t, model["provider-access"])["cursor"]; c.Access.Token != "cursor-lease" || c.Access.CellToken != "" {
		t.Fatalf("unlisted provider changed: %#v", c.Access)
	}
	if err := validateProviderAccess(model["provider-access"]); err != nil {
		t.Fatal("worker document fails validation", err)
	}
}

// No key, no grant: a cell whose ledger has no CA key gets the provider
// disabled, never the shared real token.
func TestCellTokensFailClosedWithoutKey(t *testing.T) {
	ctx := context.Background()
	r, w := providerFixture(t, "grok")
	if err := r.ensure(ctx, w); err != nil {
		t.Fatal(err)
	}
	r.Config.CellTokens = map[string]bool{"grok": true}
	ledger := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: "control", Name: identity(w)}, ledger); err != nil {
		t.Fatal(err)
	}
	delete(ledger.Data, "ca.key")
	if err := r.Update(ctx, ledger); err != nil {
		t.Fatal(err)
	}
	grants := r.classLeases(ctx, w, []string{"grok"})
	worker, gateway := r.withCellTokens(ctx, w, grants)
	if worker["grok"].Enabled || worker["grok"].Access != nil || gateway["grok"].Enabled {
		t.Fatalf("missing key did not fail closed: %#v %#v", worker["grok"], gateway["grok"])
	}
}
