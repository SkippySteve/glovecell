package sessionhub

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	acp "github.com/coder/acp-go-sdk"
	"github.com/microsoft/agent-host-protocol/clients/go/ahp"
	"github.com/microsoft/agent-host-protocol/clients/go/ahptypes"
	"github.com/microsoft/agent-host-protocol/clients/go/ahpws"
)

// Connector is what an operator app (T3 Code, Orca, Agent Orchestrator)
// starts as an ACP agent. It speaks ACP to the app on stdio and AHP to the
// cell's session hub, so every app sees the same live sessions of one
// allowlisted agent (Provider). It runs in the workspace container, next to
// the app; the hub's agents run in the launcher container and never see the
// token (docs/session-hub.md).
type Connector struct {
	URL      string // ws://127.0.0.1:8765/ahp
	Token    string
	Provider string // allowlisted hub agent id, e.g. cell-codex
	ClientID string

	hub  *ahp.Client
	conn *acp.AgentSideConnection

	mu       sync.Mutex
	sessions map[acp.SessionId]*hubSession
}

type hubSession struct {
	id   acp.SessionId // the hub's session URI
	chat string        // the session's default chat URI
	sub  *ahp.Subscription

	mu       sync.Mutex
	turn     string // turn this connector started and is waiting for
	done     chan acp.StopReason
	errText  string
	asked    map[string]bool // tool calls already put to the app
	ownTurns map[string]bool
	// pending permission questions, withdrawn once the hub has an answer
	// (from any app) or the tool call or turn ends
	pending map[string]pendingQuestion
}

type pendingQuestion struct {
	turn   string
	cancel context.CancelFunc
}

// settle withdraws open questions for a tool call (toolCallID) or for every
// tool call of a turn (toolCallID == "").
func (s *hubSession) settle(turnID, toolCallID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, q := range s.pending {
		if (toolCallID != "" && id == toolCallID) || (toolCallID == "" && q.turn == turnID) {
			q.cancel()
			delete(s.pending, id)
		}
	}
}

// RunConnector serves one app connection until stdin closes.
func RunConnector(ctx context.Context, hubURL, tokenFile, provider string, in io.Reader, out io.Writer) error {
	if !idPattern.MatchString(provider) {
		return fmt.Errorf("session-hub-acp: invalid --agent %q", provider)
	}
	raw, err := os.ReadFile(tokenFile)
	if err != nil {
		return fmt.Errorf("session-hub-acp: hub token unavailable (is this a session-hub cell?): %w", err)
	}
	token := strings.TrimSpace(string(raw))
	if !ValidToken(token) {
		return errors.New("session-hub-acp: invalid hub token")
	}
	if u, err := url.Parse(hubURL); err != nil || u.Scheme != "ws" || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost") {
		return errors.New("session-hub-acp: the hub URL must be ws:// on loopback")
	}
	c := &Connector{URL: hubURL, Token: token, Provider: provider, ClientID: "glovecell-acp-" + randomID(), sessions: map[acp.SessionId]*hubSession{}}
	c.conn = acp.NewAgentSideConnection(c, out, in)
	defer c.close()
	select {
	case <-c.conn.Done():
	case <-ctx.Done():
	}
	return nil
}

func randomID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (c *Connector) close() {
	if c.hub != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = c.hub.Shutdown(ctx)
	}
}

func (c *Connector) connect(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.hub != nil {
		return nil
	}
	header := http.Header{}
	header.Set("Authorization", "Bearer "+c.Token)
	transport, err := ahpws.Connect(ctx, c.URL, ahpws.DialOptions{HTTPHeader: header})
	if err != nil {
		return fmt.Errorf("session hub unreachable: %w", err)
	}
	client, err := ahp.Connect(ctx, transport, ahp.DefaultConfig())
	if err != nil {
		return err
	}
	if _, err := client.Initialize(ctx, c.ClientID, ahptypes.SupportedProtocolVersions(), nil); err != nil {
		_ = client.Shutdown(ctx)
		return fmt.Errorf("session hub refused the connection: %w", err)
	}
	c.hub = client
	return nil
}

// ---- ACP agent: setup -------------------------------------------------------

