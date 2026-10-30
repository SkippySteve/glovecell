// Package speech is a client for the OpenAI-compatible speech API:
// POST /v1/audio/transcriptions (speech to text) and POST /v1/audio/speech
// (text to speech). The default server is a self-hosted speaches
// (faster-whisper for STT, Kokoro for TTS); any compatible provider (OpenAI,
// Groq and others) works by changing the base URL, models and key.
//
// Matrix voice messages are Ogg/Opus. They are sent to the transcription
// endpoint unchanged: speaches decodes uploads with PyAV (faster_whisper's
// decode_audio), and OpenAI accepts .ogg, so no conversion is needed.
// Replies are requested with response_format=opus, which both speaches
// (ffmpeg's Ogg muxer with libopus) and OpenAI return as Ogg/Opus, the format
// Element X plays as a voice message. Synthesize checks that it got Ogg/Opus
// and refuses anything else rather than post a file clients cannot play.
package speech

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

// Defaults for a self-hosted speaches.
const (
	DefaultSTTModel = "Systran/faster-distil-whisper-small.en"
	// DefaultTTSModel is Kokoro-82M (hexgrad/Kokoro-82M) as speaches packages it.
	DefaultTTSModel = "speaches-ai/Kokoro-82M-v1.0-ONNX"
	DefaultVoice    = "af_heart"
	// MaxAudioBytes is the largest upload accepted (OpenAI's limit is 25 MB).
	MaxAudioBytes = 25 << 20
	// MaxSpeechInput is the longest text sent to TTS in one request
	// (OpenAI's limit is 4096 characters).
	MaxSpeechInput = 4096
	maxResponse    = 32 << 20
)

// ErrNotOggOpus reports a TTS response that is not Ogg/Opus.
var ErrNotOggOpus = errors.New("speech server did not return Ogg/Opus audio")

// Transcriber turns audio into text.
type Transcriber interface {
	Transcribe(ctx context.Context, audio []byte, mimeType string) (string, error)
}

// Synthesizer turns text into Ogg/Opus audio.
type Synthesizer interface {
	Synthesize(ctx context.Context, text string) (*Audio, error)
}

// Audio is a synthesized voice note with what a Matrix voice message needs.
type Audio struct {
	Data     []byte
	MIMEType string
	// Duration is the playing time.
	Duration time.Duration
	// Waveform has WaveformBins values from 0 to 1024 (MSC1767/MSC3245).
	Waveform []int
}

// Config configures a Client.
type Config struct {
	// BaseURL is the server origin, with or without a trailing /v1.
	BaseURL  string
	APIKey   string
	STTModel string
	TTSModel string
	Voice    string
	// Language is an optional ISO-639-1 hint for transcription.
	Language string
	// Speed is the TTS speed (0 means the server default).
	Speed   float64
	Timeout time.Duration
}

// Client talks to one OpenAI-compatible speech server.
type Client struct {
	base string
	cfg  Config
	HTTP *http.Client
}

var (
	_ Transcriber = (*Client)(nil)
	_ Synthesizer = (*Client)(nil)
)

