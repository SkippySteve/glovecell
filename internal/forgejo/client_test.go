package forgejo

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// protectionServer serves one repository whose main rule is rule. PATCH bodies
// are recorded and, when apply is set, merged into the rule like Forgejo does.
func protectionServer(t *testing.T, rules []protection, apply bool, patchStatus int) (*Client, *[]map[string]any, func()) {
	t.Helper()
	var patches []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/branch_protections"):
			_ = json.NewEncoder(w).Encode(rules)
		case r.Method == "GET":
			_ = json.NewEncoder(w).Encode(map[string]any{"default_branch": "main"})
		case r.Method == "PATCH" && strings.HasSuffix(r.URL.Path, "/repos/steve/repo/branch_protections/main"):
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			patches = append(patches, body)
			if patchStatus != 0 {
				w.WriteHeader(patchStatus)
				return
			}
			if apply {
				current, _ := json.Marshal(rules[0])
				merged := map[string]any{}
				_ = json.Unmarshal(current, &merged)
				for k, v := range body {
					merged[k] = v
				}
				raw, _ := json.Marshal(merged)
				_ = json.Unmarshal(raw, &rules[0])
			}
			_ = json.NewEncoder(w).Encode(rules[0])
		default:
			t.Error("unexpected", r.Method, r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	return &Client{Origin: server.URL, Owner: "steve", AdminToken: "fixture", HTTP: server.Client()}, &patches, server.Close
}

func TestExistingMainProtectionIsTightenedOnlyWhereItFallsShort(t *testing.T) {
	rules := []protection{{RuleName: "main", ApplyToAdmins: false, EnableMergeWhitelist: true, MergeWhitelistUsernames: []string{"steve"}, RequiredApprovals: 2}}
	c, patches, done := protectionServer(t, rules, true, 0)
	defer done()
	if err := c.protect(context.Background(), "steve/repo"); err != nil {
		t.Fatal(err)
	}
	// Only the failing setting is sent; the stricter approval count is kept.
	if len(*patches) != 1 || len((*patches)[0]) != 1 || (*patches)[0]["apply_to_admins"] != true {
		t.Fatal(*patches)
	}
	if !rules[0].ApplyToAdmins || rules[0].RequiredApprovals != 2 {
		t.Fatal(rules[0])
	}
}

func TestEveryShortfallIsTightenedTowardThePolicy(t *testing.T) {
	rules := []protection{{RuleName: "main", EnablePush: true, EnableForcePush: true, MergeWhitelistUsernames: []string{"steve", "other"}, MergeWhitelistTeams: []string{"owners"}}}
	c, patches, done := protectionServer(t, rules, true, 0)
	defer done()
	if err := c.protect(context.Background(), "steve/repo"); err != nil {
		t.Fatal(err)
	}
	p := (*patches)[0]
	if len(*patches) != 1 || p["enable_push"] != false || p["enable_force_push"] != false || p["apply_to_admins"] != true || p["required_approvals"] != float64(1) || p["enable_merge_whitelist"] != true {
		t.Fatal(p)
	}
	if users, _ := p["merge_whitelist_usernames"].([]any); len(users) != 1 || users[0] != "steve" {
		t.Fatal(p)
	}
	if teams, ok := p["merge_whitelist_teams"].([]any); !ok || len(teams) != 0 {
		t.Fatal(p)
	}
}

func TestIgnoredTighteningFailsClosedWithoutRetrying(t *testing.T) {
	rules := []protection{{RuleName: "main", EnableMergeWhitelist: true, MergeWhitelistUsernames: []string{"steve"}, RequiredApprovals: 0, ApplyToAdmins: true}}
	c, patches, done := protectionServer(t, rules, false, 0)
	defer done()
	err := c.protect(context.Background(), "steve/repo")
	if err == nil || !strings.Contains(err.Error(), "require at least 1 approval (currently 0)") || !strings.Contains(err.Error(), "Settings > Branches") || len(*patches) != 1 {
		t.Fatal(err, *patches)
	}
}

func TestRejectedTighteningReportsTheShortfall(t *testing.T) {
	rules := []protection{{RuleName: "main", EnableMergeWhitelist: true, MergeWhitelistUsernames: []string{"steve"}, RequiredApprovals: 1}}
	c, _, done := protectionServer(t, rules, false, 403)
	defer done()
	err := c.protect(context.Background(), "steve/repo")
	if err == nil || !strings.Contains(err.Error(), "apply protection to administrators") || !statusIs(err, 403) {
		t.Fatal(err)
	}
}

func TestOverlappingWildcardRuleIsNeverRewritten(t *testing.T) {
	rules := []protection{{RuleName: "main", EnablePush: true}, {RuleName: "ma*"}}
	c, patches, done := protectionServer(t, rules, true, 0)
	defer done()
	err := c.protect(context.Background(), "steve/repo")
	if err == nil || !strings.Contains(err.Error(), "operator review") || len(*patches) != 0 {
		t.Fatal(err, *patches)
	}
}

func TestSufficientMainProtectionIsLeftAlone(t *testing.T) {
	rules := []protection{{RuleName: "main", ApplyToAdmins: true, EnableMergeWhitelist: true, MergeWhitelistUsernames: []string{"steve"}, RequiredApprovals: 1}}
	c, patches, done := protectionServer(t, rules, true, 0)
	defer done()
	if err := c.protect(context.Background(), "steve/repo"); err != nil || len(*patches) != 0 {
		t.Fatal(err, *patches)
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