func (c *Connector) Initialize(ctx context.Context, p acp.InitializeRequest) (acp.InitializeResponse, error) {
	if err := c.connect(ctx); err != nil {
		return acp.InitializeResponse{}, err
	}
	title := "Glovecell session hub (" + c.Provider + ")"
	return acp.InitializeResponse{
		ProtocolVersion: acp.ProtocolVersionNumber,
		AgentInfo:       &acp.Implementation{Name: "glovecell-session-hub", Title: &title, Version: "1"},
		AuthMethods:     []acp.AuthMethod{},
		AgentCapabilities: acp.AgentCapabilities{
			LoadSession:         true,
			SessionCapabilities: acp.SessionCapabilities{List: &acp.SessionListCapabilities{}, Resume: &acp.SessionResumeCapabilities{}},
		},
	}, nil
}

func (c *Connector) Authenticate(context.Context, acp.AuthenticateRequest) (acp.AuthenticateResponse, error) {
	return acp.AuthenticateResponse{}, nil
}
func (c *Connector) Logout(context.Context, acp.LogoutRequest) (acp.LogoutResponse, error) {
	return acp.LogoutResponse{}, nil
}
func (c *Connector) SetSessionMode(context.Context, acp.SetSessionModeRequest) (acp.SetSessionModeResponse, error) {
	return acp.SetSessionModeResponse{}, acp.NewMethodNotFound("session/set_mode")
}
func (c *Connector) SetSessionConfigOption(context.Context, acp.SetSessionConfigOptionRequest) (acp.SetSessionConfigOptionResponse, error) {
	return acp.SetSessionConfigOptionResponse{}, acp.NewMethodNotFound("session/set_config_option")
}

// CloseSession detaches this app from the session; the session itself, and
// any other app on it, carry on.
func (c *Connector) CloseSession(ctx context.Context, p acp.CloseSessionRequest) (acp.CloseSessionResponse, error) {
	c.mu.Lock()
	s := c.sessions[p.SessionId]
	delete(c.sessions, p.SessionId)
	c.mu.Unlock()
	if s != nil && c.hub != nil {
		_ = c.hub.Unsubscribe(ctx, s.chat)
	}
	return acp.CloseSessionResponse{}, nil
}

// ---- sessions ---------------------------------------------------------------

func fileURI(dir string) string { return (&url.URL{Scheme: "file", Path: dir}).String() }

func pathOf(uri string) string {
	if u, err := url.Parse(uri); err == nil && u.Scheme == "file" {
		return u.Path
	}
	return ""
}

func (c *Connector) NewSession(ctx context.Context, p acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	if err := c.connect(ctx); err != nil {
		return acp.NewSessionResponse{}, err
	}
	channel := "ahp-session:/" + randomID() + randomID()
	provider := c.Provider
	params := ahptypes.CreateSessionParams{Channel: ahptypes.URI(channel), Provider: &provider}
	if p.Cwd != "" {
		params.WorkingDirectories = []ahptypes.URI{ahptypes.URI(fileURI(p.Cwd))}
	}
	if err := c.hub.Request(ctx, "createSession", params, nil); err != nil {
		return acp.NewSessionResponse{}, fmt.Errorf("session hub could not start %s: %w", c.Provider, err)
	}
	if _, err := c.attach(ctx, acp.SessionId(channel)); err != nil {
		return acp.NewSessionResponse{}, err
	}
	return acp.NewSessionResponse{SessionId: acp.SessionId(channel)}, nil
}

// ListSessions lists the hub's sessions of this connector's agent, whichever
// app started them, so an app can import a chat begun elsewhere.
func (c *Connector) ListSessions(ctx context.Context, p acp.ListSessionsRequest) (acp.ListSessionsResponse, error) {
	if err := c.connect(ctx); err != nil {
		return acp.ListSessionsResponse{}, err
	}
	params := ahptypes.ListSessionsParams{Channel: "ahp-root://", Cursor: p.Cursor}
	var res ahptypes.ListSessionsResult
	if err := c.hub.Request(ctx, "listSessions", params, &res); err != nil {
		return acp.ListSessionsResponse{}, err
	}
	out := acp.ListSessionsResponse{Sessions: []acp.SessionInfo{}, NextCursor: res.NextCursor}
	for _, item := range res.Items {
		if item.Provider != c.Provider {
			continue
		}
		cwd := ""
		if len(item.WorkingDirectories) > 0 {
			cwd = pathOf(string(item.WorkingDirectories[0]))
		}
		if p.Cwd != nil && *p.Cwd != "" && cwd != "" && cwd != *p.Cwd {
			continue
		}
		title, updated := item.Title, item.ModifiedAt
		out.Sessions = append(out.Sessions, acp.SessionInfo{SessionId: acp.SessionId(item.Resource), Cwd: cwd, Title: &title, UpdatedAt: &updated})
	}
	return out, nil
}

