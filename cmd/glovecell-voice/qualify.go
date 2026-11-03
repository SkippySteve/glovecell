package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode"

	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/voice/speech"
)

const speechCheckPhrase = "The voice service can send a message and read the reply."

// checkSpeech uses only a fixed public phrase: no Matrix login, task launch,
// Forgejo access or private recording. Run it from the deployed voice container
// to qualify the same speech models and network path the bot uses.
func checkSpeech(ctx context.Context, cfg speech.Config, out io.Writer) error {
	client, err := speech.New(cfg)
	if err != nil {
		return err
	}
	start := time.Now()
	audio, err := client.Synthesize(ctx, speechCheckPhrase)
	if err != nil {
		return fmt.Errorf("speech check synthesis: %w", err)
	}
	ttsMS := time.Since(start).Milliseconds()
	start = time.Now()
	transcript, err := client.Transcribe(ctx, audio.Data, audio.MIMEType)
	if err != nil {
		return fmt.Errorf("speech check transcription: %w", err)
	}
	words := strings.FieldsFunc(strings.ToLower(transcript), func(r rune) bool { return !unicode.IsLetter(r) })
	seen := map[string]bool{}
	for _, word := range words {
		seen[word] = true
	}
	for _, word := range []string{"voice", "message", "reply"} {
		if !seen[word] {
			return errors.New("speech check did not recognize the test phrase")
		}
	}
	return json.NewEncoder(out).Encode(map[string]any{
		"check": "speech-roundtrip", "ok": true, "transcript": transcript,
		"tts_ms": ttsMS, "stt_ms": time.Since(start).Milliseconds(),
		"audio_bytes": len(audio.Data), "duration_ms": audio.Duration.Milliseconds(),
	})
}

func checkSpeechFromEnv(out io.Writer) error {
	key, err := secret(envSpeechKey, false)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	return checkSpeech(ctx, speech.Config{
		BaseURL: os.Getenv(envSpeechURL), APIKey: key,
		STTModel: os.Getenv(envSTTModel), TTSModel: os.Getenv(envTTSModel),
		Voice: os.Getenv(envVoice), Language: os.Getenv(envLanguage),
	}, out)
}
