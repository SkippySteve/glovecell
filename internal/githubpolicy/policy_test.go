package githubpolicy

import (
	"bufio"
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"
)

func pkt(s string) string { return fmt.Sprintf("%04x%s", len(s)+4, s) }

const (
	zero = "0000000000000000000000000000000000000000"
	a    = "1111111111111111111111111111111111111111"
	b    = "2222222222222222222222222222222222222222"
)

func TestParseRefusesUnknownFieldsAndBadShapes(t *testing.T) {
	good := `{"project":"p","projectUID":"u","repositories":["SkippySteve/app","SkippySteve/*"],"protectedBranches":["release/*"],"pushBranches":["agent/*"]}`
	if p, err := Parse([]byte(good)); err != nil || p == nil {
		t.Fatal(err)
	}
	if p, err := Parse(nil); err != nil || p != nil {
		t.Fatal("empty policy must mean off")
	}
	for _, raw := range []string{
		`{"repositories":["a/b"],"token":"x"}`,
		`{"repositories":[]}`,
		`{"repositories":["a"]}`,
		`{"repositories":["a/*b"]}`,
		`{"repositories":["a/b","A/B"]}`,
		`{"repositories":["a/b"],"protectedBranches":["*"]}`,
		`{"repositories":["a/b"],"protectedBranches":["a/../b"]}`,
		`{"repositories":["a/b"],"approvals":[{"action":"delete-repository","name":"x","expiresAt":"2030-01-01T00:00:00Z"}]}`,
		`{"repositories":["a/b"],"approvals":[{"action":"create-repository","name":"x","visibility":"internal","expiresAt":"2030-01-01T00:00:00Z"}]}`,
		`{"repositories":["a/b"],"approvals":[{"action":"create-repository","name":"x","visibility":"private"}]}`,
		`{"repositories":["a/b"]} {}`,
	} {
		if _, err := Parse([]byte(raw)); err == nil {
			t.Error("accepted", raw)
		}
	}
}

func TestRepositoryMatching(t *testing.T) {
	p := &Policy{Repositories: []string{"SkippySteve/app", "bot/*"}}
	for repo, want := range map[string]bool{"skippysteve/APP": true, "SkippySteve/other": false, "bot/anything": true, "evil/app": false, "bot/a/b": false, "bot/..": false} {
		if p.RepoAllowed(repo) != want {
			t.Error(repo)
		}
	}
}

func TestRESTPolicyTable(t *testing.T) {
	p := &Policy{Repositories: []string{"SkippySteve/app"}}
	obj := func(s string) []byte { return []byte(s) }
	for _, c := range []struct {
		method, path string
		body         []byte
		want         Verdict
	}{
		{"GET", "/repos/SkippySteve/app/pulls", nil, Allow},
		{"GET", "/user", nil, Allow},
		{"GET", "/repos/other/repo", nil, NotBrokered},
		{"POST", "/repos/other/repo/pulls", obj(`{}`), NotBrokered},
		{"POST", "/repos/SkippySteve/app/pulls", obj(`{"title":"t","head":"agent/x","base":"main"}`), Allow},
		{"POST", "/repos/SkippySteve/app/issues/3/comments", obj(`{"body":"hi"}`), Allow},
		{"POST", "/repos/SkippySteve/app/issues", obj(`{"title":"t"}`), Allow},
		{"PATCH", "/repos/SkippySteve/app/pulls/4", obj(`{"title":"t"}`), Allow},
		{"PATCH", "/repos/SkippySteve/app/pulls/4", obj(`{"head":"x"}`), Deny},
		{"POST", "/repos/SkippySteve/app/pulls/4/reviews", obj(`{"event":"APPROVE"}`), Deny},
		{"POST", "/repos/SkippySteve/app/pulls/4/reviews", obj(`{"event":"COMMENT","body":"x"}`), Allow},
		{"PUT", "/repos/SkippySteve/app/pulls/4/merge", obj(`{}`), Deny},
		{"POST", "/repos/SkippySteve/app/merges", obj(`{"base":"main","head":"x"}`), Deny},
		{"DELETE", "/repos/SkippySteve/app", nil, Deny},
		{"PATCH", "/repos/SkippySteve/app", obj(`{"private":false}`), Deny},
		{"PATCH", "/repos/SkippySteve/app", obj(`{"archived":true}`), Deny},
		{"POST", "/repos/SkippySteve/app/transfer", obj(`{"new_owner":"x"}`), Deny},
		{"PUT", "/repos/SkippySteve/app/collaborators/x", obj(`{}`), Deny},
		{"POST", "/repos/SkippySteve/app/hooks", obj(`{}`), Deny},
		{"POST", "/repos/SkippySteve/app/keys", obj(`{}`), Deny},
		{"PUT", "/repos/SkippySteve/app/actions/secrets/X", obj(`{}`), Deny},
		{"PUT", "/repos/SkippySteve/app/branches/main/protection", obj(`{}`), Deny},
		{"PUT", "/repos/SkippySteve/app/contents/README.md", obj(`{}`), Deny},
		{"PATCH", "/repos/SkippySteve/app/git/refs/heads/main", obj(`{"sha":"x","force":true}`), Deny},
		{"POST", "/repos/SkippySteve/app/releases", obj(`{"tag_name":"v1"}`), Deny},
		{"DELETE", "/repos/SkippySteve/app/issues/comments/1", nil, Deny},
		{"POST", "/repos/SkippySteve/app/pulls?x=1", obj(`{}`), Deny},
		{"POST", "/user/repos", obj(`{"name":"new","private":true}`), NeedsApproval},
		{"POST", "/user/repos", obj(`{"name":"new","team_id":1}`), Deny},
		{"POST", "/repos/upstream/tool/forks", nil, NeedsApproval},
		{"POST", "/repos/upstream/tool/forks", obj(`{"organization":"elsewhere"}`), Deny},
		{"POST", "/graphql", obj(`{}`), Inspect},
		{"DELETE", "/user/keys/1", nil, NotBrokered},
		{"GET", "/repos/SkippySteve/app/../other", nil, Deny},
	} {
		path, query, _ := strings.Cut(c.path, "?")
		if got := Classify(p, "api.github.com", c.method, path, query, c.body); got.Verdict != c.want {
			t.Errorf("%s %s = %v (%s), want %v", c.method, c.path, got.Verdict, got.Reason, c.want)
		}
	}
	p.AllowMerge, p.AllowReleases = true, true
	if got := Classify(p, "api.github.com", "PUT", "/repos/SkippySteve/app/pulls/4/merge", "", nil); got.Verdict != Allow {
		t.Error("opted-in merge denied", got.Reason)
	}
	if got := Classify(p, "api.github.com", "DELETE", "/repos/SkippySteve/app/releases/1", "", nil); got.Verdict != Deny {
		t.Error("release deletion allowed")
	}
}