// New validates c and returns a client. The API key is never logged or
// returned in errors.
func New(c Config) (*Client, error) {
	u, err := url.Parse(strings.TrimSpace(c.BaseURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("speech base URL must be an http(s) URL without credentials, query or fragment")
	}
	base := strings.TrimSuffix(u.String(), "/")
	base = strings.TrimSuffix(base, "/v1")
	if c.STTModel == "" {
		c.STTModel = DefaultSTTModel
	}
	if c.TTSModel == "" {
		c.TTSModel = DefaultTTSModel
	}
	if c.Voice == "" {
		c.Voice = DefaultVoice
	}
	if c.Timeout <= 0 {
		c.Timeout = 5 * time.Minute
	}
	return &Client{base: base, cfg: c, HTTP: &http.Client{Timeout: c.Timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (c *Client) do(req *http.Request) ([]byte, http.Header, error) {
	if c.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("speech server unreachable: %w", redact(err))
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil {
		return nil, nil, fmt.Errorf("speech server response: %w", err)
	}
	if len(body) > maxResponse {
		return nil, nil, errors.New("speech server response too large")
	}
	if resp.StatusCode/100 != 2 {
		return nil, nil, fmt.Errorf("speech server returned HTTP %d: %s", resp.StatusCode, snippet(body))
	}
	return body, resp.Header, nil
}

// redact strips the URL from client errors (it carries no secret today, but
// a base URL could).
func redact(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if !utf8.ValidString(s) {
		return "(binary)"
	}
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// Transcribe sends the audio as it is (Matrix voice notes are Ogg/Opus) and
// returns the trimmed transcript.
func (c *Client) Transcribe(ctx context.Context, audio []byte, mimeType string) (string, error) {
	if len(audio) == 0 {
		return "", errors.New("empty audio")
	}
	if len(audio) > MaxAudioBytes {
		return "", fmt.Errorf("audio is %d bytes; the limit is %d", len(audio), MaxAudioBytes)
	}
	mimeType, name := uploadName(audio, mimeType)
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename="%s"`, name))
	h.Set("Content-Type", mimeType)
	part, err := w.CreatePart(h)
	if err != nil {
		return "", err
	}
	if _, err := part.Write(audio); err != nil {
		return "", err
	}
	fields := map[string]string{"model": c.cfg.STTModel, "response_format": "json"}
	if c.cfg.Language != "" {
		fields["language"] = c.cfg.Language
	}
	for k, v := range fields {
		if err := w.WriteField(k, v); err != nil {
			return "", err
		}
	}
	if err := w.Close(); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/v1/audio/transcriptions", &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	out, _, err := c.do(req)
	if err != nil {
		return "", err
	}
	var parsed struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		return "", errors.New("speech server returned an unreadable transcription")
	}
	return strings.TrimSpace(parsed.Text), nil
}

// uploadName picks a file name whose extension matches the audio, because
// some servers choose the decoder from it.
func uploadName(audio []byte, mimeType string) (string, string) {
	mimeType = strings.ToLower(strings.TrimSpace(strings.SplitN(mimeType, ";", 2)[0]))
	if IsOgg(audio) {
		return "audio/ogg", "voice.ogg"
	}
	switch mimeType {
	case "audio/mpeg", "audio/mp3":
		return mimeType, "voice.mp3"
	case "audio/mp4", "audio/m4a", "audio/x-m4a", "audio/aac":
		return mimeType, "voice.m4a"
	case "audio/wav", "audio/x-wav", "audio/wave":
		return mimeType, "voice.wav"
	case "audio/webm":
		return mimeType, "voice.webm"
	case "audio/flac":
		return mimeType, "voice.flac"
	case "":
		return "application/octet-stream", "voice.ogg"
	}
	return mimeType, "voice.ogg"
}

// Synthesize asks for Ogg/Opus speech. Text longer than MaxSpeechInput is cut
// at a sentence or word boundary; callers post the full text alongside.
func (c *Client) Synthesize(ctx context.Context, text string) (*Audio, error) {
	text = Truncate(strings.TrimSpace(text), MaxSpeechInput)
	if text == "" {
		return nil, errors.New("nothing to say")
	}
	req := map[string]any{"model": c.cfg.TTSModel, "input": text, "voice": c.cfg.Voice, "response_format": "opus"}
	if c.cfg.Speed > 0 {
		req["speed"] = c.cfg.Speed
	}
	b, _ := json.Marshal(req)
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/v1/audio/speech", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	hr.Header.Set("Content-Type", "application/json")
	out, _, err := c.do(hr)
	if err != nil {
		return nil, err
	}
	return Analyze(out)
}

// Analyze checks that data is Ogg/Opus and computes its duration and
// waveform.
func Analyze(data []byte) (*Audio, error) {
	info, err := ParseOggOpus(data)
	if err != nil {
		return nil, err
	}
	return &Audio{Data: data, MIMEType: "audio/ogg", Duration: info.Duration, Waveform: info.Waveform(WaveformBins)}, nil
}

// Truncate shortens s to at most max bytes, preferring a sentence end and
// then a space, and marks the cut.
func Truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	const mark = " …"
	cut := s[:max-len(mark)]
	for !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	if i := strings.LastIndexAny(cut, ".!?"); i > len(cut)/2 {
		return cut[:i+1] + mark
	}
	if i := strings.LastIndexByte(cut, ' '); i > len(cut)/2 {
		return cut[:i] + mark
	}
	return cut + mark
}
