package speech

import (
	"context"
	"sync"
	"time"
)

// Fake is a Transcriber and Synthesizer for tests. Transcribe returns the
// text registered for the audio (by content), or Default.
type Fake struct {
	mu      sync.Mutex
	texts   map[string]string
	Default string
	STTErr  error
	TTSErr  error
	Spoken  []string
}

var (
	_ Transcriber = (*Fake)(nil)
	_ Synthesizer = (*Fake)(nil)
)

// Voice registers what audio "says" and returns the audio.
func (f *Fake) Voice(text string) []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.texts == nil {
		f.texts = map[string]string{}
	}
	// Distinct audio per text, so hashes differ.
	audio := append(SyntheticVoice(time.Second), []byte(text)...)
	f.texts[string(audio)] = text
	return audio
}

func (f *Fake) Transcribe(_ context.Context, audio []byte, _ string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.STTErr != nil {
		return "", f.STTErr
	}
	if t, ok := f.texts[string(audio)]; ok {
		return t, nil
	}
	return f.Default, nil
}

func (f *Fake) Synthesize(_ context.Context, text string) (*Audio, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.TTSErr != nil {
		return nil, f.TTSErr
	}
	f.Spoken = append(f.Spoken, text)
	return Analyze(SyntheticVoice(2 * time.Second))
}