func TestRepositoryCreationIsCanonical(t *testing.T) {
	for body, want := range map[string]string{
		`{"name":"x"}`:                                      "public",
		`{"name":"x","private":false}`:                      "public",
		`{"name":"x","private":true}`:                       "private",
		`{"name":"x","visibility":"private"}`:               "private",
		`{"name":"x","private":true,"visibility":"public"}`: "",
		`{"name":"x","private":"yes"}`:                      "",
		`{"name":"x","visibility":"internal"}`:              "",
		`{"name":"../x"}`:                                   "",
	} {
		d := createRepository([]byte(body))
		if want == "" {
			if d.Verdict != Deny {
				t.Error("accepted", body)
			}
			continue
		}
		if d.Verdict != NeedsApproval || d.Approval.Visibility != want || d.Approval.Name != "x" {
			t.Error(body, d)
		}
	}
}

func TestApprovalMatchingNeedsExactArgumentsAndLife(t *testing.T) {
	now := time.Now()
	p := &Policy{Repositories: []string{"a/b"}, Approvals: []Approval{{Action: ActionCreateRepository, Name: "New", Visibility: "private", ExpiresAt: now.Add(time.Hour)}, {Action: ActionForkRepository, Name: "tool", Source: "up/tool", ExpiresAt: now.Add(-time.Minute)}}}
	if _, ok := p.Match(ApprovalRequest{Action: ActionCreateRepository, Name: "new", Visibility: "private"}, now); !ok {
		t.Fatal("approved creation refused")
	}
	if _, ok := p.Match(ApprovalRequest{Action: ActionCreateRepository, Name: "new", Visibility: "public"}, now); ok {
		t.Fatal("public creation matched a private approval")
	}
	if _, ok := p.Match(ApprovalRequest{Action: ActionForkRepository, Name: "tool", Source: "up/tool"}, now); ok {
		t.Fatal("expired approval matched")
	}
}

func TestGitRoutes(t *testing.T) {
	p := &Policy{Repositories: []string{"SkippySteve/app"}}
	for _, c := range []struct {
		method, path, query string
		want                Verdict
	}{
		{"GET", "/SkippySteve/app.git/info/refs", "service=git-upload-pack", Allow},
		{"GET", "/SkippySteve/app/info/refs", "service=git-receive-pack", Allow},
		{"POST", "/SkippySteve/app.git/git-upload-pack", "", Allow},
		{"POST", "/SkippySteve/app.git/git-receive-pack", "", Inspect},
		{"POST", "/SkippySteve/app.git/git-receive-pack", "x=1", NotBrokered},
		{"POST", "/other/app.git/git-receive-pack", "", NotBrokered},
		{"GET", "/SkippySteve/app", "", NotBrokered},
		{"POST", "/SkippySteve/app.git/info/lfs/objects/batch", "", Deny},
	} {
		if got := Classify(p, "github.com", c.method, c.path, c.query, nil); got.Verdict != c.want {
			t.Errorf("%s %s?%s = %v, want %v", c.method, c.path, c.query, got.Verdict, c.want)
		}
	}
}

