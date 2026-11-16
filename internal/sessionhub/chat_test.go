package sessionhub

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/microsoft/agent-host-protocol/clients/go/ahptypes"
)

func approveReject() []ahptypes.ConfirmationOption {
	return []ahptypes.ConfirmationOption{
		{Id: "allow", Label: "Allow", Kind: ahptypes.ConfirmationOptionKindApprove},
		{Id: "deny", Label: "Reject", Kind: ahptypes.ConfirmationOptionKindDeny},
	}
}

func statusOf(v *chatView) string {
	state, kind, _ := v.status()
	if kind != "" {
		return state + "/" + kind
	}
	return state
}

// Every app's turn moves the terminal through working, blocked and idle, so
// Herdr shows the hub chat's real state whichever app is driving it.
func TestChatViewStatusFollowsEveryApp(t *testing.T) {
	v := newChatView()
	if statusOf(v) != "idle" {
		t.Fatalf("fresh chat: %s", statusOf(v))
	}
	out := v.apply(&ahptypes.ChatTurnStartedAction{TurnId: "theirs", Message: ahptypes.Message{Text: "fix the tests"}})
	if !strings.Contains(out, "you, in another app: fix the tests") || statusOf(v) != "working" {
		t.Fatalf("another app's turn: %q %s", out, statusOf(v))
	}
	out = v.apply(&ahptypes.ChatDeltaAction{TurnId: "theirs", Content: "Looking"})
	if out != "Looking" {
		t.Fatalf("delta %q", out)
	}
	out = v.apply(&ahptypes.ChatToolCallReadyAction{TurnId: "theirs", ToolCallId: "t1", InvocationMessage: plain("Run go test ./..."), Options: approveReject()})
	if !strings.Contains(out, "\n? Permission: Run go test ./...") || !strings.Contains(out, "1) Allow") || !strings.Contains(out, "2) Reject") {
		t.Fatalf("question %q", out)
	}
	if state, kind, msg := v.status(); state != "blocked" || kind != "permission" || msg != "Run go test ./..." {
		t.Fatalf("blocked status %s %s %q", state, kind, msg)
	}
	// Another app answers first: the question closes here.
	allow := "allow"
	out = v.apply(&ahptypes.ChatToolCallConfirmedAction{TurnId: "theirs", ToolCallId: "t1", Approved: true, SelectedOptionId: &allow})
	if !strings.Contains(out, "(answered: Allow)") || statusOf(v) != "working" {
		t.Fatalf("answered elsewhere: %q %s", out, statusOf(v))
	}
	if _, err := v.answer("1"); err == nil {
		t.Fatal("a settled question must not be answerable")
	}
	v.apply(&ahptypes.ChatTurnCompleteAction{TurnId: "theirs"})
	if statusOf(v) != "idle" {
		t.Fatalf("after the turn: %s", statusOf(v))
	}
}

func TestChatViewOwnTurnAndErrors(t *testing.T) {
	v := newChatView()
	v.ownTurns["mine"] = true
	if out := v.apply(&ahptypes.ChatTurnStartedAction{TurnId: "mine", Message: ahptypes.Message{Text: "hi"}}); out != "" {
		t.Fatalf("own prompt is already on screen: %q", out)
	}
	out := v.apply(&ahptypes.ChatErrorAction{TurnId: "mine", Part: ahptypes.ErrorResponsePart{Error: ahptypes.ErrorInfo{Message: "model unavailable"}}})
	if !strings.Contains(out, "[error] model unavailable") {
		t.Fatalf("error %q", out)
	}
	if state, _, msg := v.status(); state != "error" || msg != "model unavailable" {
		t.Fatalf("error status %s %q", state, msg)
	}
	v.apply(&ahptypes.ChatTurnStartedAction{TurnId: "next"})
	if statusOf(v) != "working" {
		t.Fatal("a new turn clears the error")
	}
	v.apply(&ahptypes.ChatTurnCancelledAction{TurnId: "next"})
	if statusOf(v) != "idle" {
		t.Fatalf("cancelled: %s", statusOf(v))
	}
}

