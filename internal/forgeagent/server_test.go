package forgeagent

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/forgejo"
)

const (
	caller   = "system:serviceaccount:control:controller"
	identity = "apc-0123456789abcdef0123456789abcdef"
	repo     = "steve/project"
)

type fakeHost struct {
	grants, revokes []forgejo.Credentials
	err             error
}

func (f *fakeHost) Discover(context.Context) ([]forgejo.Repository, error) {
	return []forgejo.Repository{{FullName: repo, Private: true}}, f.err
}
func (f *fakeHost) Grant(_ context.Context, _ string, c forgejo.Credentials) (string, error) {
	f.grants = append(f.grants, c)
	return "repo-token", f.err
}
func (f *fakeHost) Revoke(_ context.Context, _ string, c forgejo.Credentials) error {
	f.revokes = append(f.revokes, c)
	return f.err
}

type fakeReviewer map[string]string

func (f fakeReviewer) Review(_ context.Context, token string) (string, error) {
	if u, ok := f[token]; ok {
		return u, nil
	}
	return "", errUnauthenticated
}

func setup(t *testing.T, token string) (*fakeHost, *Client) {
	t.Helper()
	host := &fakeHost{}
	srv := httptest.NewServer(&Server{Host: host, Owner: "steve", Caller: caller, Reviewer: fakeReviewer{"controller-token": caller, "other-token": "system:serviceaccount:control:other"}})
	t.Cleanup(srv.Close)
	file := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(file, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := NewClient(srv.URL, file, "https://git.example.com", "steve")
	if err != nil {
		t.Fatal(err)
	}
	return host, c
}

func TestGrantRevokeDiscoverRoundTrip(t *testing.T) {
	ctx := context.Background()
	host, c := setup(t, "controller-token")
	cred := forgejo.Credentials{Username: identity, Password: "pw", Token: "old"}
	token, err := c.Grant(ctx, repo, cred)
	if err != nil || token != "repo-token" {
		t.Fatal(token, err)
	}
	if len(host.grants) != 1 || host.grants[0] != cred {
		t.Fatalf("credentials not passed through: %+v", host.grants)
	}
	if err := c.Revoke(ctx, repo, forgejo.Credentials{Username: "apcp-0123456789abcdef0123456789abcdef", Password: "pw"}); err != nil {
		t.Fatal(err)
	}
	repos, err := c.Discover(ctx)
	if err != nil || len(repos) != 1 || repos[0].FullName != repo {
		t.Fatal(repos, err)
	}
}

func TestCallerMustBeTheController(t *testing.T) {
	ctx := context.Background()
	for _, token := range []string{"other-token", "unknown", ""} {
		host, c := setup(t, token)
		if token == "" {
			// An empty token file never reaches the agent.
			if _, err := c.Grant(ctx, repo, forgejo.Credentials{Username: identity, Password: "pw"}); err == nil {
				t.Fatal("empty token accepted")
			}
			continue
		}
		if _, err := c.Grant(ctx, repo, forgejo.Credentials{Username: identity, Password: "pw"}); err == nil || !strings.Contains(err.Error(), "unauthorized") {
			t.Fatalf("%s: %v", token, err)
		}
		if _, err := c.Discover(ctx); err == nil {
			t.Fatal(token, "discover without the controller's token")
		}
		if len(host.grants) != 0 {
			t.Fatal("forge called for an unauthorized caller")
		}
	}
}

// Only workspace identities, only the owner's repositories: the agent refuses
// even when the caller is the controller.
func TestAgentRefusesWhatTheControllerNeverAsksFor(t *testing.T) {
	host := &fakeHost{}
	s := &Server{Host: host, Owner: "steve", Caller: caller, Reviewer: fakeReviewer{"t": caller}}
	cases := map[string]string{
		"admin user":         `{"repository":"steve/project","username":"steve","password":"pw"}`,
		"other identity":     `{"repository":"steve/project","username":"apc-root","password":"pw"}`,
		"uppercase identity": `{"repository":"steve/project","username":"apc-0123456789ABCDEF0123456789ABCDEF","password":"pw"}`,
		"other owner":        `{"repository":"someone/else","username":"` + identity + `","password":"pw"}`,
		"path traversal":     `{"repository":"steve/../admin","username":"` + identity + `","password":"pw"}`,
		"no password":        `{"repository":"steve/project","username":"` + identity + `","password":""}`,
		"unknown field":      `{"repository":"steve/project","username":"` + identity + `","password":"pw","admin":true}`,
		"two documents":      `{"repository":"steve/project","username":"` + identity + `","password":"pw"}{}`,
		"spaces in password": `{"repository":"steve/project","username":"` + identity + `","password":"p w"}`,
	}
	for name, body := range cases {
		for _, path := range []string{"/v1/grant", "/v1/revoke"} {
			r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
			r.Header.Set("Authorization", "Bearer t")
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			if w.Code < 400 || w.Code >= 500 {
				t.Errorf("%s %s: HTTP %d", name, path, w.Code)
			}
		}
	}
	if len(host.grants)+len(host.revokes) != 0 {
		t.Fatal("forge called for a refused request")
	}
	for _, tc := range []struct{ method, path, ct string }{
		{http.MethodGet, "/v1/grant", "application/json"},
		{http.MethodPost, "/v1/admin", "application/json"},
		{http.MethodPost, "/v1/grant", "text/plain"},
	} {
		r := httptest.NewRequest(tc.method, tc.path, strings.NewReader("{}"))
		r.Header.Set("Authorization", "Bearer t")
		r.Header.Set("Content-Type", tc.ct)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code < 400 {
			t.Errorf("%s %s %s: HTTP %d", tc.method, tc.path, tc.ct, w.Code)
		}
	}
	big := httptest.NewRequest(http.MethodPost, "/v1/grant", strings.NewReader(strings.Repeat("x", maxBody+1)))
	big.Header.Set("Authorization", "Bearer t")
	big.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, big)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatal("oversized body", w.Code)
	}
}

// A Forgejo failure reaches the controller as an error with its message (for
// the workspace condition), never as success.
func TestForgeErrorsFailClosed(t *testing.T) {
	host, c := setup(t, "controller-token")
	host.err = errors.New("main branch protection needs operator review: x")
	if _, err := c.Grant(context.Background(), repo, forgejo.Credentials{Username: identity, Password: "pw"}); err == nil || !strings.Contains(err.Error(), "operator review") {
		t.Fatal(err)
	}
	if err := c.Revoke(context.Background(), repo, forgejo.Credentials{Username: identity, Password: "pw"}); err == nil {
		t.Fatal("failed revocation reported as done")
	}
}

func TestClientSettings(t *testing.T) {
	for _, bad := range [][2]string{{"ftp://agent", "f"}, {"http://user:pw@agent", "f"}, {"http://agent/x", "f"}, {"http://agent", ""}} {
		if _, err := NewClient(bad[0], bad[1], "https://git.example.com", "steve"); err == nil {
			t.Error("accepted", bad)
		}
	}
	if _, err := NewClient("http://agent:8443", "f", "http://git.example.com", "steve"); err == nil {
		t.Error("accepted a non-HTTPS Forgejo origin")
	}
	_, c := setup(t, "controller-token")
	if _, err := c.Grant(context.Background(), "other/repo", forgejo.Credentials{Username: identity, Password: "pw"}); err == nil {
		t.Error("client sent another owner's repository")
	}
}
