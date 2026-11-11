package telemetry

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

// Fixtures are assembled at run time so secret scanners never see a literal.
var (
	githubToken = "ghp_" + strings.Repeat("A1b2", 9)
	jwt         = "eyJhbGciOiJIUzI1NiJ9" + ".eyJzdWIiOiJzdGV2ZSJ9" + ".c2lnbmF0dXJlLXZhbHVl"
	password    = "hunter2-" + "correct-horse"
)

func TestRedactStringRemovesCredentials(t *testing.T) {
	for _, input := range []string{
		"Get \"https://steve:" + password + "@git.example/repo.git\": refused",
		"Authorization: Bearer " + githubToken,
		"upstream said: bearer " + strings.Repeat("x", 40),
		"token " + githubToken + " rejected",
		"id_token=" + jwt,
		"lease " + jwt,
		"password=" + password,
		`{"client_secret": "` + password + `"}`,
		"--password " + password,
		"https://relay.example/cb?code=" + password + "&state=ok",
		"-----BEGIN OPENSSH PRIVATE KEY-----\nAAAA" + password + "\n-----END OPENSSH PRIVATE KEY-----",
		"blob " + strings.Repeat("QUJD", 40),
		"Cookie: session=" + password,
	} {
		got := RedactString(input)
		for _, secret := range []string{githubToken, jwt, password, strings.Repeat("QUJD", 40), strings.Repeat("x", 40)} {
			if strings.Contains(got, secret) {
				t.Errorf("credential survived redaction:\n in: %q\nout: %q", input, got)
			}
		}
	}
}

func TestRedactStringKeepsOrdinaryText(t *testing.T) {
	for _, text := range []string{
		"grok credential owner renewal unavailable",
		"GitHub token unavailable",
		"forge agent token review failed",
		"database unavailable; audit record spooled to a ConfigMap",
		"workspace qualification-20261104 Ready",
	} {
		if got := RedactString(text); got != text {
			t.Errorf("ordinary text changed: %q -> %q", text, got)
		}
	}
}

func TestLoggerRedactsMessagesAttributesErrorsAndGroups(t *testing.T) {
	var out bytes.Buffer
	log := NewLogger(&out, slog.LevelInfo).With("api_key", password)
	log.Info("calling https://u:"+password+"@host/x",
		"error", errors.New("refresh failed: refresh_token="+password),
		"access_token", githubToken,
		"Authorization", "Bearer abc",
		"lease", slog.GroupValue(slog.String("token", jwt), slog.String("provider", "grok")),
		"detail", map[string]string{"auth": "Bearer " + githubToken},
		"input_tokens", 1234,
		"masterKey", "Zm9vYmFyYmF6cXV4",
		"provider", "cursor",
	)
	line := out.String()
	for _, secret := range []string{password, githubToken, jwt, "Bearer abc"} {
		if strings.Contains(line, secret) {
			t.Fatalf("credential in log line: %s", line)
		}
	}
	var record map[string]any
	if err := json.Unmarshal(out.Bytes(), &record); err != nil {
		t.Fatalf("log line is not JSON: %v", err)
	}
	if record["provider"] != "cursor" || record["masterKey"] != "Zm9vYmFyYmF6cXV4" || record["input_tokens"] != float64(1234) {
		t.Fatalf("ordinary attributes were changed: %s", line)
	}
	if record["api_key"] != Redacted || record["access_token"] != Redacted {
		t.Fatalf("sensitive keys not redacted: %s", line)
	}
	if lease := record["lease"].(map[string]any); lease["token"] != Redacted || lease["provider"] != "grok" {
		t.Fatalf("group not redacted: %s", line)
	}
}

func TestParseLevel(t *testing.T) {
	for name, want := range map[string]slog.Level{"debug": slog.LevelDebug, "WARN": slog.LevelWarn, "error": slog.LevelError, "": slog.LevelInfo, "bogus": slog.LevelInfo} {
		if got := ParseLevel(name); got != want {
			t.Errorf("ParseLevel(%q) = %v, want %v", name, got, want)
		}
	}
}