// A turn that ends withdraws its open questions.
func TestChatViewTurnEndWithdrawsQuestions(t *testing.T) {
	v := newChatView()
	v.apply(&ahptypes.ChatTurnStartedAction{TurnId: "a"})
	v.apply(&ahptypes.ChatToolCallReadyAction{TurnId: "a", ToolCallId: "t1", InvocationMessage: plain("rm -rf build"), Options: approveReject()})
	out := v.apply(&ahptypes.ChatTurnCancelledAction{TurnId: "a"})
	if !strings.Contains(out, "withdrawn") || statusOf(v) != "idle" {
		t.Fatalf("withdraw %q %s", out, statusOf(v))
	}
}

func TestChatViewAnswer(t *testing.T) {
	v := newChatView()
	v.apply(&ahptypes.ChatToolCallReadyAction{TurnId: "a", ToolCallId: "t1", InvocationMessage: plain("edit main.go"), Options: approveReject()})
	for _, bad := range []string{"0", "3", "yes", ""} {
		if _, err := v.answer(bad); err == nil {
			t.Errorf("answer %q accepted", bad)
		}
	}
	c, err := v.answer(" 2 ")
	if err != nil {
		t.Fatal(err)
	}
	// The hub refuses a denial that carries a confirmed reason or lacks a
	// denial reason, and an approval without a confirmed reason.
	if c.Approved || *c.SelectedOptionId != "deny" || c.TurnId != "a" || c.ToolCallId != "t1" || c.Confirmed != nil || c.Reason == nil || *c.Reason != ahptypes.ToolCallCancellationReasonDenied || c.Type != ahptypes.ActionTypeChatToolCallConfirmed {
		t.Fatalf("denial %+v", c)
	}
	c, _ = v.answer("1")
	if !c.Approved || *c.SelectedOptionId != "allow" || c.Confirmed == nil || *c.Confirmed != ahptypes.ToolCallConfirmationReasonUserAction || c.Reason != nil {
		t.Fatalf("approval %+v", c)
	}
	// A call the hub settled itself is not asked.
	settled := ahptypes.ToolCallConfirmationReasonNotNeeded
	w := newChatView()
	if out := w.apply(&ahptypes.ChatToolCallReadyAction{ToolCallId: "t2", Options: approveReject(), Confirmed: &settled}); strings.Contains(out, "Permission") {
		t.Fatalf("settled call asked: %q", out)
	}
}

// Attaching mid-turn shows the history and asks a question that was already
// open (the ACP connector replays finished turns only).
func TestChatViewLoad(t *testing.T) {
	v := newChatView()
	state := &ahptypes.ChatState{
		Turns: []ahptypes.Turn{{Message: ahptypes.Message{Text: "first"}, State: ahptypes.TurnStateComplete,
			ResponseParts: []ahptypes.ResponsePart{{Value: &ahptypes.ReasoningResponsePart{Content: "hidden"}}, {Value: &ahptypes.MarkdownResponsePart{Content: "done"}}}}},
		ActiveTurn: &ahptypes.ActiveTurn{Id: "live", Message: ahptypes.Message{Text: "second"}, ResponseParts: []ahptypes.ResponsePart{
			{Value: &ahptypes.ToolCallResponsePart{ToolCall: ahptypes.ToolCallState{Value: &ahptypes.ToolCallPendingConfirmationState{
				ToolCallId: "t9", DisplayName: "Shell", InvocationMessage: plain("make deploy"), Options: approveReject()}}}}}},
	}
	out := v.load(state)
	for _, want := range []string{"> you: first", "done\n", "> you: second", "[tool] Shell", "? Permission: make deploy"} {
		if !strings.Contains(out, want) {
			t.Errorf("load lacks %q in %q", want, out)
		}
	}
	if strings.Contains(out, "hidden") {
		t.Error("reasoning is not replayed")
	}
	if statusOf(v) != "blocked/permission" || v.active != "live" {
		t.Fatalf("status after load %s", statusOf(v))
	}
}