// LoadSession attaches and replays the chat so far.
func (c *Connector) LoadSession(ctx context.Context, p acp.LoadSessionRequest) (acp.LoadSessionResponse, error) {
	if err := c.connect(ctx); err != nil {
		return acp.LoadSessionResponse{}, err
	}
	turns, err := c.attach(ctx, p.SessionId)
	if err != nil {
		return acp.LoadSessionResponse{}, err
	}
	for _, t := range turns {
		for _, u := range replayTurn(t) {
			if err := c.conn.SessionUpdate(ctx, acp.SessionNotification{SessionId: p.SessionId, Update: u}); err != nil {
				return acp.LoadSessionResponse{}, err
			}
		}
	}
	return acp.LoadSessionResponse{}, nil
}

// ResumeSession attaches without replay; new activity streams from here on.
func (c *Connector) ResumeSession(ctx context.Context, p acp.ResumeSessionRequest) (acp.ResumeSessionResponse, error) {
	if err := c.connect(ctx); err != nil {
		return acp.ResumeSessionResponse{}, err
	}
	if _, err := c.attach(ctx, p.SessionId); err != nil {
		return acp.ResumeSessionResponse{}, err
	}
	return acp.ResumeSessionResponse{}, nil
}

// attach subscribes to a session's default chat and starts forwarding its
// actions to the app. It refuses a session of another agent.
func (c *Connector) attach(ctx context.Context, id acp.SessionId) ([]ahptypes.Turn, error) {
	if !strings.HasPrefix(string(id), "ahp-session:/") {
		return nil, fmt.Errorf("unknown session %q", id)
	}
	snap, _, err := c.hub.Subscribe(ctx, string(id))
	if err != nil {
		return nil, fmt.Errorf("session %s: %w", id, err)
	}
	session := snap.Snapshot.State.Session
	if session == nil || session.DefaultChat == nil {
		return nil, fmt.Errorf("session %s has no chat", id)
	}
	// Only the snapshot is needed; the chat subscription below carries the
	// activity. An unread subscription would fill and stall the client.
	_ = c.hub.Unsubscribe(ctx, string(id))
	if session.Provider != c.Provider {
		return nil, fmt.Errorf("session %s belongs to agent %q, not %q", id, session.Provider, c.Provider)
	}
	chat := string(*session.DefaultChat)
	chatSnap, sub, err := c.hub.Subscribe(ctx, chat)
	if err != nil {
		return nil, err
	}
	var turns []ahptypes.Turn
	if state := chatSnap.Snapshot.State.Chat; state != nil {
		turns = state.Turns
	}
	s := &hubSession{id: id, chat: chat, sub: sub, asked: map[string]bool{}, ownTurns: map[string]bool{}, pending: map[string]pendingQuestion{}}
	c.mu.Lock()
	c.sessions[id] = s
	c.mu.Unlock()
	go c.pump(s)
	return turns, nil
}

func (c *Connector) session(id acp.SessionId) (*hubSession, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.sessions[id]
	if s == nil {
		return nil, fmt.Errorf("session %s is not open in this app; load or resume it first", id)
	}
	return s, nil
}

// ---- turns ------------------------------------------------------------------

func promptText(blocks []acp.ContentBlock) string {
	var b strings.Builder
	for _, block := range blocks {
		switch {
		case block.Text != nil:
			b.WriteString(block.Text.Text)
		case block.ResourceLink != nil:
			b.WriteString("\n" + block.ResourceLink.Uri + "\n")
		}
	}
	return b.String()
}

