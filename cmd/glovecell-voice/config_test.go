package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// clusterEnv sets the environment the cluster Deployment
// (infra/agent-workplace/glovecell-voice/deployment.yaml) provides, with its
// Secret volumes as directories.
func clusterEnv(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write := func(sub, key, value string) {
		p := filepath.Join(dir, sub)
		os.MkdirAll(p, 0o700)
		if err := os.WriteFile(filepath.Join(p, key), []byte(value+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("matrix", "user_id", "@glovecell:matrix.example.com")
	write("matrix", "access_token", "syt_secret_access_token")
	write("matrix", "device_id", "ABCDEFGHIJ")
	write("matrix", "pickle_key", strings.Repeat("p", 64))
	write("owner", "mxid", "@steve:matrix.example.com")
	write("owner", "master_key", "MSKsteveMSK")
	write("owner", "rooms", "!inbox:matrix.example.com,!approvals:matrix.example.com")
	write("forgejo", "token", "gate-token")
	write("handoff", "token", strings.Repeat("h", 64))
	write("notify", "orca-pilot", strings.Repeat("n", 64))
	write("database", "uri", "postgres://voice@db.example.com/voice")
	for k, v := range map[string]string{
		"VOICE_LISTEN_ADDR":            ":8080",
		"VOICE_HEALTH_ADDR":            ":8081",
		"MATRIX_HOMESERVER":            "https://matrix.cluster.example.com",
		"MATRIX_DEVICE_NAME":           "glovecell-voice",
		"MATRIX_CREDENTIALS_DIR":       filepath.Join(dir, "matrix"),
		"VOICE_OWNER_DIR":              filepath.Join(dir, "owner"),
		"SPEECH_BASE_URL":              "http://speaches.speaches.svc.cluster.local:8000/v1",
		"SPEECH_STT_MODEL":             "Systran/faster-distil-whisper-small.en",
		"SPEECH_TTS_MODEL":             "speaches-ai/Kokoro-82M-v1.0-ONNX",
		"SPEECH_VOICE":                 "af_heart",
		"SPEECH_LANGUAGE":              "en",
		"FORGEJO_URL":                  "http://forgejo-http.forgejo.svc.cluster.local:3000",
		"FORGEJO_TOKEN_FILE":           filepath.Join(dir, "forgejo", "token"),
		"ORCA_HANDOFF_URL":             "http://orca-handoff.agent-workspace-orca-pilot.svc.cluster.local:8791",
		"ORCA_RUN_RECORDS_URL":         "http://orca-run-records.agent-workspace-orca-pilot.svc.cluster.local:8790",
		"ORCA_HANDOFF_TOKEN_FILE":      filepath.Join(dir, "handoff", "token"),
		"NOTIFY_TOKENS_DIR":            filepath.Join(dir, "notify"),
		"DATABASE_URL_FILE":            filepath.Join(dir, "database", "uri"),
		"DATABASE_CA_FILE":             filepath.Join(dir, "database-ca", "ca.crt"),
		"APPROVAL_CODE_TTL":            "24h",
		"APPROVAL_MAX_MERGES_PER_HOUR": "10",
		"APPROVAL_REPOS":               "steve/*",
		"APPROVAL_ENABLED":             "true",
		"VOICE_DEFAULT_REPO":           "steve/agent-project-controller",
	} {
		t.Setenv(k, v)
	}
	return dir
}

func TestLoadClusterContract(t *testing.T) {
	clusterEnv(t)
	s, err := load()
	if err != nil {
		t.Fatal(err)
	}
	if s.Steve != "@steve:matrix.example.com" || s.MasterKey != "MSKsteveMSK" || s.AccessToken != "syt_secret_access_token" || s.Config.Matrix.DeviceID != "ABCDEFGHIJ" {
		t.Fatalf("owner or matrix %+v", s)
	}
	if !s.GateEnabled || s.Config.Gate.Repos[0] != "steve/*" || s.Config.Gate.MaxMergesPerHour != 10 || s.Listen != ":8080" || s.Health != ":8081" {
		t.Fatalf("gate or listeners %+v", s)
	}
	rooms := s.Config.Rooms
	if len(rooms) != 2 || !rooms[0].Inbox || !rooms[1].Approvals || rooms[0].Target != "orca" || rooms[0].Repo != "steve/agent-project-controller" {
		t.Fatalf("rooms %+v", rooms)
	}
	orca := s.Config.Targets["orca"]
	if orca.HandoffURL == "" || orca.RunRecordsURL == "" || !strings.Contains(orca.BriefFooter, "orca-pilot voice send") || s.Config.Targets["codex"].Agent != "codex" {
		t.Fatalf("targets %+v", s.Config.Targets)
	}
}

func TestLoadWithConfigFile(t *testing.T) {
	dir := clusterEnv(t)
	os.WriteFile(filepath.Join(dir, "owner", "rooms"), []byte("!inbox:matrix.example.com inbox, !approvals:matrix.example.com approvals, !apc:matrix.example.com"), 0o600)
	t.Setenv("VOICE_CONFIG_FILE", "../../docs/examples/glovecell-voice-config.json")
	s, err := load()
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Config.Rooms) != 3 || s.Config.Rooms[2].Target != "codex" || s.Config.Rooms[2].Name != "APC" {
		t.Fatalf("room details from the file not applied: %+v", s.Config.Rooms)
	}
}

func TestLoadRefusals(t *testing.T) {
	for name, mutate := range map[string]func(dir string){
		"missing gate token": func(dir string) { os.Remove(filepath.Join(dir, "forgejo", "token")) },
		"bad gate switch":    func(dir string) { t.Setenv("APPROVAL_ENABLED", "yes") },
		"gate without room": func(dir string) {
			os.WriteFile(filepath.Join(dir, "owner", "rooms"), []byte("!inbox:matrix.example.com inbox"), 0o600)
		},
		"no pinned key":      func(dir string) { os.Remove(filepath.Join(dir, "owner", "master_key")) },
		"no pickle key":      func(dir string) { os.Remove(filepath.Join(dir, "matrix", "pickle_key")) },
		"no owner":           func(dir string) { t.Setenv("VOICE_OWNER_DIR", "") },
		"gate without repos": func(dir string) { t.Setenv("APPROVAL_REPOS", "") },
		"bad merges":         func(dir string) { t.Setenv("APPROVAL_MAX_MERGES_PER_HOUR", "lots") },
		"bad room": func(dir string) {
			os.WriteFile(filepath.Join(dir, "owner", "rooms"), []byte("#inbox:example.com"), 0o600)
		},
		"two approvals rooms": func(dir string) {
			os.WriteFile(filepath.Join(dir, "owner", "rooms"), []byte("!a:x approvals,!b:x approvals"), 0o600)
		},
		"no targets":       func(dir string) { t.Setenv("ORCA_HANDOFF_URL", "") },
		"no database":      func(dir string) { t.Setenv("DATABASE_URL_FILE", "") },
		"bad default repo": func(dir string) { t.Setenv("VOICE_DEFAULT_REPO", "nope") },
	} {
		t.Run(name, func(t *testing.T) {
			dir := clusterEnv(t)
			mutate(dir)
			_, err := load()
			if err == nil {
				t.Fatal("accepted")
			}
			for _, secret := range []string{"syt_secret_access_token", "gate-token", strings.Repeat("p", 64)} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("error leaks a secret: %v", err)
				}
			}
		})
	}
}