func plain(s string) ahptypes.StringOrMarkdown { return ahptypes.StringOrMarkdown{Plain: &s} }

func TestStatusReport(t *testing.T) {
	got := statusReport("blocked", "cell-codex", "permission", "Run\n\x1b[31mls", "Fix tests")
	b64 := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	want := "\x1b]7501;state=blocked:app=cell-codex:kind=permission:msg=" + b64("Run [31mls") + ":title=" + b64("Fix tests") + "\x1b\\"
	if got != want {
		t.Fatalf("report %q, want %q", got, want)
	}
	if got := statusReport("idle", "cell-codex", "", "", ""); got != "\x1b]7501;state=idle:app=cell-codex\x1b\\" {
		t.Fatalf("idle %q", got)
	}
	if got := oneLine(strings.Repeat("é", 300), 80); len([]rune(got)) != 80 || !strings.HasSuffix(got, "…") {
		t.Fatalf("truncate %q", got)
	}
}

func TestAgentSessions(t *testing.T) {
	items := []ahptypes.SessionSummary{
		{Resource: "ahp-session:/a", Provider: "cell-codex", Title: "old", ModifiedAt: "2026-11-15T10:00:00Z"},
		{Resource: "ahp-session:/b", Provider: "cell-claude", Title: "other agent", ModifiedAt: "2026-11-16T10:00:00Z"},
		{Resource: "ahp-session:/c", Provider: "cell-codex", Title: "new", ModifiedAt: "2026-11-16T09:00:00Z", Status: ahptypes.SessionStatusInProgress},
	}
	got := agentSessions(items, "cell-codex")
	if len(got) != 2 || got[0].URI != "ahp-session:/c" || !got[0].Busy || got[1].URI != "ahp-session:/a" {
		t.Fatalf("sessions %+v", got)
	}
	if len(agentSessions(items, "")) != 3 {
		t.Fatal("no agent lists every session")
	}
}

func TestPasteReader(t *testing.T) {
	lines := []string{"hello", pasteStart + "line one", "line two" + pasteEnd, pasteStart + "single" + pasteEnd, "after"}
	p := &pasteReader{next: func() (string, bool) {
		if len(lines) == 0 {
			return "", false
		}
		l := lines[0]
		lines = lines[1:]
		return l, true
	}}
	var got []string
	for {
		l, ok := p.read()
		if !ok {
			break
		}
		got = append(got, l)
	}
	if strings.Join(got, "|") != "hello|line one\nline two|single|after" {
		t.Fatalf("paste %q", got)
	}
}

