package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"time"

	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/voice/service"
)

// Config is the non-secret configuration (a mounted ConfigMap, JSON).
// Secrets are separate files named by environment variables (secretEnv).
type Config struct {
	// Steve is the only Matrix user whose messages count.
	Steve  string       `json:"steve"`
	Matrix MatrixConfig `json:"matrix"`
	// Rooms: task rooms, one Inbox and one Approvals room.
	Rooms      []service.Room          `json:"rooms"`
	Speech     SpeechConfig            `json:"speech"`
	Targets    map[string]TargetConfig `json:"targets"`
	Gate       GateConfig              `json:"gate"`
	Summarizer SummarizerConfig        `json:"summarizer"`
	// PollInterval for running tasks (Go duration, default 30s).
	PollInterval string `json:"pollInterval,omitempty"`
}

type MatrixConfig struct {
	Homeserver string `json:"homeserver"`
	UserID     string `json:"userID"`
	// DeviceID is required with an access token, optional with a password.
	DeviceID   string `json:"deviceID,omitempty"`
	DeviceName string `json:"deviceName,omitempty"`
	// PinnedMasterKey is Steve's cross-signing master key; see docs/voice.md.
	PinnedMasterKey string `json:"pinnedMasterKey,omitempty"`
	// MaxEventAge drops older events (default 1h).
	MaxEventAge string `json:"maxEventAge,omitempty"`
}

type SpeechConfig struct {
	BaseURL  string  `json:"baseURL"`
	STTModel string  `json:"sttModel,omitempty"`
	TTSModel string  `json:"ttsModel,omitempty"`
	Voice    string  `json:"voice,omitempty"`
	Language string  `json:"language,omitempty"`
	Speed    float64 `json:"speed,omitempty"`
}

// TargetConfig configures one agent target. Kind is orca or archon.
type TargetConfig struct {
	Kind          string   `json:"kind"`
	HandoffURL    string   `json:"handoffURL,omitempty"`
	RunRecordsURL string   `json:"runRecordsURL,omitempty"`
	Agent         string   `json:"agent,omitempty"`
	DefaultRepo   string   `json:"defaultRepo,omitempty"`
	Base          string   `json:"base,omitempty"`
	BriefFooter   string   `json:"briefFooter,omitempty"`
	Aliases       []string `json:"aliases,omitempty"`
}

type GateConfig struct {
	// Enabled turns the approval gate on (needs an Approvals room and the
	// approval-gate Forgejo token).
	Enabled          bool     `json:"enabled"`
	ForgejoOrigin    string   `json:"forgejoOrigin,omitempty"`
	Repos            []string `json:"repos,omitempty"`
	MaxMergesPerHour int      `json:"maxMergesPerHour,omitempty"`
	CodeTTL          string   `json:"codeTTL,omitempty"`
	StatusContext    string   `json:"statusContext,omitempty"`
}

type SummarizerConfig struct {
	Enabled bool   `json:"enabled"`
	BaseURL string `json:"baseURL,omitempty"`
	Model   string `json:"model,omitempty"`
}

// Secret file environment variables.
const (
	envConfig           = "VOICE_CONFIG_FILE"
	envListen           = "VOICE_LISTEN"
	envDatabaseURL      = "VOICE_DATABASE_URL_FILE"
	envDatabaseCA       = "VOICE_DATABASE_CA_FILE"
	envDatabaseMaxConns = "VOICE_DATABASE_MAX_CONNS"
	envMatrixPassword   = "VOICE_MATRIX_PASSWORD_FILE"
	envMatrixToken      = "VOICE_MATRIX_ACCESS_TOKEN_FILE"
	envPickleKey        = "VOICE_MATRIX_PICKLE_KEY_FILE"
	envForgejoToken     = "VOICE_FORGEJO_TOKEN_FILE"
	envOrcaToken        = "VOICE_ORCA_TOKEN_FILE"
	envSpeechKey        = "VOICE_SPEECH_API_KEY_FILE"
	envSummarizerKey    = "VOICE_SUMMARIZER_API_KEY_FILE"
	envNotifyCallers    = "VOICE_NOTIFY_CALLERS_FILE"
	envLogLevel         = "VOICE_LOG_LEVEL"
)

func loadConfig(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("config file %s unreadable", path)
	}
	var c Config
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return Config{}, fmt.Errorf("config file: %w", err)
	}
	return c, c.validate()
}

func (c Config) validate() error {
	if c.Steve == "" {
		return errors.New("config: steve (Steve's MXID) is required")
	}
	if len(c.Rooms) == 0 {
		return errors.New("config: rooms are required")
	}
	if c.Speech.BaseURL == "" {
		return errors.New("config: speech.baseURL is required")
	}
	approvals := 0
	for _, r := range c.Rooms {
		if r.Approvals {
			approvals++
		}
	}
	if c.Gate.Enabled && approvals != 1 {
		return errors.New("config: the gate needs exactly one room with approvals: true")
	}
	if !c.Gate.Enabled && approvals > 0 {
		return errors.New("config: an approvals room is configured but gate.enabled is false")
	}
	if c.Gate.Enabled && (c.Gate.ForgejoOrigin == "" || len(c.Gate.Repos) == 0) {
		return errors.New("config: the gate needs forgejoOrigin and repos")
	}
	if len(c.Targets) == 0 {
		return errors.New("config: at least one target is required")
	}
	for _, d := range []string{c.PollInterval, c.Matrix.MaxEventAge, c.Gate.CodeTTL} {
		if d != "" {
			if _, err := time.ParseDuration(d); err != nil {
				return fmt.Errorf("config: bad duration %q", d)
			}
		}
	}
	return nil
}

func duration(s string, d time.Duration) time.Duration {
	if v, err := time.ParseDuration(s); err == nil && v > 0 {
		return v
	}
	return d
}

// secret reads a mounted Secret file named by env. Errors name the
// variable, never the content.
func secret(env string, required bool) (string, error) {
	path := os.Getenv(env)
	if path == "" {
		if required {
			return "", fmt.Errorf("%s is required", env)
		}
		return "", nil
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) && !required {
		// Optional keys are absent from the mounted Secret.
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("%s: file unreadable", env)
	}
	v := strings.TrimSpace(string(b))
	if v == "" && required {
		return "", fmt.Errorf("%s: file is empty", env)
	}
	return v, nil
}
