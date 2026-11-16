package sessionhub

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	acp "github.com/coder/acp-go-sdk"
	"github.com/microsoft/agent-host-protocol/clients/go/ahptypes"
)

func kinds(updates []acp.SessionUpdate) []string {
	out := []string{}
	for _, u := range updates {
		switch {
		case u.UserMessageChunk != nil:
			out = append(out, "user:"+u.UserMessageChunk.Content.Text.Text)
		case u.AgentMessageChunk != nil:
			out = append(out, "agent:"+u.AgentMessageChunk.Content.Text.Text)
		case u.AgentThoughtChunk != nil:
			out = append(out, "thought:"+u.AgentThoughtChunk.Content.Text.Text)
		case u.ToolCall != nil:
			out = append(out, "tool:"+string(u.ToolCall.ToolCallId)+":"+string(u.ToolCall.Status))
		case u.ToolCallUpdate != nil:
			status := ""
			if u.ToolCallUpdate.Status != nil {
				status = string(*u.ToolCallUpdate.Status)
			}
			out = append(out, "toolupdate:"+string(u.ToolCallUpdate.ToolCallId)+":"+status)
		default:
			out = append(out, "other")
		}
	}
	return out
}

func TestTranslate(t *testing.T) {
	own := map[string]bool{"mine": true}
	cases := []struct {
		name    string
		action  any
		want    string
		outcome *turnOutcome
	}{
		{"another app's turn is shown as its message", &ahptypes.ChatTurnStartedAction{TurnId: "theirs", Message: ahptypes.Message{Text: "hi"}}, "user:hi", nil},
		{"this app's own turn is not echoed", &ahptypes.ChatTurnStartedAction{TurnId: "mine", Message: ahptypes.Message{Text: "hi"}}, "", nil},
		{"delta", &ahptypes.ChatDeltaAction{TurnId: "mine", Content: "abc"}, "agent:abc", nil},
		{"reasoning", &ahptypes.ChatReasoningAction{TurnId: "mine", Content: "hmm"}, "thought:hmm", nil},
		{"markdown part", &ahptypes.ChatResponsePartAction{TurnId: "mine", Part: ahptypes.ResponsePart{Value: &ahptypes.MarkdownResponsePart{Content: "start"}}}, "agent:start", nil},
		{"tool start", &ahptypes.ChatToolCallStartAction{ToolCallId: "t1", DisplayName: "ls"}, "tool:t1:pending", nil},
		{"tool ready", &ahptypes.ChatToolCallReadyAction{ToolCallId: "t1"}, "toolupdate:t1:in_progress", nil},
		{"tool failed", &ahptypes.ChatToolCallCompleteAction{ToolCallId: "t1", Result: ahptypes.ToolCallResult{Success: false}}, "toolupdate:t1:failed", nil},
		{"turn complete", &ahptypes.ChatTurnCompleteAction{TurnId: "mine"}, "", &turnOutcome{turnID: "mine", reason: acp.StopReasonEndTurn}},
		{"turn cancelled", &ahptypes.ChatTurnCancelledAction{TurnId: "mine"}, "", &turnOutcome{turnID: "mine", reason: acp.StopReasonCancelled}},
		{"error", &ahptypes.ChatErrorAction{TurnId: "mine", Part: ahptypes.ErrorResponsePart{Error: ahptypes.ErrorInfo{Message: "boom"}}}, "", &turnOutcome{turnID: "mine", reason: acp.StopReasonEndTurn, err: "boom"}},
		{"unrelated action", &ahptypes.ChatDraftChangedAction{}, "", nil},
	}
	for _, c := range cases {
		updates, outcome := translate(c.action, own)
		if got := strings.Join(kinds(updates), ","); got != c.want {
			t.Errorf("%s: updates %q, want %q", c.name, got, c.want)
		}
		if (outcome == nil) != (c.outcome == nil) || outcome != nil && *outcome != *c.outcome {
			t.Errorf("%s: outcome %+v, want %+v", c.name, outcome, c.outcome)
		}
	}
}

func TestReplayTurn(t *testing.T) {
	turn := ahptypes.Turn{Message: ahptypes.Message{Text: "question"}, ResponseParts: []ahptypes.ResponsePart{
		{Value: &ahptypes.ReasoningResponsePart{Content: "thinking"}},
		{Value: &ahptypes.MarkdownResponsePart{Content: "answer"}},
		{Value: &ahptypes.ToolCallResponsePart{}},
	}}
	if got := strings.Join(kinds(replayTurn(turn)), ","); got != "user:question,thought:thinking,agent:answer" {
		t.Fatalf("replay %q", got)
	}
}

func TestNeedsPermission(t *testing.T) {
	options := []ahptypes.ConfirmationOption{{Id: "allow", Kind: ahptypes.ConfirmationOptionKindApprove}}
	settled := ahptypes.ToolCallConfirmationReasonNotNeeded
	if !needsPermission(&ahptypes.ChatToolCallReadyAction{Options: options}) {
		t.Error("unconfirmed call with options must be asked")
	}
	if needsPermission(&ahptypes.ChatToolCallReadyAction{Options: options, Confirmed: &settled}) {
		t.Error("a call the hub already settled must not be asked")
	}
	if needsPermission(&ahptypes.ChatToolCallReadyAction{}) {
		t.Error("a call without options must not be asked")
	}
}