func TestRunChatRefuses(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "token")
	os.WriteFile(good, []byte(testToken+"\n"), 0o600)
	bad := filepath.Join(dir, "bad")
	os.WriteFile(bad, []byte("guessable"), 0o600)
	cases := map[string]ChatOptions{
		"bad agent id":        {URL: "ws://127.0.0.1:8765/ahp", TokenFile: good, Agent: "Cell Codex"},
		"bad session":         {URL: "ws://127.0.0.1:8765/ahp", TokenFile: good, Session: "file:///etc/passwd"},
		"session and new":     {URL: "ws://127.0.0.1:8765/ahp", TokenFile: good, Session: "ahp-session:/x", New: true},
		"no token":            {URL: "ws://127.0.0.1:8765/ahp", TokenFile: filepath.Join(dir, "none")},
		"invalid token":       {URL: "ws://127.0.0.1:8765/ahp", TokenFile: bad},
		"non-loopback hub":    {URL: "ws://hub.example.com:8765/ahp", TokenFile: good},
		"tls somewhere else":  {URL: "wss://127.0.0.1:8765/ahp", TokenFile: good},
		"no hub listening at": {URL: "ws://127.0.0.1:1/ahp", TokenFile: good},
	}
	for name, opts := range cases {
		if err := RunChat(context.Background(), opts, strings.NewReader(""), &strings.Builder{}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// fakeHC records herdr-core CLI calls and answers `ls` from a set of
// labelled sessions.
type fakeHC struct {
	calls    [][]string
	labelled map[string]bool
	failRun  error
}

func (f *fakeHC) run(_ context.Context, args ...string) ([]byte, error) {
	f.calls = append(f.calls, args)
	switch args[0] {
	case "ls":
		session := strings.TrimPrefix(args[len(args)-1], HerdrSessionLabel+"=")
		if f.labelled[session] {
			return []byte(`{"terminals":[{"address":"t1"}]}`), nil
		}
		return []byte(`{"terminals":[]}`), nil
	case "run":
		return nil, f.failRun
	}
	return nil, errors.New("unexpected")
}

func (f *fakeHC) runs() [][]string {
	var out [][]string
	for _, c := range f.calls {
		if c[0] == "run" {
			out = append(out, c)
		}
	}
	return out
}

func TestHerdrPanesOpensOneTerminalPerSession(t *testing.T) {
	sessions := []ahptypes.SessionSummary{
		{Resource: "ahp-session:/from-t3", Provider: "cell-codex"},
		{Resource: "ahp-session:/from-herdr", Provider: "cell-codex"},
	}
	hc := &fakeHC{labelled: map[string]bool{"ahp-session:/from-herdr": true}}
	p := &HerdrPanes{HC: hc.run, Cwd: "/workspace/repo",
		List: func(context.Context) ([]ahptypes.SessionSummary, error) { return sessions, nil }}
	ctx := context.Background()
	if err := p.Sync(ctx); err != nil || len(hc.calls) != 0 {
		t.Fatalf("first sight waits a round: %v %v", err, hc.calls)
	}
	if err := p.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	runs := hc.runs()
	if len(runs) != 1 {
		t.Fatalf("want one terminal (the other session has one), got %v", runs)
	}
	want := []string{"run", "--bg", "--json", "--no-parent", "--no-notify", "--name", herdrTerminalName("cell-codex", "ahp-session:/from-t3"),
		"--label", HerdrSessionLabel + "=ahp-session:/from-t3", "--cwd", "/workspace/repo", "--",
		Binary, "session-hub-chat", "--session", "ahp-session:/from-t3"}
	if strings.Join(runs[0], " ") != strings.Join(want, " ") {
		t.Fatalf("run %q\nwant %q", runs[0], want)
	}
	// Handled sessions are not checked again, so a closed terminal stays closed.
	calls := len(hc.calls)
	if err := p.Sync(ctx); err != nil || len(hc.calls) != calls {
		t.Fatalf("handled sessions were checked again: %v", hc.calls[calls:])
	}
}

func TestHerdrPanesRetriesFailedRuns(t *testing.T) {
	sessions := []ahptypes.SessionSummary{{Resource: "ahp-session:/s", Provider: "cell-codex"}}
	hc := &fakeHC{failRun: errors.New("core restarting")}
	p := &HerdrPanes{HC: hc.run, List: func(context.Context) ([]ahptypes.SessionSummary, error) { return sessions, nil }}
	ctx := context.Background()
	_ = p.Sync(ctx)
	if err := p.Sync(ctx); err == nil {
		t.Fatal("a failed run must be reported")
	}
	hc.failRun = nil
	if err := p.Sync(ctx); err != nil || len(hc.runs()) != 2 {
		t.Fatalf("retry: %v %v", err, hc.runs())
	}
}

func TestHerdrTerminalName(t *testing.T) {
	a := herdrTerminalName("cell-codex", "ahp-session:/one")
	if a == herdrTerminalName("cell-codex", "ahp-session:/two") || !strings.HasPrefix(a, "cell-codex-") || len(a) != len("cell-codex-")+8 {
		t.Fatalf("name %q", a)
	}
	long := herdrTerminalName("a-very-long-agent-id-with-many-parts", "ahp-session:/x")
	if len(long) > 32 || strings.Contains(long, "--") {
		t.Fatalf("long name %q", long)
	}
}

func TestHerdrTemplates(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "cell-claude.md"), []byte("---\ncommand: my own\n---\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "cell-gone.md"), []byte("---\ncommand: x\n"+herdrTemplateMarker+"\n---\n"), 0o644)
	p := &HerdrPanes{TemplatesDir: dir, Cwd: "/workspace/repo"}
	agents := []ahptypes.AgentInfo{{Provider: "cell-codex", DisplayName: "Codex\nInjected: yes"}, {Provider: "cell-claude", DisplayName: "Claude"}, {Provider: "Bad Id"}}
	if err := p.writeTemplates(agents); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "cell-codex.md"))
	if err != nil {
		t.Fatal(err)
	}
	want := "---\ndescription: New Codex Injected: yes chat in the cell's session hub, shared with the other apps\n" +
		"command: " + Binary + " session-hub-chat --agent cell-codex --new --cwd /workspace/repo\n" + herdrTemplateMarker + "\n---\n"
	if string(raw) != want {
		t.Fatalf("template %q", raw)
	}
	if own, _ := os.ReadFile(filepath.Join(dir, "cell-claude.md")); string(own) != "---\ncommand: my own\n---\n" {
		t.Fatal("the operator's own template was overwritten")
	}
	if _, err := os.Stat(filepath.Join(dir, "cell-gone.md")); !os.IsNotExist(err) {
		t.Fatal("a template for an agent the hub no longer lists must go")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Fatalf("templates %v", entries)
	}
}

