package targets

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func registry() *Registry {
	r := NewRegistry()
	r.Add(&Fake{TargetName: "orca"}, "orka")
	r.Add(&Fake{TargetName: "codex"}, "codecs", "code x")
	r.Add(&Fake{TargetName: "claude"})
	r.Add(Archon{}, "arkon")
	return r
}

func TestParseOverride(t *testing.T) {
	r := registry()
	cases := []struct {
		in, target, rest string
	}{
		{"Send this to Codex: fix the flaky login test", "codex", "Fix the flaky login test"},
		{"send it to codecs, fix the flaky login test", "codex", "Fix the flaky login test"},
		{"Use Orca. Bump the Go toolchain to 1.27.4", "orca", "Bump the Go toolchain to 1.27.4"},
		{"use codex for this: tidy the README", "codex", "Tidy the README"},
		{"Ask Claude to review the chart templates", "claude", "Review the chart templates"},
		{"Bump the Go toolchain, send this to Orca.", "orca", "Bump the Go toolchain"},
		{"Tidy the README and use Codex", "codex", "Tidy the README"},
		{"Fix the cache. Use the orka.", "orca", "Fix the cache"},
		{"Please send this to Archon - work on issue 12", "archon", "Work on issue 12"},
	}
	for _, c := range cases {
		target, rest, ok := r.ParseOverride(c.in)
		if !ok || target != c.target || rest != c.rest {
			t.Errorf("%q: got %q %q %v", c.in, target, rest, ok)
		}
	}
	for _, in := range []string{
		"Use the cache when building images",
		"Send this to the printer: page two",
		"Make the bot use codex style comments in the middle of things",
		"We should ask Bob to look at it",
		"Fix the build and use caching",
		"",
	} {
		if target, rest, ok := r.ParseOverride(in); ok || rest != in {
			t.Errorf("%q overridden to %q (%q)", in, target, rest)
		}
	}
}

type intake struct {
	mu       sync.Mutex
	handoffs map[string]handoffRecord
	got      []handoffRequest
	runs     map[string]string // id -> record JSON
	runReads int
}

func (in *intake) server(t *testing.T, token string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		in.mu.Lock()
		defer in.mu.Unlock()
		switch {
		case r.URL.Path == "/runs":
			var list []map[string]string
			for id, body := range in.runs {
				list = append(list, map[string]string{"id": id, "sha256": "h" + string(rune(len(body)))})
			}
			json.NewEncoder(w).Encode(list)
			return
		case strings.HasPrefix(r.URL.Path, "/runs/"):
			in.runReads++
			w.Write([]byte(in.runs[strings.TrimPrefix(r.URL.Path, "/runs/")]))
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "console token required", 401)
			return
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/handoffs":
			var req handoffRequest
			dec := json.NewDecoder(r.Body)
			dec.DisallowUnknownFields()
			if err := dec.Decode(&req); err != nil {
				http.Error(w, "invalid handoff: "+err.Error(), 400)
				return
			}
			in.got = append(in.got, req)
			if rec, ok := in.handoffs[req.Name]; ok {
				w.WriteHeader(409)
				json.NewEncoder(w).Encode(rec)
				return
			}
			rec := handoffRecord{Name: req.Name, Repo: req.Repo, Agent: req.Agent, Status: "preparing", Step: "queued"}
			in.handoffs[req.Name] = rec
			w.WriteHeader(202)
			json.NewEncoder(w).Encode(rec)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/handoffs/"):
			rec, ok := in.handoffs[strings.TrimPrefix(r.URL.Path, "/handoffs/")]
			if !ok {
				http.NotFound(w, r)
				return
			}
			json.NewEncoder(w).Encode(rec)
		default:
			http.NotFound(w, r)
		}
	}))
}

const token = "0123456789abcdef0123456789abcdef"

