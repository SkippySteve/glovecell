package sessionhub

import (
	"encoding/base64"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/microsoft/agent-host-protocol/clients/go/ahptypes"
)

// chatView is session-hub-chat's picture of one hub chat: what to print for
// each hub action, which permission questions are open, and the status the
// terminal reports to herdr-core. It does no I/O, so tests drive it with
// actions directly.
type chatView struct {
	ownTurns  map[string]bool // turns this terminal started; their text is already on screen
	active    string          // the running turn, whichever app started it
	lastError string
	questions map[string]question // open permission questions by tool call
	order     []string            // question tool call ids, oldest first
	atLineEnd bool                // the last thing printed ended a line
	closedAt  time.Time           // when the last question closed
}

type question struct {
	turn    string
	title   string
	options []ahptypes.ConfirmationOption
}

func newChatView() *chatView {
	return &chatView{ownTurns: map[string]bool{}, questions: map[string]question{}, atLineEnd: true}
}

// Status words herdr-core understands (OSC 7501).
const (
	statusIdle    = "idle"
	statusWorking = "working"
	statusBlocked = "blocked"
	statusError   = "error"
)

// status is what the terminal reports: blocked while a question is open,
// working while any app's turn runs, error after a failed turn, else idle.
func (v *chatView) status() (state, kind, msg string) {
	if len(v.order) > 0 {
		return statusBlocked, "permission", v.questions[v.order[0]].title
	}
	if v.active != "" {
		return statusWorking, "", ""
	}
	if v.lastError != "" {
		return statusError, "", v.lastError
	}
	return statusIdle, "", ""
}

// load renders the chat's history and its running turn, and picks up any
// question that was already open when this terminal attached.
func (v *chatView) load(state *ahptypes.ChatState) string {
	var b strings.Builder
	for _, t := range state.Turns {
		b.WriteString(v.userLine(t.Message.Text, false))
		b.WriteString(renderParts(t.ResponseParts))
		if t.State == ahptypes.TurnStateError {
			b.WriteString(v.line("[error]"))
		}
	}
	if a := state.ActiveTurn; a != nil {
		v.active = a.Id
		b.WriteString(v.userLine(a.Message.Text, false))
		b.WriteString(renderParts(a.ResponseParts))
		for _, part := range a.ResponseParts {
			tc, ok := part.Value.(*ahptypes.ToolCallResponsePart)
			if !ok {
				continue
			}
			if p, ok := tc.ToolCall.Value.(*ahptypes.ToolCallPendingConfirmationState); ok && len(p.Options) > 0 {
				title := text(p.InvocationMessage)
				if p.ConfirmationTitle != nil {
					title = text(*p.ConfirmationTitle)
				}
				b.WriteString(v.ask(a.Id, p.ToolCallId, title, p.Options))
			}
		}
	}
	v.atLineEnd = b.Len() == 0 || strings.HasSuffix(b.String(), "\n")
	return b.String()
}

func renderParts(parts []ahptypes.ResponsePart) string {
	var b strings.Builder
	for _, part := range parts {
		switch p := part.Value.(type) {
		case *ahptypes.MarkdownResponsePart:
			if p.Content != "" {
				b.WriteString(p.Content)
				if !strings.HasSuffix(p.Content, "\n") {
					b.WriteString("\n")
				}
			}
		case *ahptypes.ToolCallResponsePart:
			if name := toolName(p.ToolCall); name != "" {
				b.WriteString("  [tool] " + name + "\n")
			}
		}
	}
	return b.String()
}

func toolName(s ahptypes.ToolCallState) string {
	switch t := s.Value.(type) {
	case *ahptypes.ToolCallPendingConfirmationState:
		return t.DisplayName
	case *ahptypes.ToolCallRunningState:
		return t.DisplayName
	case *ahptypes.ToolCallCompletedState:
		return t.DisplayName
	case *ahptypes.ToolCallCancelledState:
		return t.DisplayName
	case *ahptypes.ToolCallStreamingState:
		return t.DisplayName
	}
	return ""
}

// line starts s on a fresh line and ends it.
func (v *chatView) line(s string) string {
	prefix := ""
	if !v.atLineEnd {
		prefix = "\n"
	}
	v.atLineEnd = true
	return prefix + s + "\n"
}

// stream appends agent text as it arrives.
func (v *chatView) stream(s string) string {
	if s == "" {
		return ""
	}
	v.atLineEnd = strings.HasSuffix(s, "\n")
	return s
}

