package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/approvalgate"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/voice/service"
)

type call struct {
	caller service.Caller
	kind   string
	text   string
	repo   string
	pr     int
	task   int64
	final  bool
	room   string
	thread string
}

type fakeBackend struct {
	calls   []call
	cardErr error
}

func (f *fakeBackend) NotifyMessage(_ context.Context, c service.Caller, n service.Note) (string, error) {
	f.calls = append(f.calls, call{caller: c, kind: n.Kind, text: n.Text, task: n.Task, final: n.Final, room: n.Room, thread: n.Thread})
	if n.Task == 99 {
		return "", service.ErrUnknownTask
	}
	return "$e", nil
}

func (f *fakeBackend) RequestCard(_ context.Context, c service.Caller, repo string, pr int, note string) error {
	f.calls = append(f.calls, call{caller: c, kind: "approval", repo: repo, pr: pr, text: note})
	return f.cardErr
}

const (
	orcaToken = "orca-token-0123456789abcdef0123456789"
	cellToken = "cell-token-0123456789abcdef0123456789"
)

func handler(t *testing.T) (*Handler, *fakeBackend) {
	b := &fakeBackend{}
	h, err := New(b, []CallerConfig{
		{Name: "orca", TokenSHA256: HashToken(orcaToken), Kinds: AllKinds, Targets: []string{"orca"}, PerHour: 600},
		{Name: "cell-apc", TokenSHA256: HashToken(cellToken), Room: "!apc:example.com"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return h, b
}

func post(h http.Handler, token string, body any) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/notify", bytes.NewReader(b))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func TestNotifyAuth(t *testing.T) {
	h, b := handler(t)
	for _, tok := range []string{"", "short", strings.Repeat("x", 40), HashToken(orcaToken)} {
		if w := post(h, tok, Request{Kind: KindMessage, Text: "hi"}); w.Code != 401 || w.Header().Get("WWW-Authenticate") != "Bearer" {
			t.Fatalf("token %q: %d", tok, w.Code)
		}
	}
	if len(b.calls) != 0 {
		t.Fatal("unauthenticated call reached the backend")
	}
	if w := post(h, cellToken, Request{Kind: KindMessage, Text: "build is green"}); w.Code != 202 {
		t.Fatalf("message %d %s", w.Code, w.Body)
	}
	if c := b.calls[0]; c.caller.Name != "cell-apc" || c.caller.Room != "!apc:example.com" || c.text != "build is green" {
		t.Fatalf("call %+v", c)
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/notify", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 405 {
		t.Fatalf("GET %d", w.Code)
	}
}

// The body orca-pilot voice send posts (cluster repo, orca-pilot voice.go).
func TestNotifyOrcaPilotShape(t *testing.T) {
	h, b := handler(t)
	for _, kind := range []string{"message", "question", "blocked", "done"} {
		w := post(h, orcaToken, map[string]string{"kind": kind, "text": "Build is green.", "room": "!inbox:example.com", "thread": "$abc"})
		var resp struct{ ID, Message string }
		json.Unmarshal(w.Body.Bytes(), &resp)
		if w.Code != 202 || resp.ID != "$e" || resp.Message == "" {
			t.Fatalf("%s: %d %s", kind, w.Code, w.Body)
		}
	}
	if c := b.calls[3]; c.kind != "done" || c.room != "!inbox:example.com" || c.thread != "$abc" {
		t.Fatalf("call %+v", c)
	}
	if w := post(h, orcaToken, map[string]string{"kind": "message", "text": "x", "thread": "has space"}); w.Code != 400 {
		t.Fatalf("thread with a space: %d", w.Code)
	}
}

func TestLoadTokenDir(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "orca-pilot"), []byte(orcaToken+"\n"), 0o600)
	os.MkdirAll(filepath.Join(dir, "..2026_10_30"), 0o700)
	os.Symlink("..2026_10_30", filepath.Join(dir, "..data"))
	targets, err := ParseTargets("orca-pilot=orca|codex")
	if err != nil {
		t.Fatal(err)
	}
	callers, err := LoadTokenDir(dir, nil, targets)
	if err != nil || len(callers) != 1 || callers[0].Name != "orca-pilot" || callers[0].TokenSHA256 != HashToken(orcaToken) || len(callers[0].Targets) != 2 {
		t.Fatalf("%+v %v", callers, err)
	}
	h, _ := New(&fakeBackend{}, callers)
	if w := post(h, orcaToken, Request{Kind: KindDone, Text: "done"}); w.Code != 202 {
		t.Fatalf("token from the directory refused: %d", w.Code)
	}
	os.WriteFile(filepath.Join(dir, "short"), []byte("abc"), 0o600)
	if _, err := LoadTokenDir(dir, nil, nil); err == nil || strings.Contains(err.Error(), "abc") {
		t.Fatalf("short token: %v", err)
	}
	if _, err := ParseTargets("orca-pilot"); err == nil {
		t.Fatal("bad targets accepted")
	}
}

