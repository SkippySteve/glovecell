package web

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
)

func providerFixture(t *testing.T, enabled bool) *lanEnv {
	t.Helper()
	e := lanFixture(t, false)
	if enabled {
		e.s.Providers = &Providers{Status: func(context.Context) (map[string]bool, error) {
			return map[string]bool{"grok": true, "cursor": false}, nil
		}}
	}
	h, err := e.s.Handler()
	if err != nil {
		t.Fatal(err)
	}
	e.h = h
	return e
}

func (e *lanEnv) enable(t *testing.T, name string, modify ...func(*http.Request)) int {
	t.Helper()
	e.refresh(t)
	return e.do("POST", "/api/v1/workspaces/project/providers", `{"resourceVersion":"`+e.obj.ResourceVersion+`","name":"`+name+`"}`, modify...).Code
}

func (e *lanEnv) revoke(t *testing.T, name string, modify ...func(*http.Request)) int {
	t.Helper()
	e.refresh(t)
	return e.do("DELETE", "/api/v1/workspaces/project/providers/"+name, `{"resourceVersion":"`+e.obj.ResourceVersion+`"}`, modify...).Code
}

func TestProviderEnableAndRevokeByOIDCOperator(t *testing.T) {
	e := providerFixture(t, true)
	if code := e.enable(t, "grok"); code != 201 {
		t.Fatal(code)
	}
	e.refresh(t)
	if len(e.obj.Spec.Providers) != 1 || e.obj.Spec.Providers[0].Name != "grok" || e.obj.Spec.Providers[0].AddedBy != "https://auth.example.com#steve" || e.obj.Spec.Providers[0].AddedAt == nil {
		t.Fatalf("providers = %#v", e.obj.Spec.Providers)
	}
	if e.obj.Spec.DesiredState != "Running" {
		t.Fatal("opt-in changed lifecycle")
	}
	if code := e.enable(t, "grok"); code != 409 {
		t.Fatal("duplicate opt-in accepted", code)
	}
	if code := e.enable(t, "codex"); code != 400 {
		t.Fatal("unreviewed provider accepted", code)
	}
	if got := e.audits(t, "provider-enabled"); len(got) != 1 || got[0]["provider"] != "grok" || got[0]["actor"] != "https://auth.example.com#steve" {
		t.Fatalf("audit = %#v", got)
	}
	if code := e.revoke(t, "cursor"); code != 404 {
		t.Fatal("unknown revocation", code)
	}
	if code := e.revoke(t, "grok"); code != 200 {
		t.Fatal(code)
	}
	e.refresh(t)
	if len(e.obj.Spec.Providers) != 0 {
		t.Fatal("revocation kept the provider")
	}
	if got := e.audits(t, "provider-revoked"); len(got) != 1 || !strings.Contains(got[0]["message"], "revoked by") {
		t.Fatalf("audit = %#v", got)
	}
}

func TestProviderOptInNeedsOIDCOperatorAndLeaseSource(t *testing.T) {
	e := providerFixture(t, true)
	bearer := func(r *http.Request) {
		r.Header.Del("Cookie")
		r.Header.Set("Authorization", "Bearer "+strings.Repeat("a", 32))
	}
	if code := e.enable(t, "grok", bearer); code != http.StatusForbidden {
		t.Fatal("automation credential opted a project in", code)
	}
	off := providerFixture(t, false)
	if code := off.enable(t, "grok"); code != http.StatusForbidden {
		t.Fatal("opt-in without a lease source", code)
	}
	// Revocation still works with the feature off.
	off.refresh(t)
	off.obj.Spec.Providers = []api.ProviderGrant{{Name: "grok"}}
	if err := off.s.Client.Update(context.Background(), off.obj); err != nil {
		t.Fatal(err)
	}
	if code := off.revoke(t, "grok"); code != 200 {
		t.Fatal("revocation refused with the feature off", code)
	}
}

func TestProviderLeaseStatusHasNoTokens(t *testing.T) {
	e := providerFixture(t, true)
	rec := e.do("GET", "/api/v1/provider-leases", "")
	if rec.Code != 200 {
		t.Fatal(rec.Code)
	}
	var body map[string]any
	if json.Unmarshal(rec.Body.Bytes(), &body) != nil || body["configured"] != true {
		t.Fatal(rec.Body.String())
	}
	leases := body["leases"].(map[string]any)
	if leases["grok"] != true || leases["cursor"] != false || len(leases) != 2 {
		t.Fatal(leases)
	}
	session := e.do("GET", "/api/v1/session", "")
	if !strings.Contains(session.Body.String(), `"providers":["grok","cursor"]`) {
		t.Fatal("session does not advertise providers", session.Body.String())
	}
}
