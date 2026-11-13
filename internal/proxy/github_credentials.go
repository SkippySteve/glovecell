package proxy

import (
	"net/http"
	"net/url"
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
	// Fetched per request so a replaced or revoked grant applies at once.
	token, err := s.credential(r.Context(), GitHubTokenURI)
	if err != nil {
		return err
	}
	r.Header.Del("Cookie")
	r.SetBasicAuth("x-access-token", string(token))
	return nil
}
