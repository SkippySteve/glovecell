package sessionhub

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/microsoft/agent-host-protocol/clients/go/ahp"
	"github.com/microsoft/agent-host-protocol/clients/go/ahptypes"
	"github.com/microsoft/agent-host-protocol/clients/go/ahpws"
)

// session-hub-chat is a small terminal chat client for the cell's session
// hub, made for Herdr: each hub chat runs in a herdr-core terminal, which the
// Herdr apps on a phone or PC show like any other agent (docs/session-hub.md,
// "Herdr"). It speaks AHP to the hub directly, so it sees every app's turns
// start and end and can report idle, working and blocked to herdr-core
// through OSC 7501.

// HerdrSessionLabel is the herdr-core terminal label that names the hub
// session a terminal shows.
const HerdrSessionLabel = "glovecell.hub-session"

// ChatOptions configure session-hub-chat.
type ChatOptions struct {
	URL       string // ws://127.0.0.1:8765/ahp
	TokenFile string
	// AgentsFile lists the agents to offer when Agent is empty
	// (ClientAgentsFile).
	AgentsFile string
	Agent      string // allowlisted agent id; empty: ask, or the only one
	Session    string // hub session URI to open
	New        bool   // start a new session instead of choosing one
	List       bool   // print the sessions as JSON lines and exit
	Cwd        string // working directory for a new session
	// Report sends OSC 7501 status reports; set inside a herdr-core
	// terminal (HC_TERMINAL_ID).
	Report bool
}

// hubAccess validates the hub URL and reads the cell's token, with the same
// rules as the ACP connector: loopback ws:// only, and a well-formed token.
func hubAccess(hubURL, tokenFile string) (string, error) {
	if err := loopbackHub(hubURL); err != nil {
		return "", err
	}
	raw, err := os.ReadFile(tokenFile)
	if err != nil {
		return "", fmt.Errorf("hub token unavailable (is this a session-hub cell?): %w", err)
	}
	token := strings.TrimSpace(string(raw))
	if !ValidToken(token) {
		return "", errors.New("invalid hub token")
	}
	return token, nil
}

func loopbackHub(hubURL string) error {
	if u, err := url.Parse(hubURL); err != nil || u.Scheme != "ws" || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost") {
		return errors.New("the hub URL must be ws:// on loopback")
	}
	return nil
}

// dialHub connects and initializes an AHP client, subscribed to nothing.
func dialHub(ctx context.Context, hubURL, token, clientID string) (*ahp.Client, *ahptypes.RootState, error) {
	header := http.Header{}
	header.Set("Authorization", "Bearer "+token)
	transport, err := ahpws.Connect(ctx, hubURL, ahpws.DialOptions{HTTPHeader: header})
	if err != nil {
		return nil, nil, fmt.Errorf("session hub unreachable: %w", err)
	}
	client, err := ahp.Connect(ctx, transport, ahp.DefaultConfig())
	if err != nil {
		return nil, nil, err
	}
	res, err := client.Initialize(ctx, clientID, ahptypes.SupportedProtocolVersions(), []string{"ahp-root://"})
	if err != nil {
		_ = client.Shutdown(ctx)
		return nil, nil, fmt.Errorf("session hub refused the connection: %w", err)
	}
	// Only the root snapshot is needed (the agent list); an unread
	// subscription would fill and stall the client.
	_ = client.Unsubscribe(ctx, "ahp-root://")
	var root *ahptypes.RootState
	for _, s := range res.Snapshots {
		if s.State.Root != nil {
			root = s.State.Root
		}
	}
	if root == nil {
		root = &ahptypes.RootState{}
	}
	return client, root, nil
}

func listHubSessions(ctx context.Context, hub *ahp.Client) ([]ahptypes.SessionSummary, error) {
	var all []ahptypes.SessionSummary
	var cursor *string
	for range 50 {
		var res ahptypes.ListSessionsResult
		if err := hub.Request(ctx, "listSessions", ahptypes.ListSessionsParams{Channel: "ahp-root://", Cursor: cursor}, &res); err != nil {
			return nil, err
		}
		all = append(all, res.Items...)
		if res.NextCursor == nil || *res.NextCursor == "" {
			break
		}
		cursor = res.NextCursor
	}
	return all, nil
}

