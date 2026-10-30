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
	// Target is the agent target the caller speaks for: Steve's replies in
	// the caller's threads go there, and the caller may complete tasks
	// sent to it.
	Target string
}

// Errors for notify callers.
var (
	ErrUnknownTask = errors.New("no such task for this caller")
	ErrBadRequest  = errors.New("bad request")
)

// NotifyMessage posts an agent's message or question to Steve as text and
// voice, clearly marked as coming from the caller. With task set it reports
// that task's outcome; final finishes it. It never approves anything.
func (s *Service) NotifyMessage(ctx context.Context, c Caller, kind, text string, taskID int64, final bool) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", ErrBadRequest
	}
	if taskID != 0 {
		task, err := s.store.GetTask(ctx, taskID)
		if errors.Is(err, store.ErrNotFound) || (err == nil && (c.Target == "" || task.Target != c.Target)) {
			return "", ErrUnknownTask
		}
		if err != nil {
			return "", err
		}
		if final {
			if task.Delivered || (task.State != store.TaskRunning && task.State != store.TaskReceived) {
				return "", fmt.Errorf("%w: task already finished", ErrBadRequest)
			}
			s.mu.Lock()
			s.finish(ctx, task, kind != "failure", text)
			s.mu.Unlock()
			return task.ThreadRoot, nil
		}
		return s.Post(ctx, task.RoomID, task.ThreadRoot, fmt.Sprintf("Update from %s on task %d: %s", c.Name, task.ID, text), fmt.Sprintf("Update on task %d from %s. %s", task.ID, c.Name, text))
	}
	room := s.inbox
	if c.Room != "" {
		r, ok := s.rooms[c.Room]
		if !ok || r.Approvals {
			return "", fmt.Errorf("%w: room not allowed", ErrBadRequest)
		}
		room = r
	}
	label, spokenLabel := "Message", "Message"
	if kind == "question" {
		label, spokenLabel = "Question", "Question"
	}
	id, err := s.Post(ctx, room.ID, "", fmt.Sprintf("%s from %s (agent):\n%s", label, c.Name, text), fmt.Sprintf("%s from %s. %s", spokenLabel, c.Name, text))
	if err != nil {
		return "", err
	}
	// Steve's replies in this thread go to the caller's target.
	if c.Target != "" {
		if _, ok := s.targets.Get(c.Target); ok {
			_ = s.store.UpsertThread(ctx, store.Thread{RoomID: room.ID, RootEvent: id, Target: c.Target})
		}
	}
	return id, nil
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