func (c *Connector) Prompt(ctx context.Context, p acp.PromptRequest) (acp.PromptResponse, error) {
	s, err := c.session(p.SessionId)
	if err != nil {
		return acp.PromptResponse{}, err
	}
	text := promptText(p.Prompt)
	if strings.TrimSpace(text) == "" {
		return acp.PromptResponse{}, acp.NewInvalidParams("empty prompt")
	}
	turnID := "turn-" + randomID()
	done := make(chan acp.StopReason, 1)
	s.mu.Lock()
	if s.turn != "" {
		s.mu.Unlock()
		return acp.PromptResponse{}, acp.NewInvalidRequest("a turn is already running in this session")
	}
	s.turn, s.done, s.errText = turnID, done, ""
	s.ownTurns[turnID] = true
	s.mu.Unlock()
	action := &ahptypes.ChatTurnStartedAction{Type: ahptypes.ActionTypeChatTurnStarted, TurnId: turnID,
		StartedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Message:   ahptypes.Message{Text: text, Origin: ahptypes.MessageOrigin{Kind: ahptypes.MessageKindUser}}}
	if _, err := c.hub.Dispatch(ctx, s.chat, ahptypes.StateAction{Value: action}); err != nil {
		s.clearTurn(turnID)
		return acp.PromptResponse{}, err
	}
	select {
	case reason := <-done:
		s.mu.Lock()
		msg := s.errText
		s.mu.Unlock()
		if msg != "" {
			return acp.PromptResponse{}, errors.New(msg)
		}
		return acp.PromptResponse{StopReason: reason}, nil
	case <-ctx.Done():
		c.cancelTurn(s, turnID)
		return acp.PromptResponse{StopReason: acp.StopReasonCancelled}, nil
	case <-c.hub.Done():
		s.clearTurn(turnID)
		return acp.PromptResponse{}, errors.New("session hub connection lost")
	}
}

func (s *hubSession) clearTurn(turnID string) {
	s.mu.Lock()
	if s.turn == turnID {
		s.turn, s.done = "", nil
	}
	s.mu.Unlock()
}

func (c *Connector) Cancel(ctx context.Context, p acp.CancelNotification) error {
	s, err := c.session(p.SessionId)
	if err != nil {
		return nil
	}
	s.mu.Lock()
	turnID := s.turn
	s.mu.Unlock()
	if turnID != "" {
		c.cancelTurn(s, turnID)
	}
	return nil
}

func (c *Connector) cancelTurn(s *hubSession, turnID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = c.hub.Dispatch(ctx, s.chat, ahptypes.StateAction{Value: &ahptypes.ChatTurnCancelledAction{Type: ahptypes.ActionTypeChatTurnCancelled, TurnId: turnID}})
}

// finish ends this connector's own turn with reason.
func (s *hubSession) finish(turnID string, reason acp.StopReason, errText string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.turn != turnID || s.done == nil {
		return
	}
	s.errText = errText
	s.done <- reason
	s.turn, s.done = "", nil
}

// ---- hub -> app -------------------------------------------------------------

// pump forwards the chat's actions to the app, whichever app caused them.
func (c *Connector) pump(s *hubSession) {
	ctx := context.Background()
	for ev := range s.sub.Events() {
		a, ok := ev.(ahp.SubscriptionEventAction)
		if !ok || a.Envelope.RejectionReason != nil {
			continue
		}
		s.mu.Lock()
		own := s.ownTurns
		s.mu.Unlock()
		updates, outcome := translate(a.Envelope.Action.Value, own)
		for _, u := range updates {
			if err := c.conn.SessionUpdate(ctx, acp.SessionNotification{SessionId: s.id, Update: u}); err != nil {
				log.Printf("session-hub-acp: update: %v", err)
			}
		}
		switch act := a.Envelope.Action.Value.(type) {
		case *ahptypes.ChatToolCallReadyAction:
			if needsPermission(act) {
				s.mu.Lock()
				first := !s.asked[act.ToolCallId]
				s.asked[act.ToolCallId] = true
				var qctx context.Context
				if first {
					var cancel context.CancelFunc
					qctx, cancel = context.WithCancel(context.Background())
					s.pending[act.ToolCallId] = pendingQuestion{turn: act.TurnId, cancel: cancel}
				}
				s.mu.Unlock()
				if first {
					go c.askPermission(qctx, s, act)
				}
			}
		case *ahptypes.ChatToolCallConfirmedAction:
			s.settle(act.TurnId, act.ToolCallId)
		case *ahptypes.ChatToolCallCompleteAction:
			s.settle(act.TurnId, act.ToolCallId)
		case *ahptypes.ChatTurnCompleteAction:
			s.settle(act.TurnId, "")
		case *ahptypes.ChatTurnCancelledAction:
			s.settle(act.TurnId, "")
		case *ahptypes.ChatErrorAction:
			s.settle(act.TurnId, "")
		}
		if outcome != nil {
			s.finish(outcome.turnID, outcome.reason, outcome.err)
		}
	}
}

