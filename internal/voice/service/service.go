// Package service is glovecell-voice's task flow. A voice note or text from
// Steve in a task room is transcribed, echoed back as text so he sees what
// was understood, sent to the room's (or thread's, or spoken) agent target
// and tracked; when it finishes, the result is read back as a voice note
// plus text in the same thread. Messages in the Approvals room go to the
// approval gate instead. Agents reach Steve through Notify.
package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/approvalgate"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/voice/matrix"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/voice/speech"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/voice/store"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/voice/targets"
)

// Room is one configured Matrix room.
type Room struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Target is the default agent target for new threads.
	Target string `json:"target"`
	// Repo is the default OWNER/NAME for tasks from this room.
	Repo string `json:"repo,omitempty"`
	// Approvals marks the Approvals room (exactly one).
	Approvals bool `json:"approvals,omitempty"`
	// Inbox marks the room agents' notifications go to by default.
	Inbox bool `json:"inbox,omitempty"`
}

// Summarizer optionally shortens a result before it is spoken. Off unless
// configured; the target's own text is used otherwise.
type Summarizer interface {
	Summarize(ctx context.Context, text string) (string, error)
}

// Config configures the service.
type Config struct {
	Steve string
	Rooms []Room
	// PollInterval is how often running tasks are checked (30s).
	PollInterval time.Duration
	// RequireEncryption drops unencrypted messages everywhere (true in
	// production; the gate requires it regardless).
	RequireEncryption bool
	// MaxSpoken caps the characters read aloud (1500).
	MaxSpoken int
}

// Service wires Matrix, speech, targets, the store and the gate.
type Service struct {
	cfg        Config
	store      store.Store
	stt        speech.Transcriber
	tts        speech.Synthesizer
	mx         matrix.Sender
	targets    *targets.Registry
	gate       *approvalgate.Gate
	summarizer Summarizer
	rooms      map[string]Room
	inbox      Room
	approvals  Room
	Now        func() time.Time
	// OnDecision observes gate decisions and OnTask finished tasks
	// (metrics).
	OnDecision func(approvalgate.Decision)
	OnTask     func(target, state string)
	wake       chan struct{}
	mu         sync.Mutex // one poll at a time
	deliverMu  sync.Mutex // one delivery at a time, so none is posted twice
}

// New validates the room configuration against the registered targets.
func New(cfg Config, s store.Store, stt speech.Transcriber, tts speech.Synthesizer, mx matrix.Sender, reg *targets.Registry, sum Summarizer) (*Service, error) {
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 30 * time.Second
	}
	if cfg.MaxSpoken <= 0 {
		cfg.MaxSpoken = 1500
	}
	svc := &Service{cfg: cfg, store: s, stt: stt, tts: tts, mx: mx, targets: reg, summarizer: sum, rooms: map[string]Room{}, Now: time.Now, wake: make(chan struct{}, 1)}
	for _, r := range cfg.Rooms {
		if r.ID == "" {
			return nil, errors.New("room without an ID")
		}
		if _, dup := svc.rooms[r.ID]; dup {
			return nil, fmt.Errorf("room %s listed twice", r.ID)
		}
		if r.Approvals {
			if svc.approvals.ID != "" {
				return nil, errors.New("only one Approvals room")
			}
			svc.approvals = r
		} else if _, ok := reg.Get(r.Target); !ok {
			return nil, fmt.Errorf("room %s: unknown target %q", orName(r), r.Target)
		}
		if r.Repo != "" && !approvalgate.ValidRepo(r.Repo) {
			return nil, fmt.Errorf("room %s: repository must be OWNER/NAME", orName(r))
		}
		if r.Inbox {
			svc.inbox = r
		}
		svc.rooms[r.ID] = r
	}
	if svc.inbox.ID == "" {
		for _, r := range cfg.Rooms {
			if !r.Approvals {
				svc.inbox = r
				break
			}
		}
	}
	if svc.inbox.ID == "" {
		return nil, errors.New("configure at least one task room")
	}
	return svc, nil
}