func TestGateOffForAsyncTasks(t *testing.T) {
	dir := clusterEnv(t)
	t.Setenv("APPROVAL_ENABLED", "false")
	os.Remove(filepath.Join(dir, "forgejo", "token"))
	t.Setenv("APPROVAL_REPOS", "")
	s, err := load()
	if err != nil || s.GateEnabled {
		t.Fatalf("gate %v %v", s.GateEnabled, err)
	}
}

func TestParseRooms(t *testing.T) {
	rooms, err := ParseRooms("!a:x\n!b:x target=codex repo=steve/b name=B, !c:x approvals")
	if err != nil || len(rooms) != 3 || rooms[0].Inbox || !rooms[2].Approvals || rooms[1].Target != "codex" || rooms[1].Repo != "steve/b" {
		t.Fatalf("%+v %v", rooms, err)
	}
	if _, err := ParseRooms("!a:x admin"); err == nil {
		t.Fatal("unknown word accepted")
	}
}

func TestGateDefaultsOffEvenWithToken(t *testing.T) {
	clusterEnv(t)
	t.Setenv("APPROVAL_ENABLED", "")
	s, err := load()
	if err != nil || s.GateEnabled || s.ForgejoToken != "" {
		t.Fatalf("gate activated or credential loaded without opt-in: %v", err)
	}
}

func TestGateDisabledOverridesConfigFile(t *testing.T) {
	dir := clusterEnv(t)
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"gate":{"enabled":true}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VOICE_CONFIG_FILE", path)
	t.Setenv("APPROVAL_ENABLED", "false")
	s, err := load()
	if err != nil || s.GateEnabled || s.ForgejoToken != "" {
		t.Fatalf("disabled gate loaded merge credentials: %v", err)
	}
}
