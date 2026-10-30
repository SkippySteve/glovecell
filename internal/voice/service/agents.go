package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/approvalgate"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/voice/store"
)

// Feedback passes a gate decision (rejection, hold, revert request) back to
// an agent as a task, in a thread of the Approvals room so Steve hears the
// outcome there. It implements approvalgate.Feedback.
func (s *Service) Feedback(ctx context.Context, repo string, pr int, brief string) error {
	room := s.approvals
	if room.ID == "" {
		return errors.New("no Approvals room")
	}
	target := room.Target
	if target == "" {
		target = s.inbox.Target
	}
	if repo == "" {
		repo = orDefault(room.Repo, s.inbox.Repo)
	}
	thread, err := s.mx.SendText(ctx, room.ID, "", fmt.Sprintf("Passing this to %s: %s", target, brief))
	if err != nil {
		return err
	}
	task, err := s.store.CreateTask(ctx, store.Task{RoomID: room.ID, ThreadRoot: thread, SourceEvent: thread, Origin: "gate", Target: target, Repo: repo,
		State: store.TaskReceived, Transcript: brief, CreatedAt: s.Now()})
	if err != nil {
		return err
	}
	s.send(ctx, task)
	return nil
}

// Caller is an authenticated notify caller (an agent cell or orchestrator).
type Caller struct {
	Name string
	// Room is where the caller's messages go ("" means the Inbox room).
	Room string
	// Targets are the agent targets the caller speaks for: Steve's replies
	// in the caller's new threads go to the first, and the caller may finish
	// tasks sent to any of them.
	Targets []string
}

func (c Caller) speaksFor(target string) bool {
	for _, t := range c.Targets {
		if t == target {
			return true
		}
	}
	return false
}

// Note is one notify message.
type Note struct {
	// Kind is message, question, blocked or done.
	Kind string
	Text string
	// Room is a configured room ID or name ("" means the caller's room).
	Room string
	// Thread is the event ID of the thread to post in, for example Steve's
	// request.
	Thread string
	// Task names a task; with Final (or kind done) it finishes it.
	Task   int64
	Final  bool
	Failed bool
}

// Errors for notify callers.
var (
	ErrUnknownTask = errors.New("no such task for this caller")
	ErrBadRequest  = errors.New("bad request")
)

var noteLabels = map[string]string{"message": "Message", "question": "Question", "blocked": "Blocked", "done": "Done"}

func (s *Service) roomFor(c Caller, name string) (Room, error) {
	if name == "" {
		name = c.Room
	}
	if name == "" {
		return s.inbox, nil
	}
	r, ok := s.rooms[name]
	if !ok {
		for _, cand := range s.cfg.Rooms {
			if cand.Name != "" && strings.EqualFold(cand.Name, name) {
				r, ok = cand, true
				break
			}
		}
	}
	if !ok || r.Approvals {
		return Room{}, fmt.Errorf("%w: room not allowed", ErrBadRequest)
	}
	return r, nil
}

// NotifyMessage posts an agent's note to Steve as text and voice, marked as
// coming from the caller. In a thread it replies there; "done" (or Final)
// on a task the caller speaks for finishes the task, and its result is read
// out. It never approves anything.
func (s *Service) NotifyMessage(ctx context.Context, c Caller, n Note) (string, error) {
	n.Text = strings.TrimSpace(n.Text)
	label, ok := noteLabels[n.Kind]
	if !ok || n.Text == "" {
		return "", ErrBadRequest
	}
	final := n.Final || n.Kind == "done"
	if n.Task != 0 {
		task, err := s.store.GetTask(ctx, n.Task)
		if errors.Is(err, store.ErrNotFound) || (err == nil && !c.speaksFor(task.Target)) {
			return "", ErrUnknownTask
		}
		if err != nil {
			return "", err
		}
		return s.onTask(ctx, c, task, label, n, final)
	}
	room, err := s.roomFor(c, n.Room)
	if err != nil {
		return "", err
	}
	if n.Thread != "" {
		task, err := s.store.TaskInThread(ctx, room.ID, n.Thread)
		if err == nil && c.speaksFor(task.Target) {
			return s.onTask(ctx, c, task, label, n, final)
		}
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return "", err
		}
		return s.Post(ctx, room.ID, n.Thread, fmt.Sprintf("%s from %s (agent): %s", label, c.Name, n.Text), fmt.Sprintf("%s from %s. %s", label, c.Name, n.Text))
	}
	id, err := s.Post(ctx, room.ID, "", fmt.Sprintf("%s from %s (agent):\n%s", label, c.Name, n.Text), fmt.Sprintf("%s from %s. %s", label, c.Name, n.Text))
	if err != nil {
		return "", err
	}
	// Steve's replies in this thread go to the caller's target.
	if len(c.Targets) > 0 {
		if _, ok := s.targets.Get(c.Targets[0]); ok {
			_ = s.store.UpsertThread(ctx, store.Thread{RoomID: room.ID, RootEvent: id, Target: c.Targets[0]})
		}
	}
	return id, nil
}

func (s *Service) onTask(ctx context.Context, c Caller, task store.Task, label string, n Note, final bool) (string, error) {
	if final {
		if task.Delivered || (task.State != store.TaskRunning && task.State != store.TaskReceived) {
			return "", fmt.Errorf("%w: task already finished", ErrBadRequest)
		}
		s.mu.Lock()
		s.finish(ctx, task, !n.Failed, n.Text)
		s.mu.Unlock()
		return task.ThreadRoot, nil
	}
	return s.Post(ctx, task.RoomID, task.ThreadRoot, fmt.Sprintf("%s from %s on task %d: %s", label, c.Name, task.ID, n.Text), fmt.Sprintf("%s from %s on task %d. %s", label, c.Name, task.ID, n.Text))
}

// RequestCard asks the gate for an approval card. The caller gets no code:
// only Steve hears it.
func (s *Service) RequestCard(ctx context.Context, c Caller, repo string, pr int, note string) error {
	if s.gate == nil {
		return errors.New("approval gate not configured")
	}
	_, err := s.gate.RequestCard(ctx, approvalgate.CardRequest{Repo: repo, PR: pr, RequestedBy: "notify:" + c.Name, Note: note})
	return err
}