func orName(r Room) string {
	if r.Name != "" {
		return r.Name
	}
	return r.ID
}

// SetGate attaches the approval gate (it needs the service as its Out).
func (s *Service) SetGate(g *approvalgate.Gate) { s.gate = g }

// RoomIDs lists every configured room.
func (s *Service) RoomIDs() []string {
	var out []string
	for _, r := range s.cfg.Rooms {
		out = append(out, r.ID)
	}
	return out
}

// ApprovalsRoom returns the Approvals room ID ("" when none).
func (s *Service) ApprovalsRoom() string { return s.approvals.ID }

// HandleMessage is the Matrix handler.
func (s *Service) HandleMessage(ctx context.Context, msg matrix.Message) {
	if msg.Sender != s.cfg.Steve {
		return
	}
	room, ok := s.rooms[msg.RoomID]
	if !ok {
		return
	}
	first, err := s.store.ClaimEvent(ctx, msg.EventID)
	if err != nil {
		slog.Error("event not recorded; not handled", "event", msg.EventID, "error", err.Error())
		s.say(ctx, msg.RoomID, msg.ThreadRoot, "I can't reach my database, so I didn't act on that. Try again shortly.", "My database is unavailable, so I didn't act on that.")
		return
	}
	if !first {
		return
	}
	if room.Approvals {
		s.handleApproval(ctx, msg)
		return
	}
	if s.cfg.RequireEncryption && !msg.Encrypted {
		slog.Warn("unencrypted message ignored", "event", msg.EventID)
		return
	}
	if msg.Edited || msg.Forwarded {
		slog.Info("edited or forwarded message ignored", "event", msg.EventID)
		return
	}
	s.handleTask(ctx, room, msg)
}

func (s *Service) transcript(ctx context.Context, msg matrix.Message) (string, error) {
	if msg.Kind != matrix.KindVoice {
		return msg.Body, nil
	}
	text, err := s.stt.Transcribe(ctx, msg.Audio, msg.AudioMIME)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(text) == "" {
		return "", errors.New("I couldn't make out any words")
	}
	return text, nil
}

func (s *Service) handleApproval(ctx context.Context, msg matrix.Message) {
	if s.gate == nil {
		return
	}
	text, err := s.transcript(ctx, msg)
	if err != nil {
		s.say(ctx, msg.RoomID, msg.ThreadRoot, "I couldn't transcribe that: "+err.Error(), "Sorry, I couldn't transcribe that.")
		return
	}
	if msg.Kind == matrix.KindVoice {
		_, _ = s.mx.SendText(ctx, msg.RoomID, msg.ThreadRoot, "Heard: "+text)
	}
	d := s.gate.Handle(ctx, msg, text)
	if s.OnDecision != nil {
		s.OnDecision(d)
	}
}