func (v *chatView) userLine(msg string, elsewhere bool) string {
	if msg == "" {
		return ""
	}
	who := "you"
	if elsewhere {
		who = "you, in another app"
	}
	return v.line("\n> " + who + ": " + strings.ReplaceAll(msg, "\n", "\n  "))
}

func (v *chatView) ask(turn, toolCall, title string, options []ahptypes.ConfirmationOption) string {
	if _, open := v.questions[toolCall]; open {
		return ""
	}
	v.questions[toolCall] = question{turn: turn, title: title, options: options}
	v.order = append(v.order, toolCall)
	return v.line(v.questionText(toolCall))
}

func (v *chatView) questionText(toolCall string) string {
	q := v.questions[toolCall]
	var b strings.Builder
	b.WriteString("? Permission: " + q.title + "\n")
	for i, o := range q.options {
		fmt.Fprintf(&b, "  %d) %s\n", i+1, o.Label)
	}
	b.WriteString("  Answer with a number. Any app attached to this chat may answer; the first answer counts.")
	return b.String()
}

// settle closes the questions of a tool call, or of every tool call of a
// turn when toolCall is empty, and says how they were settled.
func (v *chatView) settle(turn, toolCall, how string) string {
	var out strings.Builder
	keep := v.order[:0]
	for _, id := range v.order {
		q := v.questions[id]
		if (toolCall != "" && id == toolCall) || (toolCall == "" && q.turn == turn) {
			delete(v.questions, id)
			v.closedAt = time.Now()
			if how != "" {
				out.WriteString(v.line("  (" + how + ")"))
			}
			continue
		}
		keep = append(keep, id)
	}
	v.order = keep
	return out.String()
}

// apply renders one hub action and updates the status.
func (v *chatView) apply(action any) string {
	switch a := action.(type) {
	case *ahptypes.ChatTurnStartedAction:
		v.active, v.lastError = a.TurnId, ""
		if v.ownTurns[a.TurnId] {
			return ""
		}
		return v.userLine(a.Message.Text, true)
	case *ahptypes.ChatDeltaAction:
		return v.stream(a.Content)
	case *ahptypes.ChatResponsePartAction:
		if md, ok := a.Part.Value.(*ahptypes.MarkdownResponsePart); ok {
			return v.stream(md.Content)
		}
	case *ahptypes.ChatToolCallStartAction:
		return v.line("  [tool] " + a.DisplayName)
	case *ahptypes.ChatToolCallReadyAction:
		if needsPermission(a) {
			title := text(a.InvocationMessage)
			if a.ConfirmationTitle != nil {
				title = text(*a.ConfirmationTitle)
			}
			return v.ask(a.TurnId, a.ToolCallId, title, a.Options)
		}
	case *ahptypes.ChatToolCallConfirmedAction:
		if _, open := v.questions[a.ToolCallId]; !open {
			return ""
		}
		how := "denied"
		if a.Approved {
			how = "allowed"
		}
		if a.SelectedOptionId != nil {
			for _, o := range v.questions[a.ToolCallId].options {
				if o.Id == *a.SelectedOptionId {
					how = "answered: " + o.Label
				}
			}
		}
		return v.settle(a.TurnId, a.ToolCallId, how)
	case *ahptypes.ChatToolCallCompleteAction:
		out := v.settle(a.TurnId, a.ToolCallId, "")
		if !a.Result.Success {
			out += v.line("  [tool failed]")
		}
		return out
	case *ahptypes.ChatTurnCompleteAction:
		return v.endTurn(a.TurnId, "", "")
	case *ahptypes.ChatTurnCancelledAction:
		return v.endTurn(a.TurnId, "(cancelled)", "")
	case *ahptypes.ChatErrorAction:
		msg := "the agent stopped with an error"
		if a.Part.Error.Message != "" {
			msg = a.Part.Error.Message
		}
		return v.endTurn(a.TurnId, "[error] "+msg, msg)
	}
	return ""
}

func (v *chatView) endTurn(turn, note, errText string) string {
	out := v.settle(turn, "", "withdrawn: the turn ended")
	if v.active == turn {
		v.active = ""
	}
	v.lastError = errText
	if note != "" {
		out += v.line(note)
	} else if !v.atLineEnd {
		out += v.line("")
	}
	return out
}

// lateAnswer reports whether input looks like an answer to a question that
// just closed (answered in another app, or withdrawn), so it is not sent to
// the agent as a message by mistake.
func (v *chatView) lateAnswer(input string, now time.Time) bool {
	if len(v.order) > 0 || v.closedAt.IsZero() || now.Sub(v.closedAt) > 15*time.Second {
		return false
	}
	_, err := strconv.Atoi(strings.TrimSpace(input))
	return err == nil
}

