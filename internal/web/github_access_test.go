package web

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func githubFixture(t *testing.T, enabled bool) *lanEnv {
	t.Helper()
	e := lanFixture(t, false)
	e.s.GitHubBrokering = enabled
	h, err := e.s.Handler()
	if err != nil {
		t.Fatal(err)
	}
	e.h = h
	return e
}

func (e *lanEnv) github(t *testing.T, method, path, body string, modify ...func(*http.Request)) int {
	t.Helper()
	e.refresh(t)
	version := `"resourceVersion":"` + e.obj.ResourceVersion + `"`
	if body == "{}" {
		body = "{" + version + "}"
	} else {
		body = strings.Replace(body, "{", "{"+version+",", 1)
	}
	return e.do(method, "/api/v1/workspaces/project/github"+path, body, modify...).Code
}

func TestGitHubOptInApprovalAndRevocation(t *testing.T) {
	e := githubFixture(t, true)
	if code := e.github(t, "POST", "/approvals", `{"action":"create-repository","name":"x","visibility":"private"}`); code != http.StatusConflict {
		t.Fatal("approval before opt-in", code)
	}
	if code := e.github(t, "PUT", "", `{"repositories":["bad"]}`); code != 400 {
		t.Fatal("invalid policy accepted", code)
	}
	if code := e.github(t, "PUT", "", `{"repositories":["SkippySteve/*"],"pushBranches":["agent/*"]}`); code != 200 {
		t.Fatal(code)
	}
	e.refresh(t)
	if g := e.obj.Spec.GitHub; g == nil || g.Repositories[0] != "SkippySteve/*" || g.AddedBy != "https://auth.example.com#steve" || g.AllowMerge {
		t.Fatalf("github = %#v", g)
	}
	for _, bad := range []string{
		`{"action":"delete-repository","name":"x"}`,
		`{"action":"create-repository","name":"x","visibility":"internal"}`,
		`{"action":"create-repository","name":"x","visibility":"private","minutes":5000}`,
		`{"action":"fork-repository","name":"x"}`,
	} {
		if code := e.github(t, "POST", "/approvals", bad); code != 400 {
			t.Fatal("bad approval accepted", bad, code)
		}
	}
	if code := e.github(t, "POST", "/approvals", `{"action":"create-repository","name":"tool","visibility":"private","minutes":30}`); code != 201 {
		t.Fatal(code)
	}
	e.refresh(t)
	a := e.obj.Spec.GitHub.Approvals
	if len(a) != 1 || a[0].ApprovedBy != "https://auth.example.com#steve" || a[0].ExpiresAt.Time.After(time.Now().Add(31*time.Minute)) {
		t.Fatalf("approvals = %#v", a)
	}
	if got := e.audits(t, "github-approved"); len(got) != 1 || !strings.Contains(got[0]["message"], "create-repository tool (private)") {
		t.Fatalf("audit = %#v", got)
	}
	// Replacing the policy keeps approvals.
	if code := e.github(t, "PUT", "", `{"repositories":["SkippySteve/app"]}`); code != 200 {
		t.Fatal(code)
	}
	e.refresh(t)
	if len(e.obj.Spec.GitHub.Approvals) != 1 {
		t.Fatal("policy change dropped approvals")
	}
	if code := e.github(t, "DELETE", "", `{}`); code != 200 {
		t.Fatal(code)
	}
	e.refresh(t)
	if e.obj.Spec.GitHub != nil || len(e.audits(t, "github-revoked")) != 1 {
		t.Fatal("revocation incomplete")
	}
}

func TestGitHubOptInNeedsOIDCOperatorAndBroker(t *testing.T) {
	e := githubFixture(t, true)
	bearer := func(r *http.Request) {
		r.Header.Del("Cookie")
		r.Header.Set("Authorization", "Bearer "+strings.Repeat("a", 32))
	}
	if code := e.github(t, "PUT", "", `{"repositories":["SkippySteve/app"]}`, bearer); code != http.StatusForbidden {
		t.Fatal("automation credential opted in", code)
	}
	off := githubFixture(t, false)
	if code := off.github(t, "PUT", "", `{"repositories":["SkippySteve/app"]}`); code != http.StatusForbidden {
		t.Fatal("opt-in without a broker", code)
	}
	off.refresh(t)
	off.obj.Spec.GitHub = &api.GitHubAccess{Repositories: []string{"SkippySteve/app"}, Approvals: []api.GitHubApproval{{Action: "create-repository", Name: "x", Visibility: "public", ExpiresAt: metav1.Now()}}}
	if err := off.s.Client.Update(context.Background(), off.obj); err != nil {
		t.Fatal(err)
	}
	if code := off.github(t, "DELETE", "", `{}`); code != 200 {
		t.Fatal("revocation refused with the feature off", code)
	}
}