// chat is one running session-hub-chat.
type chat struct {
	opts     ChatOptions
	hub      *ahp.Client
	clientID string
	in       *pasteReader
	stdin    io.Reader
	out      io.Writer

	mu       sync.Mutex
	view     *chatView
	session  string
	chatURI  string
	agent    string
	title    string
	reported string
}

// RunChat runs the terminal chat until the operator quits or input ends.
func RunChat(ctx context.Context, opts ChatOptions, in io.Reader, out io.Writer) error {
	if opts.Agent != "" && !idPattern.MatchString(opts.Agent) {
		return fmt.Errorf("session-hub-chat: invalid --agent %q", opts.Agent)
	}
	if opts.Session != "" && !strings.HasPrefix(opts.Session, "ahp-session:/") {
		return fmt.Errorf("session-hub-chat: unknown session %q", opts.Session)
	}
	if opts.Session != "" && opts.New {
		return errors.New("session-hub-chat: --session and --new exclude each other")
	}
	token, err := hubAccess(opts.URL, opts.TokenFile)
	if err != nil {
		return fmt.Errorf("session-hub-chat: %w", err)
	}
	clientID := "glovecell-herdr-" + randomID()
	hub, root, err := dialHub(ctx, opts.URL, token, clientID)
	if err != nil {
		return err
	}
	defer func() {
		sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = hub.Shutdown(sctx)
	}()
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	c := &chat{opts: opts, hub: hub, clientID: clientID, stdin: in, out: out, view: newChatView(), agent: opts.Agent,
		in: &pasteReader{next: func() (string, bool) {
			if scanner.Scan() {
				return scanner.Text(), true
			}
			return "", false
		}}}
	if opts.List {
		items, err := listHubSessions(ctx, hub)
		if err != nil {
			return err
		}
		enc := json.NewEncoder(out)
		for _, s := range agentSessions(items, opts.Agent) {
			if err := enc.Encode(s); err != nil {
				return err
			}
		}
		return nil
	}
	if err := c.choose(ctx, root); err != nil {
		return err
	}
	return c.run(ctx)
}

func (c *chat) print(s string) {
	if s == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	_, _ = io.WriteString(c.out, s)
}

// choose settles the agent and the session: the given one, a new one, or
// one the operator picks from the hub's list.
func (c *chat) choose(ctx context.Context, root *ahptypes.RootState) error {
	if c.opts.Session != "" {
		c.session = c.opts.Session
		return nil
	}
	if c.agent == "" {
		// The allowlist mounted beside the token says which agents to
		// offer; the hub's own list is the fallback.
		agents, err := ReadClientAgents(c.opts.AgentsFile)
		if err != nil || len(agents) == 0 {
			agents = root.Agents
		}
		switch len(agents) {
		case 0:
			return errors.New("session-hub-chat: the hub lists no agents")
		case 1:
			c.agent = agents[0].Provider
		default:
			c.print("Agents in this cell's session hub:\n")
			for i, a := range agents {
				c.print(fmt.Sprintf("  %d) %s (%s)\n", i+1, a.DisplayName, a.Provider))
			}
			n, err := c.pick(len(agents))
			if err != nil {
				return err
			}
			c.agent = agents[n-1].Provider
		}
	}
	if c.opts.New {
		return c.create(ctx)
	}
	items, err := listHubSessions(ctx, c.hub)
	if err != nil {
		return err
	}
	sessions := agentSessions(items, c.agent)
	if len(sessions) == 0 {
		return c.create(ctx)
	}
	c.print(fmt.Sprintf("Chats with %s in this cell, from every app:\n", c.agent))
	for i, s := range sessions {
		busy := ""
		if s.Busy {
			busy = " (working)"
		}
		title := s.Title
		if title == "" {
			title = "(untitled)"
		}
		c.print(fmt.Sprintf("  %d) %s%s\n", i+1, oneLine(title, 80), busy))
	}
	c.print(fmt.Sprintf("  %d) a new chat\n", len(sessions)+1))
	n, err := c.pick(len(sessions) + 1)
	if err != nil {
		return err
	}
	if n == len(sessions)+1 {
		return c.create(ctx)
	}
	c.session = sessions[n-1].URI
	return nil
}