func (s *Service) handleTask(ctx context.Context, room Room, msg matrix.Message) {
	text, err := s.transcript(ctx, msg)
	if err != nil {
		s.say(ctx, msg.RoomID, msg.ThreadRoot, "I couldn't transcribe that: "+err.Error(), "Sorry, I couldn't transcribe that voice message.")
		return
	}
	if msg.Kind == matrix.KindVoice {
		if _, err := s.mx.SendText(ctx, msg.RoomID, msg.ThreadRoot, "Heard: "+text); err != nil {
			slog.Warn("transcript not posted", "error", err.Error())
		}
	}
	target := room.Target
	if th, err := s.store.GetThread(ctx, msg.RoomID, msg.ThreadRoot); err == nil && th.Target != "" {
		target = th.Target
	}
	request := text
	if name, rest, ok := s.targets.ParseOverride(text); ok {
		target, request = name, rest
	}
	if err := s.store.UpsertThread(ctx, store.Thread{RoomID: msg.RoomID, RootEvent: msg.ThreadRoot, Target: target}); err != nil {
		s.say(ctx, msg.RoomID, msg.ThreadRoot, "I can't reach my database, so I didn't send that.", "My database is unavailable, so I didn't send that.")
		return
	}
	if strings.TrimSpace(request) == "" {
		s.say(ctx, msg.RoomID, msg.ThreadRoot, fmt.Sprintf("OK, this thread now goes to %s.", target), "")
		return
	}
	origin := "text"
	if msg.Kind == matrix.KindVoice {
		origin = "voice"
	}
	task, err := s.store.CreateTask(ctx, store.Task{RoomID: msg.RoomID, ThreadRoot: msg.ThreadRoot, SourceEvent: msg.EventID, Origin: origin, Target: target,
		Repo: room.Repo, State: store.TaskReceived, Transcript: request, AudioSHA256: msg.AudioSHA256, CreatedAt: s.Now()})
	if err != nil {
		s.say(ctx, msg.RoomID, msg.ThreadRoot, "I can't reach my database, so I didn't send that.", "My database is unavailable, so I didn't send that.")
		return
	}
	s.send(ctx, task)
}

// send hands a stored task to its target. A task left in "received" by a
// crash is sent again on start; Orca handoff names are stable per task, so
// that finds the existing handoff instead of starting a second agent.
func (s *Service) send(ctx context.Context, task store.Task) {
	t, ok := s.targets.Get(task.Target)
	if !ok {
		s.fail(ctx, task, fmt.Sprintf("there is no target called %s", task.Target))
		return
	}
	id, err := t.Send(ctx, targets.Task{ID: task.ID, Brief: task.Transcript, Repo: task.Repo, Actor: s.cfg.Steve, Thread: task.ThreadRoot})
	if err != nil {
		s.fail(ctx, task, fmt.Sprintf("%s didn't take the task: %v", task.Target, err))
		return
	}
	task.ExternalID, task.State = id, store.TaskRunning
	if err := s.store.UpdateTask(ctx, task); err != nil {
		slog.Error("task sent but not recorded as running", "task", task.ID, "error", err.Error())
	}
	_, _ = s.mx.SendText(ctx, task.RoomID, task.ThreadRoot, fmt.Sprintf("Sent to %s as task %d. I'll read you the result here.", task.Target, task.ID))
	s.Wake()
}

func (s *Service) fail(ctx context.Context, task store.Task, reason string) {
	task.State, task.Error = store.TaskFailed, reason
	if err := s.store.UpdateTask(ctx, task); err != nil {
		slog.Error("task failure not recorded", "task", task.ID, "error", err.Error())
	}
	s.deliver(ctx, task)
}

// Wake makes the tracker poll now.
func (s *Service) Wake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Run tracks tasks until ctx ends.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(s.cfg.PollInterval)
	defer t.Stop()
	for {
		s.Poll(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.wake:
		}
	}
}

// Poll checks every undelivered task once.
func (s *Service) Poll(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pending, err := s.store.PendingTasks(ctx)
	if err != nil {
		slog.Warn("pending tasks unavailable", "error", err.Error())
		return
	}
	for _, task := range pending {
		switch task.State {
		case store.TaskReceived:
			if s.Now().Sub(task.CreatedAt) > time.Minute {
				s.send(ctx, task)
			}
		case store.TaskRunning:
			t, ok := s.targets.Get(task.Target)
			if !ok || task.ExternalID == "" {
				s.fail(ctx, task, "the task lost its target")
				continue
			}
			res, err := t.Result(ctx, task.ExternalID)
			if err != nil {
				slog.Debug("task status unavailable", "task", task.ID, "error", err.Error())
				continue
			}
			if !res.State.Done() {
				continue
			}
			s.finish(ctx, task, res.State == targets.StateSucceeded, res.Text)
		default:
			s.deliver(ctx, task)
		}
	}
}