func TestNotifyKindsAndValidation(t *testing.T) {
	h, b := handler(t)
	if w := post(h, cellToken, Request{Kind: KindApproval, Repo: "steve/x", PR: 1}); w.Code != 403 {
		t.Fatalf("approval without the kind: %d", w.Code)
	}
	for name, body := range map[string]any{
		"unknown kind":    Request{Kind: "approve", Text: "4721"},
		"empty text":      Request{Kind: KindMessage},
		"repo on message": Request{Kind: KindMessage, Text: "x", Repo: "steve/x"},
		"bad repo":        Request{Kind: KindApproval, Repo: "../x", PR: 1},
		"no pr":           Request{Kind: KindApproval, Repo: "steve/x"},
		"unknown field":   map[string]any{"kind": "message", "text": "x", "code": "4721"},
	} {
		w := post(h, orcaToken, body)
		if w.Code != 400 && w.Code != 403 {
			t.Errorf("%s: %d", name, w.Code)
		}
	}
	if w := post(h, orcaToken, Request{Kind: KindMessage, Text: strings.Repeat("x", MaxText+1)}); w.Code != 413 {
		t.Fatalf("long text %d", w.Code)
	}
	if w := post(h, orcaToken, Request{Kind: KindMessage, Text: "done", Task: 99, Final: true}); w.Code != 404 {
		t.Fatalf("unknown task %d", w.Code)
	}
	if len(b.calls) != 1 {
		t.Fatalf("invalid requests reached the backend: %+v", b.calls)
	}
}

func TestNotifyApprovalOnlyRequestsACard(t *testing.T) {
	h, b := handler(t)
	w := post(h, orcaToken, Request{Kind: KindApproval, Repo: "steve/agent-project-controller", PR: 92, Text: "ready for review"})
	if w.Code != 202 {
		t.Fatalf("approval %d %s", w.Code, w.Body)
	}
	var resp map[string]string
	json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp) != 1 || !strings.Contains(resp["message"], "Steve decides") {
		t.Fatalf("response leaks more than a status: %v", resp)
	}
	if c := b.calls[0]; c.kind != "approval" || c.repo != "steve/agent-project-controller" || c.pr != 92 {
		t.Fatalf("call %+v", c)
	}
	b.cardErr = approvalgate.ErrNotAllowed
	if w := post(h, orcaToken, Request{Kind: KindApproval, Repo: "mallory/x", PR: 1}); w.Code != 403 {
		t.Fatalf("uncovered repo %d", w.Code)
	}
	b.cardErr = errors.New("forgejo down")
	if w := post(h, orcaToken, Request{Kind: KindApproval, Repo: "steve/x", PR: 2}); w.Code != 503 {
		t.Fatalf("backend error %d", w.Code)
	}
}

func TestNotifyRateLimited(t *testing.T) {
	h, err := New(&fakeBackend{}, []CallerConfig{
		{Name: "orca", TokenSHA256: HashToken(orcaToken), PerHour: 6},
		{Name: "cell-apc", TokenSHA256: HashToken(cellToken)},
	})
	if err != nil {
		t.Fatal(err)
	}
	codes := map[int]int{}
	for i := 0; i < 5; i++ {
		codes[post(h, orcaToken, Request{Kind: KindMessage, Text: "x"}).Code]++
	}
	if codes[429] == 0 {
		t.Fatalf("no rate limit after a burst: %v", codes)
	}
	// Another caller has its own budget.
	if w := post(h, cellToken, Request{Kind: KindMessage, Text: "x"}); w.Code != 202 {
		t.Fatalf("other caller limited: %d", w.Code)
	}
}

func TestLoadCallers(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "callers.json")
	os.WriteFile(good, []byte(`[{"name":"orca","tokenSHA256":"`+HashToken(orcaToken)+`","kinds":["message","approval"]}]`), 0o600)
	if c, err := LoadCallers(good); err != nil || len(c) != 1 {
		t.Fatalf("%v %v", c, err)
	}
	for name, content := range map[string]string{
		"plain token":  `[{"name":"orca","tokenSHA256":"` + orcaToken + `"}]`,
		"bad kind":     `[{"name":"orca","tokenSHA256":"` + HashToken(orcaToken) + `","kinds":["approve"]}]`,
		"duplicate":    `[{"name":"a","tokenSHA256":"` + HashToken("1") + `"},{"name":"a","tokenSHA256":"` + HashToken("2") + `"}]`,
		"not an array": `{"name":"orca"}`,
	} {
		p := filepath.Join(dir, name)
		os.WriteFile(p, []byte(content), 0o600)
		if _, err := LoadCallers(p); err == nil {
			t.Errorf("%s accepted", name)
		} else if strings.Contains(err.Error(), orcaToken) {
			t.Errorf("%s: error leaks the token", name)
		}
	}
}
