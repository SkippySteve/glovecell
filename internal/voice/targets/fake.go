package targets

import (
	"context"
	"fmt"
	"sync"
)

// Fake is a Target for tests. Tasks stay running until Finish.
type Fake struct {
	TargetName string
	// SendErr fails Send.
	SendErr error

	mu      sync.Mutex
	Sent    []Task
	results map[string]Result
}

var _ Target = (*Fake)(nil)

func (f *Fake) Name() string {
	if f.TargetName == "" {
		return "fake"
	}
	return f.TargetName
}

func (f *Fake) Send(_ context.Context, t Task) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.SendErr != nil {
		return "", f.SendErr
	}
	f.Sent = append(f.Sent, t)
	return fmt.Sprintf("%s-%d", f.Name(), t.ID), nil
}

// Finish completes a task.
func (f *Fake) Finish(id string, state State, text string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.results == nil {
		f.results = map[string]Result{}
	}
	f.results[id] = Result{State: state, Text: text}
}

// Tasks returns what was sent.
func (f *Fake) Tasks() []Task {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Task(nil), f.Sent...)
}

func (f *Fake) Status(_ context.Context, id string) (Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r, ok := f.results[id]; ok {
		return Status{State: r.State}, nil
	}
	return Status{State: StateRunning}, nil
}

func (f *Fake) Result(_ context.Context, id string) (Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r, ok := f.results[id]; ok {
		return r, nil
	}
	return Result{State: StateRunning}, nil
}