func TestOrcaHandoffLifecycle(t *testing.T) {
	in := &intake{handoffs: map[string]handoffRecord{}, runs: map[string]string{}}
	srv := in.server(t, token)
	defer srv.Close()
	o, err := NewOrca(OrcaConfig{Name: "codex", Agent: "codex", HandoffURL: srv.URL, RunRecordsURL: srv.URL, Token: token, DefaultRepo: "steve/agent-project-controller"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	id, err := o.Send(ctx, Task{ID: 42, Brief: "fix the flaky test", Actor: "@steve:example.com"})
	if err != nil || id != "voice-codex-42" {
		t.Fatalf("send %q %v", id, err)
	}
	req := in.got[0]
	if req.Agent != "codex" || req.Repo != "steve/agent-project-controller" || !strings.Contains(req.Brief, "fix the flaky test") || !strings.Contains(req.Brief, "never merge") || req.Actor != "glovecell-voice:@steve:example.com" {
		t.Fatalf("request %+v", req)
	}
	// A retry after a restart finds the same handoff instead of a second agent.
	if again, err := o.Send(ctx, Task{ID: 42, Brief: "fix the flaky test"}); err != nil || again != id || len(in.handoffs) != 1 {
		t.Fatalf("retry %q %v (%d handoffs)", again, err, len(in.handoffs))
	}
	if st, _ := o.Status(ctx, id); st.State != StatePending {
		t.Fatalf("preparing -> %v", st)
	}
	in.mu.Lock()
	in.handoffs[id] = handoffRecord{Name: id, Repo: req.Repo, Agent: "codex", Status: "started", Branch: "orca/voice-codex-42"}
	in.mu.Unlock()
	if st, _ := o.Status(ctx, id); st.State != StateRunning {
		t.Fatalf("started -> %v", st)
	}
	in.mu.Lock()
	in.runs["run1"] = `{"tasks":[{"status":"done","git":{"branch":"other"},"attempts":[{"outcome":"succeeded","summary":"not ours"}]},{"status":"done","git":{"branch":"orca/voice-codex-42"},"attempts":[{"outcome":"failed","lastError":"x"},{"outcome":"succeeded","summary":"Fixed the flaky test in PR 93."}]}]}`
	in.mu.Unlock()
	res, err := o.Result(ctx, id)
	if err != nil || res.State != StateSucceeded || res.Text != "Fixed the flaky test in PR 93." {
		t.Fatalf("result %+v %v", res, err)
	}
	reads := in.runReads
	o.Result(ctx, id)
	if in.runReads != reads {
		t.Fatal("unchanged run record fetched again")
	}
	in.mu.Lock()
	in.handoffs["voice-codex-43"] = handoffRecord{Name: "voice-codex-43", Status: "failed", Step: "clone", Error: "repository not found"}
	in.mu.Unlock()
	if res, _ := o.Result(ctx, "voice-codex-43"); res.State != StateFailed || !strings.Contains(res.Text, "repository not found") {
		t.Fatalf("failed handoff %+v", res)
	}
	if _, err := o.Status(ctx, "../etc"); err == nil {
		t.Fatal("bad id accepted")
	}
}

func TestOrcaRefusals(t *testing.T) {
	in := &intake{handoffs: map[string]handoffRecord{}}
	srv := in.server(t, token)
	defer srv.Close()
	ctx := context.Background()
	o, _ := NewOrca(OrcaConfig{HandoffURL: srv.URL, Token: strings.Repeat("x", 32)})
	if _, err := o.Send(ctx, Task{ID: 1, Brief: "x", Repo: "steve/repo"}); err == nil || !strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "xxxx") {
		t.Fatalf("wrong token: %v", err)
	}
	if _, err := o.Send(ctx, Task{ID: 1, Brief: "x"}); err == nil {
		t.Fatal("no repository accepted")
	}
	for name, c := range map[string]OrcaConfig{
		"short token": {HandoffURL: srv.URL, Token: "short"},
		"agent":       {HandoffURL: srv.URL, Token: token, Agent: "grok"},
		"url":         {HandoffURL: "ftp://x", Token: token},
		"creds":       {HandoffURL: "http://u:p@x", Token: token},
		"repo":        {HandoffURL: srv.URL, Token: token, DefaultRepo: "../x"},
	} {
		if _, err := NewOrca(c); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if _, err := (Archon{}).Send(ctx, Task{}); !errors.Is(err, ErrUnsupported) {
		t.Fatal("archon stub accepted a task")
	}
}
