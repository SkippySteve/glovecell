package proxy

import (
	"errors"
	"net/http"
	"net/url"
	"os"
	"strings"
)

func privateGitHubRepository(c Config, r *http.Request) string {
	if r.URL.Hostname() != "github.com" && r.Host != "github.com" {
		return ""
	}
	for _, repo := range c.PrivateGitHubRepositories {
		root := "/" + repo + ".git/"
		if r.URL.Path == root+"info/refs" || r.URL.Path == root+"git-upload-pack" {
			return repo
		}
	}
	return ""
}
func githubGitReadAllowed(c Config, r *http.Request) bool {
	if privateGitHubRepository(c, r) == "" {
		return true
	}
	if r.Method == "POST" {
		return strings.HasSuffix(r.URL.Path, "/git-upload-pack") && r.URL.RawQuery == ""
	}
	values, err := url.ParseQuery(r.URL.RawQuery)
	return err == nil && r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/info/refs") && len(values) == 1 && len(values["service"]) == 1 && values.Get("service") == "git-upload-pack"
}
func (s *Server) githubCredentials(r *http.Request) error {
	if privateGitHubRepository(s.Config, r) == "" {
		return nil
	}
	// Read the projected gateway Secret per request so credential replacement is consumed.
	if s.GitHubToken == nil {
		return errors.New("GitHub repository authorization unavailable")
	}
	token, err := s.GitHubToken()
	if err != nil || token == "" || strings.ContainsAny(token, "\r\n\x00") {
		return errors.New("GitHub repository authorization unavailable")
	}
	r.Header.Del("Cookie")
	r.SetBasicAuth("x-access-token", token)
	return nil
}
func gatewayGitHubToken() (string, error) {
	raw, err := os.ReadFile("/tls/github-token")
	return strings.TrimSpace(string(raw)), err
}
