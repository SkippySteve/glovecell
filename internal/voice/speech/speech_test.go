package speech

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestOggOpusRoundTrip(t *testing.T) {
	data := SyntheticVoice(3 * time.Second)
	info, err := ParseOggOpus(data)
	if err != nil {
		t.Fatal(err)
	}
	if info.PreSkip != 312 || len(info.PacketSizes) != 150 {
		t.Fatalf("preskip %d packets %d", info.PreSkip, len(info.PacketSizes))
	}
	if info.Duration != 3*time.Second {
		t.Fatalf("duration %v", info.Duration)
	}
	w := info.Waveform(WaveformBins)
	if len(w) != WaveformBins {
		t.Fatalf("waveform bins %d", len(w))
	}
	var max int
	for _, v := range w {
		if v < 0 || v > 1024 {
			t.Fatalf("waveform value %d out of range", v)
		}
		if v > max {
			max = v
		}
	}
	if max != 1024 {
		t.Fatalf("waveform not scaled: max %d", max)
	}
}

func TestParseRejectsCorruptOrForeignAudio(t *testing.T) {
	good := SyntheticVoice(time.Second)
	bad := append([]byte(nil), good...)
	bad[len(bad)-1] ^= 0xff
	for name, data := range map[string][]byte{
		"empty":     nil,
		"mp3":       []byte("ID3\x03\x00\x00\x00\x00\x00\x00"),
		"corrupt":   bad,
		"truncated": good[:len(good)-5],
		"vorbis":    EncodeOggOpus(nil, 960)[:0],
	} {
		if _, err := ParseOggOpus(data); !errors.Is(err, ErrNotOggOpus) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestTranscribePassesOggOpusThrough(t *testing.T) {
	voice := SyntheticVoice(time.Second)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/audio/transcriptions" || r.Header.Get("Authorization") != "Bearer k" {
			http.Error(w, "bad", 400)
			return
		}
		f, h, err := r.FormFile("file")
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		b, _ := io.ReadAll(f)
		if string(b) != string(voice) || h.Filename != "voice.ogg" || h.Header.Get("Content-Type") != "audio/ogg" {
			http.Error(w, "audio altered", 400)
			return
		}
		if r.FormValue("model") != DefaultSTTModel || r.FormValue("language") != "en" {
			http.Error(w, "model", 400)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"text": "  approve 4721  "})
	}))
	defer srv.Close()
	c, err := New(Config{BaseURL: srv.URL + "/v1/", APIKey: "k", Language: "en"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.Transcribe(context.Background(), voice, "audio/ogg; codecs=opus")
	if err != nil || got != "approve 4721" {
		t.Fatalf("%q %v", got, err)
	}
}

func TestSynthesizeRequestsOpusAndChecksIt(t *testing.T) {
	var body map[string]any
	reply := SyntheticVoice(2 * time.Second)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "audio/opus")
		w.Write(reply)
	}))
	defer srv.Close()
	c, _ := New(Config{BaseURL: srv.URL, Voice: "bf_emma", TTSModel: "tts-1"})
	a, err := c.Synthesize(context.Background(), "Done. "+strings.Repeat("word ", 2000))
	if err != nil {
		t.Fatal(err)
	}
	if body["response_format"] != "opus" || body["voice"] != "bf_emma" || body["model"] != "tts-1" {
		t.Fatalf("request %v", body)
	}
	if n := len(body["input"].(string)); n > MaxSpeechInput {
		t.Fatalf("input not truncated: %d", n)
	}
	if a.MIMEType != "audio/ogg" || a.Duration != 2*time.Second || len(a.Waveform) != WaveformBins {
		t.Fatalf("audio %v %v %d", a.MIMEType, a.Duration, len(a.Waveform))
	}
	reply = []byte("RIFF....WAVEfmt ")
	if _, err := c.Synthesize(context.Background(), "hi"); !errors.Is(err, ErrNotOggOpus) {
		t.Fatalf("non-Opus reply accepted: %v", err)
	}
}

func TestServerErrorsAndConfig(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "model not loaded", 500)
	}))
	defer srv.Close()
	c, _ := New(Config{BaseURL: srv.URL})
	if _, err := c.Transcribe(context.Background(), []byte("OggS"), ""); err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("server error hidden: %v", err)
	}
	if _, err := c.Transcribe(context.Background(), nil, ""); err == nil {
		t.Fatal("empty audio accepted")
	}
	for _, bad := range []string{"", "ftp://x", "https://user:pw@x", "https://x?a=1"} {
		if _, err := New(Config{BaseURL: bad}); err == nil {
			t.Errorf("base URL %q accepted", bad)
		}
	}
}

func TestTruncate(t *testing.T) {
	s := "First sentence. Second sentence is long. " + strings.Repeat("x", 100)
	if got := Truncate(s, 60); got != "First sentence. Second sentence is long. …" {
		t.Fatalf("%q", got)
	}
	if got := Truncate("short", 60); got != "short" {
		t.Fatal(got)
	}
}