func (c *chat) pick(max int) (int, error) {
	for {
		c.print(fmt.Sprintf("Choose 1-%d: ", max))
		line, ok := c.in.read()
		if !ok {
			return 0, io.EOF
		}
		var n int
		if _, err := fmt.Sscan(strings.TrimSpace(line), &n); err == nil && n >= 1 && n <= max {
			return n, nil
		}
	}
}

func (c *chat) create(ctx context.Context) error {
	channel := "ahp-session:/" + randomID() + randomID()
	provider := c.agent
	params := ahptypes.CreateSessionParams{Channel: ahptypes.URI(channel), Provider: &provider}
	if c.opts.Cwd != "" {
		params.WorkingDirectories = []ahptypes.URI{ahptypes.URI(fileURI(c.opts.Cwd))}
	}
	if err := c.hub.Request(ctx, "createSession", params, nil); err != nil {
		return fmt.Errorf("session hub could not start %s: %w", c.agent, err)
	}
	c.session = channel
	return nil
}

// attach subscribes to the session's chat, prints it so far and returns the
// live subscription.
func (c *chat) attach(ctx context.Context) (*ahp.Subscription, error) {
	snap, _, err := c.hub.Subscribe(ctx, c.session)
	if err != nil {
		return nil, fmt.Errorf("session %s: %w", c.session, err)
	}
	_ = c.hub.Unsubscribe(ctx, c.session)
	session := snap.Snapshot.State.Session
	if session == nil || session.DefaultChat == nil {
		return nil, fmt.Errorf("session %s has no chat", c.session)
	}
	if c.agent != "" && session.Provider != c.agent {
		return nil, fmt.Errorf("session %s belongs to agent %q, not %q", c.session, session.Provider, c.agent)
	}
	c.agent, c.title = session.Provider, session.Title
	c.chatURI = string(*session.DefaultChat)
	chatSnap, sub, err := c.hub.Subscribe(ctx, c.chatURI)
	if err != nil {
		return nil, err
	}
	title := c.title
	if title == "" {
		title = "new chat"
	}
	c.print(fmt.Sprintf("— %s · %s (session hub; shared with the other apps). /help for commands.\n", c.agent, oneLine(title, 80)))
	if state := chatSnap.Snapshot.State.Chat; state != nil {
		c.mu.Lock()
		history := c.view.load(state)
		c.mu.Unlock()
		c.print(history)
	}
	return sub, nil
}

func (c *chat) run(ctx context.Context) error {
	sub, err := c.attach(ctx)
	if err != nil {
		return err
	}
	c.labelTerminal()
	if c.opts.Report {
		// Bracketed paste: a multi-line paste (or `hc send`) arrives as one message.
		if f, ok := c.stdin.(*os.File); ok {
			defer quietControlEcho(f)()
		}
		c.print("\x1b[?2004h")
		defer c.print("\x1b[?2004l")
	}
	c.report()
	go c.pump(sub)
	lines := make(chan string)
	go func() {
		defer close(lines)
		for {
			line, ok := c.in.read()
			if !ok {
				return
			}
			lines <- line
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-c.hub.Done():
			return errors.New("session hub connection lost")
		case line, ok := <-lines:
			if !ok {
				return nil
			}
			if quit := c.handle(ctx, line); quit {
				return nil
			}
		}
	}
}

