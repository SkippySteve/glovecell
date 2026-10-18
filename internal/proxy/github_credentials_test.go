package proxy

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestGitHubGatewayScopeRenewalAndUnavailableGrant(t *testing.T) {
	cfg := Config{PrivateGitHubRepositories: []string{"owner/private"}}
	current := "initial-access-grant"
	s := &Server{Config: cfg, GitHubToken: func() (string, error) { return current, nil }}
	request := func(path string) *http.Request { r, _ := http.NewRequest("GET", path, nil); return r }
	for _, token := range []string{"initial-access-grant", "replacement-access-grant"} {
		current = token
		r := request("https://github.com/owner/private.git/info/refs?service=git-upload-pack")
		if !githubGitReadAllowed(cfg, r) || s.githubCredentials(r) != nil {
			t.Fatal("read grant rejected")
		}
		user, value, ok := r.BasicAuth()
		if !ok || user != "x-access-token" || value != token {
			t.Fatal("current grant not injected")
		}
	}
	for _, destination := range []string{"https://attacker.example/owner/private.git/info/refs", "https://github.com/owner/unapproved.git/info/refs", "https://api.github.com/repos/owner/private", "https://github.com/login"} {
		r := request(destination)
		if err := s.githubCredentials(r); err != nil || r.Header.Get("Authorization") != "" {
			t.Fatal("credential crossed its enrolled Git origin")
		}
	}
	for _, r := range []*http.Request{
		request("https://github.com/owner/private.git/info/refs?service=git-receive-pack"),
		request("https://github.com/owner/private.git/info/refs?service=git-upload-pack&extra=1"),
		request("https://github.com/owner/private.git/info/refs?service=git-upload-pack&service=git-receive-pack"),
	} {
		if githubGitReadAllowed(cfg, r) {
			t.Fatal("write or ambiguous Git discovery accepted")
		}
	}
	for _, loader := range []func() (string, error){nil, func() (string, error) { return "", nil }, func() (string, error) { return "", errors.New("broker unavailable") }, func() (string, error) { return "unsafe\nvalue", nil }} {
		s.GitHubToken = loader
		if err := s.githubCredentials(request("https://github.com/owner/private.git/info/refs?service=git-upload-pack")); err == nil || strings.Contains(err.Error(), "unsafe") {
			t.Fatal("missing/revoked grant allowed or leaked")
		}
	}
}
