package matrix

import (
	"context"
	"fmt"
	"sync"

	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/voice/speech"
)

// Posted is one message the Fake sent.
type Posted struct {
	RoomID, ThreadRoot, EventID string
	Text                        string
	Voice                       *speech.Audio
}

// Fake is a Sender for tests. Fail makes every send fail.
type Fake struct {
	mu   sync.Mutex
	Sent []Posted
	Fail error
	n    int
}

var _ Sender = (*Fake)(nil)

func (f *Fake) post(p Posted) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Fail != nil {
		return "", f.Fail
	}
	f.n++
	p.EventID = fmt.Sprintf("$sent%d", f.n)
	f.Sent = append(f.Sent, p)
	return p.EventID, nil
}

func (f *Fake) SendText(_ context.Context, room, root, text string) (string, error) {
	return f.post(Posted{RoomID: room, ThreadRoot: root, Text: text})
}

func (f *Fake) SendVoice(_ context.Context, room, root string, a *speech.Audio) (string, error) {
	return f.post(Posted{RoomID: room, ThreadRoot: root, Voice: a})
}

// Texts returns the texts sent so far.
func (f *Fake) Texts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, p := range f.Sent {
		if p.Voice == nil {
			out = append(out, p.Text)
		}
	}
	return out
}

// Voices counts the voice notes sent so far.
func (f *Fake) Voices() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, p := range f.Sent {
		if p.Voice != nil {
			n++
		}
	}
	return n
}