type turnOutcome struct {
	turnID string
	reason acp.StopReason
	err    string
}

func needsPermission(a *ahptypes.ChatToolCallReadyAction) bool {
	return a.Confirmed == nil && len(a.Options) > 0
}

func text(s ahptypes.StringOrMarkdown) string {
	if s.Markdown != nil {
		return *s.Markdown
	}
	if s.Plain != nil {
		return *s.Plain
	}
	return ""
}

// translate maps one hub action to ACP session updates, and reports the end
// of a turn. Turns started by other apps are shown as the user's message, so
// every app follows the chat live.
func translate(v any, ownTurns map[string]bool) ([]acp.SessionUpdate, *turnOutcome) {
	switch a := v.(type) {
	case *ahptypes.ChatTurnStartedAction:
		if ownTurns[a.TurnId] {
			return nil, nil
		}
		return []acp.SessionUpdate{acp.UpdateUserMessageText(a.Message.Text)}, nil
	case *ahptypes.ChatResponsePartAction:
		if md, ok := a.Part.Value.(*ahptypes.MarkdownResponsePart); ok && md.Content != "" {
			return []acp.SessionUpdate{acp.UpdateAgentMessageText(md.Content)}, nil
		}
		if r, ok := a.Part.Value.(*ahptypes.ReasoningResponsePart); ok && r.Content != "" {
			return []acp.SessionUpdate{acp.UpdateAgentThoughtText(r.Content)}, nil
		}
	case *ahptypes.ChatDeltaAction:
		return []acp.SessionUpdate{acp.UpdateAgentMessageText(a.Content)}, nil
	case *ahptypes.ChatReasoningAction:
		return []acp.SessionUpdate{acp.UpdateAgentThoughtText(a.Content)}, nil
	case *ahptypes.ChatToolCallStartAction:
		return []acp.SessionUpdate{acp.StartToolCall(acp.ToolCallId(a.ToolCallId), a.DisplayName, acp.WithStartStatus(acp.ToolCallStatusPending))}, nil
	case *ahptypes.ChatToolCallReadyAction:
		return []acp.SessionUpdate{acp.UpdateToolCall(acp.ToolCallId(a.ToolCallId), acp.WithUpdateStatus(acp.ToolCallStatusInProgress))}, nil
	case *ahptypes.ChatToolCallContentChangedAction:
		if t := toolText(a.Content); t != "" {
			return []acp.SessionUpdate{acp.UpdateToolCall(acp.ToolCallId(a.ToolCallId), acp.WithUpdateContent([]acp.ToolCallContent{acp.ToolContent(acp.TextBlock(t))}))}, nil
		}
	case *ahptypes.ChatToolCallCompleteAction:
		status := acp.ToolCallStatusCompleted
		if !a.Result.Success {
			status = acp.ToolCallStatusFailed
		}
		opts := []acp.ToolCallUpdateOpt{acp.WithUpdateStatus(status)}
		if t := toolText(a.Result.Content); t != "" {
			opts = append(opts, acp.WithUpdateContent([]acp.ToolCallContent{acp.ToolContent(acp.TextBlock(t))}))
		}
		return []acp.SessionUpdate{acp.UpdateToolCall(acp.ToolCallId(a.ToolCallId), opts...)}, nil
	case *ahptypes.ChatTurnCompleteAction:
		return nil, &turnOutcome{turnID: a.TurnId, reason: acp.StopReasonEndTurn}
	case *ahptypes.ChatTurnCancelledAction:
		return nil, &turnOutcome{turnID: a.TurnId, reason: acp.StopReasonCancelled}
	case *ahptypes.ChatErrorAction:
		msg := "the agent stopped with an error"
		if a.Part.Error.Message != "" {
			msg = a.Part.Error.Message
		}
		return nil, &turnOutcome{turnID: a.TurnId, reason: acp.StopReasonEndTurn, err: msg}
	}
	return nil, nil
}

