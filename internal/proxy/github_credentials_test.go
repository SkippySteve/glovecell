package proxy

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/credentials"
)

// loaderProvider serves every URI the proxy names from one loader, and
// records the URIs asked for.
func loaderProvider(get func() (string, error), asked *[]string) credentials.Provider {
	if get == nil {
		return nil
	}
	return credentials.ProviderFunc(func(_ context.Context, req credentials.Request) ([]byte, error) {
		if asked != nil {
			*asked = append(*asked, req.URI)
		}
		v, err := get()
		return []byte(v), err
	})
}

func TestGitHubGatewayScopeRenewalAndUnavailableGrant(t *testing.T) {
	cfg := Config{PrivateGitHubRepositories: []string{"owner/private"}}
	current := "initial-access-grant"
	var asked []string
	s := &Server{Config: cfg, Credentials: loaderProvider(func() (string, error) { return current, nil }, &asked)}
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
	if len(asked) != 2 || asked[0] != GitHubTokenURI || asked[1] != GitHubTokenURI {
		t.Fatal("GitHub grant not fetched by its credential URI", asked)
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
		s.Credentials = loaderProvider(loader, nil)
		r := request("https://github.com/owner/private.git/info/refs?service=git-upload-pack")
		err := s.githubCredentials(r)
		if err == nil || strings.Contains(err.Error(), "unsafe") || r.Header.Get("Authorization") != "" {
			t.Fatal("missing/revoked grant allowed or leaked")
		}
		if credentials.Status(err) != http.StatusForbidden && credentials.Status(err) != http.StatusServiceUnavailable {
			t.Fatal("unexpected failure status", credentials.Status(err))
		}
	}
}

func TestCredentialFailuresMapToSubstrateStatuses(t *testing.T) {
	cfg := Config{PrivateGitHubRepositories: []string{"owner/private"}}
	for err, want := range map[error]int{
		credentials.ErrNotFound:         http.StatusForbidden,
		credentials.ErrPermissionDenied: http.StatusForbidden,
		credentials.ErrUnavailable:      http.StatusServiceUnavailable,
	} {
		s := &Server{Config: cfg, Credentials: loaderProvider(func() (string, error) { return "", err }, nil)}
		r, _ := http.NewRequest("GET", "https://github.com/owner/private.git/info/refs?service=git-upload-pack", nil)
		if got := credentials.Status(s.githubCredentials(r)); got != want {
			t.Fatal(err, got, want)
		}
	}
	// No provider at all is a retryable outage, never a request without its credential.
	r, _ := http.NewRequest("GET", "https://github.com/owner/private.git/info/refs?service=git-upload-pack", nil)
	if got := credentials.Status((&Server{Config: cfg}).githubCredentials(r)); got != http.StatusServiceUnavailable {
		t.Fatal("missing provider", got)
	}
}

func TestGatewayCredentialsServeOnlyTheInjectedKeys(t *testing.T) {
	for _, uri := range []string{GitHubTokenURI, MuseSubscriptionKeyURI} {
		if _, err := credentials.ParseURI(uri); err != nil {
			t.Fatal(uri, err)
		}
	}
	f := gatewayCredentials().(credentials.Files)
	for _, uri := range []string{"ate-secret://" + CellCredentialClass + "/tls/ca.key", "ate-secret://" + CellCredentialClass + "/tls/provider-grants"} {
		if _, err := f.FetchSecret(context.Background(), credentials.Request{URI: uri}); !errors.Is(err, credentials.ErrPermissionDenied) {
			t.Fatal("proxy credential provider serves an unlisted key", uri, err)
		}
	}
}
