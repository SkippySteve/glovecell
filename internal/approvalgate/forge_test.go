package approvalgate

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestForgejoClient(t *testing.T) {
	var merged map[string]any
	var status map[string]string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "token gate-token" {
			w.WriteHeader(401)
			return
		}
		p := r.URL.Path
		switch {
		case r.Method == "GET" && p == "/api/v1/repos/steve/apc/pulls/92":
			w.Write([]byte(`{"number":92,"title":"Store","body":"b","state":"open","merged":false,"mergeable":true,"html_url":"https://x/92","additions":5,"deletions":1,"changed_files":2,"user":{"login":"orca-agent"},"head":{"sha":"abc","ref":"task/x"},"base":{"ref":"main"}}`))
		case r.Method == "GET" && p == "/api/v1/repos/steve/apc/pulls/92/files":
			w.Write([]byte(`[{"filename":"a.go","status":"modified","additions":5,"deletions":1}]`))
		case r.Method == "GET" && p == "/api/v1/repos/steve/apc/commits/abc/status":
			w.Write([]byte(`{"state":"success","statuses":[{"context":"CI","status":"success"},{"context":"voice-approval","status":"pending"}]}`))
		case r.Method == "POST" && p == "/api/v1/repos/steve/apc/statuses/abc":
			json.NewDecoder(r.Body).Decode(&status)
			w.WriteHeader(201)
		case r.Method == "POST" && p == "/api/v1/repos/steve/apc/pulls/92/merge":
			json.NewDecoder(r.Body).Decode(&merged)
			if merged["head_commit_id"] != "abc" {
				w.WriteHeader(409)
				return
			}
			w.WriteHeader(200)
		case r.Method == "POST" && p == "/api/v1/repos/steve/apc/pulls/93/merge":
			w.WriteHeader(405)
			w.Write([]byte(`{"message":"Please try again later"}`))
		case r.Method == "POST" && p == "/api/v1/repos/steve/apc/issues/92/comments":
			w.WriteHeader(201)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	f, err := NewForgejo(srv.URL, "gate-token")
	if err != nil {
		t.Fatal(err)
	}
	f.HTTP.Transport = srv.Client().Transport
	pr, err := f.PullRequest(ctx, "steve/apc", 92)
	if err != nil || pr.HeadSHA != "abc" || !pr.Mergeable || pr.Author != "orca-agent" || pr.BaseRef != "main" {
		t.Fatalf("pr %+v %v", pr, err)
	}
	files, err := f.Files(ctx, "steve/apc", 92)
	if err != nil || len(files) != 1 || files[0].Name != "a.go" {
		t.Fatalf("files %+v %v", files, err)
	}
	checks, err := f.Checks(ctx, "steve/apc", "abc")
	if err != nil || len(checks) != 2 {
		t.Fatalf("checks %+v %v", checks, err)
	}
	if ci := Summarize(checks, "voice-approval"); !ci.Green() {
		t.Fatalf("own pending status counted: %+v", ci)
	}
	if err := f.SetStatus(ctx, "steve/apc", "abc", Check{Context: "voice-approval", State: "success", Description: strings.Repeat("d", 300)}); err != nil || status["context"] != "voice-approval" || len(status["description"]) > 145 {
		t.Fatalf("status %v %v", status, err)
	}
	if err := f.Merge(ctx, "steve/apc", 92, "abc", "t", "m"); err != nil || merged["Do"] != "merge" {
		t.Fatalf("merge %v %v", merged, err)
	}
	if err := f.Merge(ctx, "steve/apc", 92, "old", "t", "m"); !errors.Is(err, ErrHeadChanged) {
		t.Fatalf("moved head: %v", err)
	}
	if err := f.Merge(ctx, "steve/apc", 93, "abc", "t", "m"); !errors.Is(err, ErrNotMergeable) {
		t.Fatalf("not mergeable: %v", err)
	}
	if err := f.Comment(ctx, "steve/apc", 92, "hi"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.PullRequest(ctx, "../etc", 1); err == nil {
		t.Fatal("bad repository accepted")
	}
	bad, _ := NewForgejo(srv.URL, "wrong")
	bad.HTTP.Transport = srv.Client().Transport
	if _, err := bad.PullRequest(ctx, "steve/apc", 92); err == nil || strings.Contains(err.Error(), "wrong") {
		t.Fatalf("auth error %v", err)
	}
	for _, o := range []string{"http://git.example.com", "https://git.example.com/sub", ""} {
		if _, err := NewForgejo(o, "t"); err == nil {
			t.Errorf("origin %q accepted", o)
		}
	}
}
