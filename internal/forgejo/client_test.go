package forgejo

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestExistingMainProtectionExplainsMissingApprovalWithoutChangingRule(t *testing.T) {
	mutations := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			mutations++
			t.Error("existing protections must not be rewritten")
		}
		if strings.HasSuffix(r.URL.Path, "/branch_protections") {
			_ = json.NewEncoder(w).Encode([]protection{{RuleName: "main", ApplyToAdmins: true, EnableMergeWhitelist: true, MergeWhitelistUsernames: []string{"steve"}, RequiredApprovals: 0}})
		} else {
			_ = json.NewEncoder(w).Encode(map[string]any{"default_branch": "main"})
		}
	}))
	defer server.Close()
	c := &Client{Origin: server.URL, Owner: "steve", AdminToken: "fixture", HTTP: server.Client()}
	err := c.protect(context.Background(), "steve/repo")
	if err == nil || !strings.Contains(err.Error(), "require at least 1 approval (currently 0)") || !strings.Contains(err.Error(), "Settings > Branches") || mutations != 0 {
		t.Fatal(err, mutations)
	}
}

func TestNewRejectsGitHubOrigin(t *testing.T) {
	for _, origin := range []string{"https://github.com", "https://GitHub.com", "https://api.github.com"} {
		_, err := New(origin, "owner", "token")
		if err == nil || !strings.Contains(err.Error(), "GIT_HOST=github") {
			t.Fatal(origin, err)
		}
	}
	if _, err := New("https://github.com.evil.example", "owner", "token"); err != nil {
		t.Fatal(err)
	}
	if _, err := New("https://git.example.com", "owner", "token"); err != nil {
		t.Fatal(err)
	}
}

func TestRevokeTreatsMissingUserAsRevoked(t *testing.T) {
	for _, exists := range []bool{false, true} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == "DELETE" && strings.Contains(r.URL.Path, "/collaborators/"):
				w.WriteHeader(422) // Forgejo's answer when the user does not exist
			case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/users/apc-gone") && !exists:
				w.WriteHeader(404)
			case r.Method == "GET":
				_ = json.NewEncoder(w).Encode(map[string]any{"email": "apc-gone@agentwork.invalid", "prohibit_login": true})
			default:
				t.Error("unexpected", r.Method, r.URL.Path)
			}
		}))
		c := &Client{Origin: server.URL, Owner: "steve", AdminToken: "fixture", HTTP: server.Client()}
		err := c.Revoke(context.Background(), "steve/repo", Credentials{Username: "apc-gone"})
		server.Close()
		if exists != (err != nil) || exists && !statusIs(err, 422) {
			t.Fatal("user exists:", exists, err)
		}
	}
}