func TestReadClientAgents(t *testing.T) {
	dir := t.TempDir()
	if agents, err := ReadClientAgents(filepath.Join(dir, "absent.json")); err != nil || len(agents) != 0 {
		t.Fatalf("outside hub cells there are no agents: %v %v", agents, err)
	}
	path := filepath.Join(dir, "agents.json")
	os.WriteFile(path, []byte(`[{"id":"cell-codex","displayName":"Codex","command":"/usr/local/bin/codex-acp","args":["--x"]},{"id":"Bad Id","displayName":"x","command":"/bin/sh"}]`), 0o600)
	agents, err := ReadClientAgents(path)
	if err != nil || len(agents) != 1 || agents[0].Provider != "cell-codex" || agents[0].DisplayName != "Codex" {
		t.Fatalf("agents %+v %v", agents, err)
	}
	os.WriteFile(path, []byte(`{`), 0o600)
	if _, err := ReadClientAgents(path); err == nil {
		t.Fatal("a broken list must be reported")
	}
	// Templates follow the mounted list.
	tpl := filepath.Join(dir, "templates")
	os.WriteFile(path, []byte(`[{"id":"cell-codex","displayName":"Codex"}]`), 0o600)
	p := &HerdrPanes{HC: (&fakeHC{}).run, AgentsFile: path, TemplatesDir: tpl,
		List: func(context.Context) ([]ahptypes.SessionSummary, error) { return nil, errors.New("hub not up yet") }}
	if err := p.Sync(context.Background()); err == nil {
		t.Fatal("hub error must be reported")
	}
	if _, err := os.Stat(filepath.Join(tpl, "cell-codex.md")); err != nil {
		t.Fatal("templates are written even before the hub answers")
	}
}

// A number typed just after another app settled the question is not sent
// to the agent as a message.
func TestChatViewLateAnswer(t *testing.T) {
	v := newChatView()
	now := time.Now()
	if v.lateAnswer("1", now) {
		t.Fatal("no question has closed yet")
	}
	v.apply(&ahptypes.ChatToolCallReadyAction{TurnId: "a", ToolCallId: "t1", InvocationMessage: plain("x"), Options: approveReject()})
	if v.lateAnswer("1", time.Now()) {
		t.Fatal("an open question takes the answer")
	}
	v.apply(&ahptypes.ChatToolCallConfirmedAction{TurnId: "a", ToolCallId: "t1", Approved: true})
	if !v.lateAnswer(" 1 ", time.Now()) || v.lateAnswer("1 more thing", time.Now()) || v.lateAnswer("1", time.Now().Add(time.Minute)) {
		t.Fatal("late-answer window")
	}
}