func TestReadCommandsReplaysExactBytes(t *testing.T) {
	body := pkt(zero+" "+a+" refs/heads/agent/x\x00report-status side-band-64k agent=git/2.47\n") + pkt(a+" "+b+" refs/heads/feature") + "0000" + "PACK...binary..."
	r := bufio.NewReader(strings.NewReader(body))
	updates, consumed, err := ReadCommands(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(updates) != 2 || !updates[0].Create() || updates[1].Ref != "refs/heads/feature" || updates[1].Create() || updates[1].Delete() {
		t.Fatal(updates)
	}
	var rest bytes.Buffer
	rest.ReadFrom(r)
	if string(consumed)+rest.String() != body {
		t.Fatal("replayed body differs")
	}
	// git's probe for large pushes is a lone flush-pkt.
	if updates, _, err := ReadCommands(bufio.NewReader(strings.NewReader("0000"))); err != nil || len(updates) != 0 {
		t.Fatal("probe refused", err)
	}
	for _, bad := range []string{
		"",
		pkt(zero + " " + a + " refs/heads/x"),
		pkt(zero+" "+a+" refs/heads/x") + "00",
		"zzzz",
		"0003",
		pkt("push-cert\x00caps\n") + "0000",
		pkt(zero+" "+zero+" refs/heads/x") + "0000",
		pkt(zero+" "+a+" refs/heads/../x") + "0000",
		pkt(zero+" "+a+" HEAD") + "0000",
		pkt(zero+" "+a+" refs/heads/x") + pkt(a+" "+b+" refs/heads/y\x00caps") + "0000",
		pkt(zero[:39]+" "+a+" refs/heads/x") + "0000",
	} {
		if _, _, err := ReadCommands(bufio.NewReader(strings.NewReader(bad))); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestCheckPushBranchRules(t *testing.T) {
	p := &Policy{Repositories: []string{"a/b"}, ProtectedBranches: []string{"release/*", "stable"}}
	up := func(old, new, ref string) []RefUpdate { return []RefUpdate{{old, new, ref}} }
	allowed := [][]RefUpdate{
		up(zero, a, "refs/heads/agent/x"),
		up(a, b, "refs/heads/agent/x"),
		up(a, b, "refs/heads/feature"),
		nil,
	}
	for _, u := range allowed {
		if err := p.CheckPush(u, "main"); err != nil {
			t.Error(u, err)
		}
	}
	denied := [][]RefUpdate{
		up(a, b, "refs/heads/main"),
		up(zero, a, "refs/heads/main"),
		up(a, zero, "refs/heads/main"),
		up(a, b, "refs/heads/release/1.0"),
		up(a, b, "refs/heads/stable"),
		up(a, zero, "refs/heads/agent/x"),
		up(zero, a, "refs/tags/v1"),
		up(zero, a, "refs/notes/commits"),
		append(up(zero, a, "refs/heads/agent/ok"), RefUpdate{a, b, "refs/heads/main"}),
	}
	for _, u := range denied {
		if err := p.CheckPush(u, "main"); err == nil {
			t.Error("allowed", u)
		}
	}
	if err := p.CheckPush(up(a, b, "refs/heads/feature"), ""); err == nil {
		t.Error("unknown default branch must protect every branch")
	}
	p.AllowReleases = true
	if err := p.CheckPush(up(zero, a, "refs/tags/v1"), "main"); err != nil {
		t.Error(err)
	}
	if err := p.CheckPush(up(a, b, "refs/tags/v1"), "main"); err == nil {
		t.Error("tag move allowed")
	}
	p.PushBranches = []string{"agent/*"}
	if err := p.CheckPush(up(a, b, "refs/heads/feature"), "main"); err == nil {
		t.Error("push outside pushBranches allowed")
	}
	if err := p.CheckPush(up(a, b, "refs/heads/agent/x"), "main"); err != nil {
		t.Error(err)
	}
}

func gql(query, vars string) []byte {
	if vars == "" {
		vars = "null"
	}
	return []byte(fmt.Sprintf(`{"query":%q,"variables":%s}`, query, vars))
}

func TestGraphQLOperations(t *testing.T) {
	p := &Policy{Repositories: []string{"a/b"}}
	prInput := `{"input":{"repositoryId":"R_1","baseRefName":"main","headRefName":"agent/x","title":"t","body":"b","draft":false}}`
	for _, c := range []struct {
		body []byte
		want Verdict
		node string
	}{
		{gql(`query { viewer { login } }`, ""), Allow, ""},
		{gql(`{ repository(owner:"a", name:"b") { pullRequests(first: 5, states: [OPEN]) { nodes { title } } } }`, ""), Allow, ""},
		{gql(`query Q($n: Int = 3) { viewer { repositories(first: $n) { nodes { name } } } } fragment F on User { login }`, ""), Allow, ""},
		{gql("mutation PullRequestCreate($input: CreatePullRequestInput!) {\n createPullRequest(input: $input) { pullRequest { id url } }\n}", prInput), Inspect, "R_1"},
		{gql(`mutation C($input: AddCommentInput!) { addComment(input: $input) { commentEdge { node { url } } } }`, `{"input":{"subjectId":"PR_9","body":"x"}}`), Inspect, "PR_9"},
		{gql(`mutation R($input: CreateRepositoryInput!) { createRepository(input: $input) { repository { url } } }`, `{"input":{"name":"n","visibility":"PRIVATE","hasIssuesEnabled":true}}`), NeedsApproval, ""},
		{gql(`mutation R($input: CreateRepositoryInput!) { createRepository(input: $input) { repository { url } } }`, `{"input":{"name":"n","visibility":"PRIVATE","teamId":"T"}}`), Deny, ""},
		{gql(`mutation($input: X!) { deleteRepository(input: $input) { clientMutationId } }`, `{"input":{"repositoryId":"R"}}`), Deny, ""},
		{gql(`mutation($input: X!) { updateRepository(input: $input) { clientMutationId } }`, `{"input":{"repositoryId":"R","visibility":"PUBLIC"}}`), Deny, ""},
		{gql(`mutation($input: X!) { mergePullRequest(input: $input) { clientMutationId } }`, `{"input":{"pullRequestId":"P"}}`), Deny, ""},
		{gql(`mutation($input: X!, $d: Y!) { createPullRequest(input: $input) { clientMutationId } deleteRepository(input: $d) { clientMutationId } }`, `{"input":{"repositoryId":"R"},"d":{"repositoryId":"R"}}`), Deny, ""},
		{gql(`mutation($input: X!) { ...F } fragment F on Mutation { deleteRepository(input: $input) { clientMutationId } }`, `{"input":{"repositoryId":"R"}}`), Deny, ""},
		{gql(`mutation { createPullRequest(input: {repositoryId: "R"}) { clientMutationId } }`, ""), Deny, ""},
		{gql(`mutation($input: X!) { createPullRequest(input: $input) { clientMutationId } }`, `{"input":{"repositoryId":"R","unknown":1}}`), Deny, ""},
		{gql(`query { viewer { login } } mutation($input: X!) { deleteRepository(input: $input) { clientMutationId } }`, `{"input":{"repositoryId":"R"}}`), Deny, ""},
		{gql(`subscription { x }`, ""), Deny, ""},
		{gql(`mutation @x { createPullRequest(input: $input) { id } }`, prInput), Deny, ""},
		{gql(`mutation($input: X!) { createPullRequest(input: $input) @skip(if: false) { id } }`, prInput), Deny, ""},
		{gql(`query { a(s: "mutation { deleteRepository }") }`, ""), Allow, ""},
		{gql(`query { a(s: """ multi "" line mutation """) }`, ""), Allow, ""},
		{gql(`query { a(s: "unterminated) }`, ""), Deny, ""},
		{gql(`type Query { a: Int }`, ""), Deny, ""},
		{[]byte(`[{"query":"query { a }"}]`), Deny, ""},
		{[]byte(`{"query":"query { a }","extensions":{}}`), Deny, ""},
		{[]byte(`{"query":"query A { a }","operationName":"B"}`), Deny, ""},
	} {
		got := ClassifyGraphQL(p, c.body)
		if got.Verdict != c.want || got.Node != c.node {
			t.Errorf("%s = %v %q (%s), want %v %q", c.body, got.Verdict, got.Node, got.Reason, c.want, c.node)
		}
	}
}

func TestBranchPatternsValidate(t *testing.T) {
	for pattern, ok := range map[string]bool{"main": true, "release/*": true, "agent/x/*": true, "*": false, "a*": false, "/x": false, "x/": false, "a//b": false, "a/../b": false, "": false} {
		p := &Policy{Repositories: []string{"a/b"}, ProtectedBranches: []string{pattern}}
		if (p.Validate() == nil) != ok {
			t.Error(pattern)
		}
	}
}