// A question another app answered is withdrawn from this one.
func TestSettleWithdrawsQuestions(t *testing.T) {
	s := &hubSession{pending: map[string]pendingQuestion{}}
	ctx1, cancel1 := context.WithCancel(context.Background())
	ctx2, cancel2 := context.WithCancel(context.Background())
	s.pending["t1"] = pendingQuestion{turn: "turn-a", cancel: cancel1}
	s.pending["t2"] = pendingQuestion{turn: "turn-b", cancel: cancel2}
	s.settle("turn-a", "t1")
	if ctx1.Err() == nil || ctx2.Err() != nil {
		t.Fatal("settling one tool call must withdraw only its question")
	}
	s.settle("turn-b", "")
	if ctx2.Err() == nil || len(s.pending) != 0 {
		t.Fatal("ending a turn must withdraw its remaining questions")
	}
}

func TestRunConnectorRefuses(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "token")
	os.WriteFile(good, []byte(testToken+"\n"), 0o600)
	bad := filepath.Join(dir, "bad")
	os.WriteFile(bad, []byte("guessable"), 0o600)
	ctx := context.Background()
	cases := map[string]struct{ url, token, agent string }{
		"bad agent id":     {"ws://127.0.0.1:8765/ahp", good, "Cell Codex"},
		"no token":         {"ws://127.0.0.1:8765/ahp", filepath.Join(dir, "none"), "cell-codex"},
		"invalid token":    {"ws://127.0.0.1:8765/ahp", bad, "cell-codex"},
		"non-loopback hub": {"ws://hub.example.com:8765/ahp", good, "cell-codex"},
		"tls elsewhere":    {"wss://127.0.0.1:8765/ahp", good, "cell-codex"},
	}
	for name, c := range cases {
		if err := RunConnector(ctx, c.url, c.token, c.agent, strings.NewReader(""), &strings.Builder{}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestPromptText(t *testing.T) {
	blocks := []acp.ContentBlock{acp.TextBlock("fix "), acp.ResourceLinkBlock("main.go", "file:///workspace/repo/main.go"), acp.TextBlock("please")}
	if got := promptText(blocks); got != "fix \nfile:///workspace/repo/main.go\nplease" {
		t.Fatalf("prompt %q", got)
	}
}

// A "cancelled" answer, or an option the question did not offer, must not
// reach the hub: with first-answer-wins it would refuse the question for every
// other app (Orca answers "cancelled" to turns it did not start).
func TestConfirmationOnlyForChosenOptions(t *testing.T) {
	ready := &ahptypes.ChatToolCallReadyAction{TurnId: "turn", ToolCallId: "t1", Options: []ahptypes.ConfirmationOption{
		{Id: "allow", Kind: ahptypes.ConfirmationOptionKindApprove}, {Id: "deny", Kind: ahptypes.ConfirmationOptionKindDeny}}}
	if c := confirmation(ready, acp.RequestPermissionOutcome{Cancelled: &acp.RequestPermissionOutcomeCancelled{}}); c != nil {
		t.Fatalf("cancelled dispatched %+v", c)
	}
	if c := confirmation(ready, acp.RequestPermissionOutcome{Selected: &acp.RequestPermissionOutcomeSelected{OptionId: "other"}}); c != nil {
		t.Fatalf("unknown option dispatched %+v", c)
	}
	allow := confirmation(ready, acp.RequestPermissionOutcome{Selected: &acp.RequestPermissionOutcomeSelected{OptionId: "allow"}})
	if allow == nil || !allow.Approved || *allow.SelectedOptionId != "allow" || allow.TurnId != "turn" || allow.ToolCallId != "t1" {
		t.Fatalf("allow %+v", allow)
	}
	deny := confirmation(ready, acp.RequestPermissionOutcome{Selected: &acp.RequestPermissionOutcomeSelected{OptionId: "deny"}})
	if deny == nil || deny.Approved {
		t.Fatalf("deny %+v", deny)
	}
}

// Each hub turn's user message and reply carry their own message id, so an
// app shows other apps' turns as separate messages instead of one long reply.
func TestChunksCarryTurnMessageIDs(t *testing.T) {
	updates, _ := translate(&ahptypes.ChatTurnStartedAction{TurnId: "t9", Message: ahptypes.Message{Text: "hi"}}, nil)
	if id := updates[0].UserMessageChunk.MessageId; id == nil || *id != "t9:user" {
		t.Fatalf("user chunk id %v", id)
	}
	updates, _ = translate(&ahptypes.ChatDeltaAction{TurnId: "t9", Content: "x"}, nil)
	if id := updates[0].AgentMessageChunk.MessageId; id == nil || *id != "t9:agent" {
		t.Fatalf("agent chunk id %v", id)
	}
	replay := replayTurn(ahptypes.Turn{Id: "t1", Message: ahptypes.Message{Text: "q"}, ResponseParts: []ahptypes.ResponsePart{{Value: &ahptypes.MarkdownResponsePart{Content: "a"}}}})
	if *replay[0].UserMessageChunk.MessageId != "t1:user" || *replay[1].AgentMessageChunk.MessageId != "t1:agent" {
		t.Fatal("replayed turn ids")
	}
}