// answer turns the operator's reply into a confirmation for the oldest open
// question, or reports why it can't.
func (v *chatView) answer(input string) (*ahptypes.ChatToolCallConfirmedAction, error) {
	if len(v.order) == 0 {
		return nil, fmt.Errorf("no open question")
	}
	id := v.order[0]
	q := v.questions[id]
	n, err := strconv.Atoi(strings.TrimSpace(input))
	if err != nil || n < 1 || n > len(q.options) {
		return nil, fmt.Errorf("answer with a number from 1 to %d", len(q.options))
	}
	o := q.options[n-1]
	selected := o.Id
	confirm := &ahptypes.ChatToolCallConfirmedAction{Type: ahptypes.ActionTypeChatToolCallConfirmed, TurnId: q.turn, ToolCallId: id,
		Approved: o.Kind == ahptypes.ConfirmationOptionKindApprove, SelectedOptionId: &selected}
	// The hub wants a confirmed reason on an approval and a denial reason
	// (and no confirmed reason) on a denial; it refuses anything else.
	if confirm.Approved {
		reason := ahptypes.ToolCallConfirmationReasonUserAction
		confirm.Confirmed = &reason
	} else {
		reason := ahptypes.ToolCallCancellationReasonDenied
		confirm.Reason = &reason
	}
	return confirm, nil
}

// statusReport is the OSC 7501 sequence (herdr-core's Program Status
// Protocol): colon-separated key=value pairs, with msg and title in base64.
// app is an agent id, so it never contains a colon or a control character.
func statusReport(state, app, kind, msg, title string) string {
	fields := []string{"state=" + state, "app=" + app}
	if kind != "" {
		fields = append(fields, "kind="+kind)
	}
	if msg = oneLine(msg, 200); msg != "" {
		fields = append(fields, "msg="+base64.StdEncoding.EncodeToString([]byte(msg)))
	}
	if title = oneLine(title, 80); title != "" {
		fields = append(fields, "title="+base64.StdEncoding.EncodeToString([]byte(title)))
	}
	return "\x1b]7501;" + strings.Join(fields, ":") + "\x1b\\"
}

// oneLine keeps printable text only, on one line, at most max runes.
func oneLine(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return ' '
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0):
			return -1
		}
		return r
	}, strings.TrimSpace(s))
	if r := []rune(s); len(r) > max {
		s = string(r[:max-1]) + "…"
	}
	return s
}

// sessionLine is one row of the session picker.
type sessionLine struct {
	URI      string `json:"session"`
	Agent    string `json:"agent"`
	Title    string `json:"title"`
	Modified string `json:"modified"`
	Busy     bool   `json:"busy"`
}

// agentSessions keeps one agent's sessions (all when agent is empty),
// newest first.
func agentSessions(items []ahptypes.SessionSummary, agent string) []sessionLine {
	out := []sessionLine{}
	for _, s := range items {
		if agent != "" && s.Provider != agent {
			continue
		}
		out = append(out, sessionLine{URI: string(s.Resource), Agent: s.Provider, Title: s.Title, Modified: s.ModifiedAt,
			Busy: s.Status.Has(ahptypes.SessionStatusInProgress)})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Modified > out[j].Modified })
	return out
}

// pasteReader joins a bracketed paste (ESC[200~ … ESC[201~) that spans
// several lines into one message, so a pasted multi-line prompt, or a
// message herdr-core pastes in with `hc send`, becomes one prompt.
type pasteReader struct {
	next    func() (string, bool)
	pasting bool
	buf     []string
}

const (
	pasteStart = "\x1b[200~"
	pasteEnd   = "\x1b[201~"
)

func (p *pasteReader) read() (string, bool) {
	for {
		line, ok := p.next()
		if !ok {
			return "", false
		}
		if i := strings.Index(line, pasteStart); i >= 0 {
			p.pasting, p.buf = true, nil
			line = line[:i] + line[i+len(pasteStart):]
		}
		if !p.pasting {
			return line, true
		}
		if i := strings.Index(line, pasteEnd); i >= 0 {
			p.buf = append(p.buf, line[:i]+line[i+len(pasteEnd):])
			p.pasting = false
			return strings.Join(p.buf, "\n"), true
		}
		p.buf = append(p.buf, line)
	}
}