func toolText(content []ahptypes.ToolResultContent) string {
	var b strings.Builder
	for _, c := range content {
		if t, ok := c.Value.(*ahptypes.ToolResultTextContent); ok {
			b.WriteString(t.Text)
		}
	}
	return b.String()
}

// replayTurn renders a past turn for session/load.
func replayTurn(t ahptypes.Turn) []acp.SessionUpdate {
	out := []acp.SessionUpdate{}
	if t.Message.Text != "" {
		out = append(out, acp.UpdateUserMessageText(t.Message.Text))
	}
	for _, part := range t.ResponseParts {
		switch p := part.Value.(type) {
		case *ahptypes.MarkdownResponsePart:
			if p.Content != "" {
				out = append(out, acp.UpdateAgentMessageText(p.Content))
			}
		case *ahptypes.ReasoningResponsePart:
			if p.Content != "" {
				out = append(out, acp.UpdateAgentThoughtText(p.Content))
			}
		}
	}
	return out
}

// askPermission puts a tool call's confirmation to the app and dispatches the
// answer. Every attached app is asked; the hub keeps the first answer and
// rejects the rest (decision SS3 A). ctx is cancelled, withdrawing the
// question from this app, once the hub has settled it.
func (c *Connector) askPermission(ctx context.Context, s *hubSession, a *ahptypes.ChatToolCallReadyAction) {
	title := text(a.InvocationMessage)
	if a.ConfirmationTitle != nil {
		title = text(*a.ConfirmationTitle)
	}
	options := make([]acp.PermissionOption, 0, len(a.Options))
	for _, o := range a.Options {
		kind := acp.PermissionOptionKindRejectOnce
		if o.Kind == ahptypes.ConfirmationOptionKindApprove {
			kind = acp.PermissionOptionKindAllowOnce
		}
		options = append(options, acp.PermissionOption{OptionId: acp.PermissionOptionId(o.Id), Name: o.Label, Kind: kind})
	}
	res, err := c.conn.RequestPermission(ctx, acp.RequestPermissionRequest{SessionId: s.id, Options: options,
		ToolCall: acp.ToolCallUpdate{ToolCallId: acp.ToolCallId(a.ToolCallId), Title: &title}})
	if err != nil || ctx.Err() != nil {
		return
	}
	confirm := confirmation(a, res.Outcome)
	if confirm == nil {
		return
	}
	_, _ = c.hub.Dispatch(context.Background(), s.chat, ahptypes.StateAction{Value: confirm})
}

// confirmation turns an app's answer into the hub action, or nil when the app
// did not answer. A "cancelled" outcome is not a refusal: ACP clients send it
// when they cancel their own turn, and some (Orca) send it for every question
// from a turn they did not start. Dispatching it as a refusal would let one app
// refuse every other app's questions ("first answer wins"). An option id the
// question did not offer is also not an answer.
func confirmation(a *ahptypes.ChatToolCallReadyAction, outcome acp.RequestPermissionOutcome) *ahptypes.ChatToolCallConfirmedAction {
	if outcome.Selected == nil {
		return nil
	}
	id := string(outcome.Selected.OptionId)
	for _, o := range a.Options {
		if o.Id == id {
			reason := ahptypes.ToolCallConfirmationReasonUserAction
			return &ahptypes.ChatToolCallConfirmedAction{Type: ahptypes.ActionTypeChatToolCallConfirmed, TurnId: a.TurnId, ToolCallId: a.ToolCallId,
				Approved: o.Kind == ahptypes.ConfirmationOptionKindApprove, SelectedOptionId: &id, Confirmed: &reason}
		}
	}
	return nil
}
