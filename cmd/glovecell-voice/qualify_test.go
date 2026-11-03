package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/voice/speech"
)

func TestCheckSpeech(t *testing.T) {
	for _, transcript := range []string{speechCheckPhrase, "Voice, message, reply.", "unrelated speech"} {
		t.Run(transcript, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				switch r.URL.Path {
				case "/v1/audio/speech":
					var req map[string]any
					if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req["input"] != speechCheckPhrase || req["response_format"] != "opus" {
						t.Error("wrong synthesis request")
					}
					w.Write(speech.SyntheticVoice(time.Second))
				case "/v1/audio/transcriptions":
					if err := r.ParseMultipartForm(1 << 20); err != nil || r.FormValue("response_format") != "json" {
						t.Error("wrong transcription request")
					}
					json.NewEncoder(w).Encode(map[string]string{"text": transcript})
				default:
					t.Error("unexpected endpoint")
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			var out strings.Builder
			err := checkSpeech(context.Background(), speech.Config{BaseURL: srv.URL}, &out)
			if transcript == "unrelated speech" {
				if err == nil || out.Len() != 0 {
					t.Fatal("unrecognized phrase reported success")
				}
			} else if err != nil || !strings.Contains(out.String(), `"ok":true`) || calls.Load() != 2 {
				t.Fatalf("speech check: %v %s", err, out.String())
			}
		})
	}
}

func TestCheckSpeechRefusesBadAudioAndServerErrors(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusServiceUnavailable} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			w.Write([]byte("not audio"))
		}))
		var out strings.Builder
		err := checkSpeech(context.Background(), speech.Config{BaseURL: srv.URL}, &out)
		srv.Close()
		if err == nil || out.Len() != 0 {
			t.Fatalf("status %d reported success with invalid audio", status)
		}
	}
}

func TestCheckSpeechLive(t *testing.T) {
	url := os.Getenv("GLOVECELL_SPEECH_TEST_URL")
	if url == "" {
		t.Skip("set GLOVECELL_SPEECH_TEST_URL for opt-in real speech qualification")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	var out strings.Builder
	if err := checkSpeech(ctx, speech.Config{BaseURL: url}, &out); err != nil {
		t.Fatal(err)
	}
	t.Log(out.String())
}