func (s *Service) finish(ctx context.Context, task store.Task, ok bool, text string) {
	if ok {
		task.State, task.Result = store.TaskSucceeded, strings.TrimSpace(text)
	} else {
		task.State, task.Error = store.TaskFailed, orDefault(strings.TrimSpace(text), "the agent reported a failure")
	}
	if err := s.store.UpdateTask(ctx, task); err != nil {
		slog.Error("task result not recorded", "task", task.ID, "error", err.Error())
		return
	}
	if s.OnTask != nil {
		s.OnTask(task.Target, task.State)
	}
	s.deliver(ctx, task)
}

// deliver posts a finished task's outcome as text plus voice, once.
func (s *Service) deliver(ctx context.Context, task store.Task) {
	s.deliverMu.Lock()
	defer s.deliverMu.Unlock()
	if cur, err := s.store.GetTask(ctx, task.ID); err != nil || cur.Delivered {
		return
	}
	var text, spoken string
	switch task.State {
	case store.TaskSucceeded:
		result := orDefault(task.Result, "It finished without a summary.")
		text = fmt.Sprintf("Task %d (%s) is done:\n%s", task.ID, task.Target, result)
		spoken = result
		if s.summarizer != nil {
			if short, err := s.summarizer.Summarize(ctx, result); err == nil && short != "" {
				spoken = short
			}
		}
		spoken = fmt.Sprintf("Task %d is done. %s", task.ID, spoken)
	case store.TaskFailed:
		text = fmt.Sprintf("Task %d (%s) failed: %s", task.ID, task.Target, task.Error)
		spoken = fmt.Sprintf("Task %d didn't work out. %s", task.ID, task.Error)
	default:
		return
	}
	if _, err := s.Post(ctx, task.RoomID, task.ThreadRoot, text, spoken); err != nil {
		slog.Warn("task result not posted; will retry", "task", task.ID, "error", err.Error())
		return
	}
	task.Delivered = true
	if err := s.store.UpdateTask(ctx, task); err != nil {
		slog.Error("task delivery not recorded", "task", task.ID, "error", err.Error())
	}
}

// Post sends text and, when spoken is set, a voice note of it. It
// implements approvalgate.Out. A speech failure still delivers the text.
func (s *Service) Post(ctx context.Context, roomID, thread, text, spoken string) (string, error) {
	id, err := s.mx.SendText(ctx, roomID, thread, text)
	if err != nil {
		return "", err
	}
	if spoken = Speakable(spoken, s.cfg.MaxSpoken); spoken != "" && s.tts != nil {
		root := thread
		if root == "" {
			root = id // a new message starts its own thread for the voice note
		}
		audio, err := s.tts.Synthesize(ctx, spoken)
		if err != nil {
			slog.Warn("speech synthesis failed; text only", "error", err.Error())
			return id, nil
		}
		if _, err := s.mx.SendVoice(ctx, roomID, root, audio); err != nil {
			slog.Warn("voice note not posted", "error", err.Error())
		}
	}
	return id, nil
}

func (s *Service) say(ctx context.Context, room, thread, text, spoken string) {
	if _, err := s.Post(ctx, room, thread, text, spoken); err != nil {
		slog.Warn("reply not posted", "error", err.Error())
	}
}

var (
	urlPattern = regexp.MustCompile(`https?://\S+`)
	mdPattern  = regexp.MustCompile("[`*_#>|]+|\\[([^\\]]*)\\]\\([^)]*\\)")
)

// Speakable cleans text for TTS: no URLs or markdown, at most max bytes.
func Speakable(text string, max int) string {
	text = urlPattern.ReplaceAllString(text, "(link in the thread)")
	text = mdPattern.ReplaceAllString(text, "$1")
	text = strings.Join(strings.Fields(text), " ")
	if len(text) > max {
		text = speech.Truncate(text, max-40) + " The rest is in the thread."
	}
	return text
}

func orDefault(s, d string) string {
	if strings.TrimSpace(s) == "" {
		return d
	}
	return s
}