// handle acts on one line of input; it reports whether to quit.
func (c *chat) handle(ctx context.Context, line string) bool {
	trimmed := strings.TrimSpace(line)
	c.mu.Lock()
	asking := len(c.view.order) > 0
	active := c.view.active
	c.mu.Unlock()
	switch {
	case trimmed == "":
		return false
	case trimmed == "/quit" || trimmed == "/exit":
		return true
	case trimmed == "/help":
		c.print("Type a message and press Enter to send it. While a question is open, answer with its number.\n" +
			"/cancel stops the running turn (whichever app started it). /quit leaves; the chat carries on in the hub.\n")
	case trimmed == "/cancel":
		if active == "" {
			c.print("Nothing is running.\n")
			return false
		}
		_, _ = c.hub.Dispatch(ctx, c.chatURI, ahptypes.StateAction{Value: &ahptypes.ChatTurnCancelledAction{Type: ahptypes.ActionTypeChatTurnCancelled, TurnId: active}})
	case asking:
		c.mu.Lock()
		confirm, err := c.view.answer(trimmed)
		c.mu.Unlock()
		if err != nil {
			c.print(err.Error() + "\n")
			return false
		}
		// The hub keeps the first answer from any app; the outcome arrives
		// as an action and closes the question here.
		if _, err := c.hub.Dispatch(ctx, c.chatURI, ahptypes.StateAction{Value: confirm}); err != nil {
			c.print("could not send the answer: " + err.Error() + "\n")
		}
	case c.lateAnswer(trimmed):
		c.print("That question was already settled (see above); nothing was sent. Type it again to send it as a message.\n")
		c.mu.Lock()
		c.view.closedAt = time.Time{}
		c.mu.Unlock()
	case active != "":
		c.print("A turn is running; wait for it, or /cancel it.\n")
	default:
		turnID := "turn-" + randomID()
		c.mu.Lock()
		c.view.ownTurns[turnID] = true
		c.view.atLineEnd = true
		c.mu.Unlock()
		action := &ahptypes.ChatTurnStartedAction{Type: ahptypes.ActionTypeChatTurnStarted, TurnId: turnID,
			StartedAt: time.Now().UTC().Format(time.RFC3339Nano),
			Message:   ahptypes.Message{Text: line, Origin: ahptypes.MessageOrigin{Kind: ahptypes.MessageKindUser}}}
		if _, err := c.hub.Dispatch(ctx, c.chatURI, ahptypes.StateAction{Value: action}); err != nil {
			c.print("could not send: " + err.Error() + "\n")
		}
	}
	return false
}

func (c *chat) lateAnswer(input string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.view.lateAnswer(input, time.Now())
}

// pump prints the chat's actions, whichever app caused them.
func (c *chat) pump(sub *ahp.Subscription) {
	for ev := range sub.Events() {
		a, ok := ev.(ahp.SubscriptionEventAction)
		if !ok {
			continue
		}
		if r := a.Envelope.RejectionReason; r != nil {
			// Say why the hub refused this terminal's own action, for
			// example an answer that came after another app's.
			if o := a.Envelope.Origin; o != nil && o.ClientId == c.clientID {
				c.mu.Lock()
				s := c.view.line("  (the hub refused that: " + oneLine(*r, 200) + ")")
				c.mu.Unlock()
				c.print(s)
			}
			continue
		}
		c.mu.Lock()
		s := c.view.apply(a.Envelope.Action.Value)
		c.mu.Unlock()
		c.print(s)
		c.report()
	}
}

// report sends the current status to herdr-core when it changed.
func (c *chat) report() {
	if !c.opts.Report {
		return
	}
	c.mu.Lock()
	state, kind, msg := c.view.status()
	seq := statusReport(state, c.agent, kind, msg, c.title)
	changed := seq != c.reported
	c.reported = seq
	c.mu.Unlock()
	if changed {
		c.print(seq)
	}
}

// labelTerminal names the session on this herdr-core terminal, so the pane
// opener (session-hub-herdr) knows the session already has a terminal.
func (c *chat) labelTerminal() {
	id := os.Getenv("HC_TERMINAL_ID")
	if !c.opts.Report || id == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = exec.CommandContext(ctx, "hc", "label", id, HerdrSessionLabel+"="+c.session).Run()
}
