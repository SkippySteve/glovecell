package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/approvalgate"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/voice/service"
)

// The service is configured from environment variables and mounted Secret
// files (the cluster's contract, infra/agent-workplace/glovecell-voice), with
// an optional JSON file for what plain variables cannot say well: per-room
// targets and repositories, target aliases, the summarizer. Variables and
// Secret files win over the JSON file; the owner Secret alone decides who
// the bot obeys and in which rooms.
const (
	envConfig          = "VOICE_CONFIG_FILE"
	envListen          = "VOICE_LISTEN_ADDR"
	envHealth          = "VOICE_HEALTH_ADDR"
	envLogLevel        = "VOICE_LOG_LEVEL"
	envHomeserver      = "MATRIX_HOMESERVER"
	envDeviceName      = "MATRIX_DEVICE_NAME"
	envMatrixDir       = "MATRIX_CREDENTIALS_DIR" // user_id, access_token, device_id, pickle_key[, password]
	envOwnerDir        = "VOICE_OWNER_DIR"        // mxid, master_key, rooms
	envDefaultRepo     = "VOICE_DEFAULT_REPO"
	envSpeechURL       = "SPEECH_BASE_URL"
	envSTTModel        = "SPEECH_STT_MODEL"
	envTTSModel        = "SPEECH_TTS_MODEL"
	envVoice           = "SPEECH_VOICE"
	envLanguage        = "SPEECH_LANGUAGE"
	envSpeechKey       = "SPEECH_API_KEY_FILE"
	envSummarizerKey   = "SUMMARIZER_API_KEY_FILE"
	envForgejoURL      = "FORGEJO_URL"
	envForgejoToken    = "FORGEJO_TOKEN_FILE"
	envOrcaHandoff     = "ORCA_HANDOFF_URL"
	envOrcaRunRecords  = "ORCA_RUN_RECORDS_URL"
	envOrcaToken       = "ORCA_HANDOFF_TOKEN_FILE"
	envOrcaFooter      = "ORCA_BRIEF_FOOTER"
	envNotifyDir       = "NOTIFY_TOKENS_DIR"
	envNotifyTargets   = "NOTIFY_CALLER_TARGETS"
	envNotifyCallers   = "NOTIFY_CALLERS_FILE"
	envDatabaseURL     = "DATABASE_URL_FILE"
	envDatabaseCA      = "DATABASE_CA_FILE"
	envDatabaseConns   = "DATABASE_MAX_CONNS"
	envCodeTTL         = "APPROVAL_CODE_TTL"
	envMaxMerges       = "APPROVAL_MAX_MERGES_PER_HOUR"
	envApprovalRepos   = "APPROVAL_REPOS"
	envApprovalContext = "APPROVAL_STATUS_CONTEXT"
	envPollInterval    = "VOICE_POLL_INTERVAL"
)

// defaultOrcaFooter tells Orca agents how to report back; {thread} and
// {task} are filled in per task.
const defaultOrcaFooter = "When you finish, tell Steve with: orca-pilot voice send --kind done --thread {thread} --text \"<two to five plain sentences>\"."

// Config is the optional JSON file.
type Config struct {
	Steve      string                  `json:"steve,omitempty"`
	Matrix     MatrixConfig            `json:"matrix"`
	Rooms      []service.Room          `json:"rooms,omitempty"`
	Speech     SpeechConfig            `json:"speech"`
	Targets    map[string]TargetConfig `json:"targets,omitempty"`
	Gate       GateConfig              `json:"gate"`
	Summarizer SummarizerConfig        `json:"summarizer"`
	// PollInterval for running tasks (Go duration, default 30s).
	PollInterval string `json:"pollInterval,omitempty"`
}

type MatrixConfig struct {
	Homeserver string `json:"homeserver,omitempty"`
	UserID     string `json:"userID,omitempty"`
	DeviceID   string `json:"deviceID,omitempty"`
	DeviceName string `json:"deviceName,omitempty"`
	// MaxEventAge drops older events (default 1h).
	MaxEventAge string `json:"maxEventAge,omitempty"`
}

type SpeechConfig struct {
	BaseURL  string  `json:"baseURL,omitempty"`
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

// Settings are the resolved configuration, secrets included. Never log it.
type Settings struct {
	Config
	Steve           string
	MasterKey       string
	AccessToken     string
	Password        string
	PickleKey       string
	ForgejoToken    string
	OrcaToken       string
	SpeechKey       string
	SummarizerKey   string
	GateEnabled     bool
	Listen, Health  string
	DatabaseURLFile string
	DatabaseCAFile  string
	DatabaseConns   int
}

func loadConfigFile(path string) (Config, error) {
	var c Config
	b, err := os.ReadFile(path)
	if err != nil {
		return c, fmt.Errorf("%s: file unreadable", envConfig)
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return c, fmt.Errorf("%s: %w", envConfig, err)
	}
	return c, nil
}

// readFile reads a mounted Secret file. Missing optional files are "".
// Errors name the variable or key, never the content.
func readFile(path, what string, required bool) (string, error) {
	if path == "" {
		if required {
			return "", fmt.Errorf("%s is required", what)
		}
		return "", nil
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) && !required {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("%s: file unreadable", what)
	}
	v := strings.TrimSpace(string(b))
	if v == "" && required {
		return "", fmt.Errorf("%s: file is empty", what)
	}
	return v, nil
}

func secret(env string, required bool) (string, error) {
	return readFile(os.Getenv(env), env, required)
}

func dirFile(dirEnv, key string, required bool) (string, error) {
	dir := os.Getenv(dirEnv)
	if dir == "" {
		if required {
			return "", fmt.Errorf("%s is required", dirEnv)
		}
		return "", nil
	}
	return readFile(filepath.Join(dir, key), dirEnv+"/"+key, required)
}

func setIf(dst *string, env string) {
	if v := strings.TrimSpace(os.Getenv(env)); v != "" {
		*dst = v
	}
}

// ParseRooms reads the owner Secret's rooms: room IDs separated by commas
// or newlines, each optionally followed by words: inbox, approvals,
// target=NAME, repo=OWNER/NAME, name=NAME. Without inbox or approvals words
// the first room is the Inbox and the second the Approvals room.
func ParseRooms(s string) ([]service.Room, error) {
	var rooms []service.Room
	marked := false
	for _, entry := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == '\n' }) {
		f := strings.Fields(entry)
		if len(f) == 0 {
			continue
		}
		r := service.Room{ID: f[0]}
		if !strings.HasPrefix(r.ID, "!") || !strings.Contains(r.ID, ":") {
			return nil, fmt.Errorf("rooms: %q is not a room ID (!id:server)", r.ID)
		}
		for _, w := range f[1:] {
			k, v, _ := strings.Cut(w, "=")
			switch k {
			case "inbox":
				r.Inbox, marked = true, true
			case "approvals":
				r.Approvals, marked = true, true
			case "target":
				r.Target = v
			case "repo":
				r.Repo = v
			case "name":
				r.Name = v
			default:
				return nil, fmt.Errorf("rooms: unknown word %q", w)
			}
		}
		rooms = append(rooms, r)
	}
	if !marked && len(rooms) > 0 {
		rooms[0].Inbox = true
		if rooms[0].Name == "" {
			rooms[0].Name = "Inbox"
		}
		if len(rooms) > 1 {
			rooms[1].Approvals = true
			if rooms[1].Name == "" {
				rooms[1].Name = "Approvals"
			}
		}
	}
	return rooms, nil
}

// load resolves the configuration from the environment.
func load() (Settings, error) {
	var s Settings
	if path := os.Getenv(envConfig); path != "" {
		c, err := loadConfigFile(path)
		if err != nil {
			return s, err
		}
		s.Config = c
	}
	c := &s.Config
	setIf(&c.Matrix.Homeserver, envHomeserver)
	setIf(&c.Matrix.DeviceName, envDeviceName)
	setIf(&c.Speech.BaseURL, envSpeechURL)
	setIf(&c.Speech.STTModel, envSTTModel)
	setIf(&c.Speech.TTSModel, envTTSModel)
	setIf(&c.Speech.Voice, envVoice)
	setIf(&c.Speech.Language, envLanguage)
	setIf(&c.Gate.ForgejoOrigin, envForgejoURL)
	setIf(&c.Gate.CodeTTL, envCodeTTL)
	setIf(&c.Gate.StatusContext, envApprovalContext)
	setIf(&c.PollInterval, envPollInterval)
	if v := os.Getenv(envMaxMerges); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return s, fmt.Errorf("%s must be a positive number", envMaxMerges)
		}
		c.Gate.MaxMergesPerHour = n
	}
	if v := os.Getenv(envApprovalRepos); v != "" {
		c.Gate.Repos = nil
		for _, r := range strings.Split(v, ",") {
			if r = strings.TrimSpace(r); r != "" {
				c.Gate.Repos = append(c.Gate.Repos, r)
			}
		}
	}

	// The bot's credentials.
	var err error
	if v, err := dirFile(envMatrixDir, "user_id", false); err != nil {
		return s, err
	} else if v != "" {
		if c.Matrix.UserID != "" && c.Matrix.UserID != v {
			return s, errors.New("the bot's user ID differs between the config file and the Matrix Secret")
		}
		c.Matrix.UserID = v
	}
	if v, err := dirFile(envMatrixDir, "device_id", false); err != nil {
		return s, err
	} else if v != "" {
		c.Matrix.DeviceID = v
	}
	if s.AccessToken, err = dirFile(envMatrixDir, "access_token", false); err != nil {
		return s, err
	}
	if s.Password, err = dirFile(envMatrixDir, "password", false); err != nil {
		return s, err
	}
	if s.PickleKey, err = dirFile(envMatrixDir, "pickle_key", true); err != nil {
		return s, err
	}

	// The owner: who the bot obeys, his pinned key, the rooms.
	if s.Steve, err = dirFile(envOwnerDir, "mxid", false); err != nil {
		return s, err
	}
	if s.Steve == "" {
		s.Steve = c.Steve
	} else if c.Steve != "" && c.Steve != s.Steve {
		return s, errors.New("Steve's MXID differs between the config file and the owner Secret")
	}
	if s.MasterKey, err = dirFile(envOwnerDir, "master_key", true); err != nil {
		return s, err
	}
	rawRooms, err := dirFile(envOwnerDir, "rooms", false)
	if err != nil {
		return s, err
	}
	if rawRooms != "" {
		owner, err := ParseRooms(rawRooms)
		if err != nil {
			return s, err
		}
		// The config file may add details (target, repo, name) for the
		// owner's rooms, never rooms of its own.
		details := map[string]service.Room{}
		for _, r := range c.Rooms {
			details[r.ID] = r
		}
		for i, r := range owner {
			if d, ok := details[r.ID]; ok {
				owner[i].Target = orDefault(r.Target, d.Target)
				owner[i].Repo = orDefault(r.Repo, d.Repo)
				owner[i].Name = orDefault(d.Name, r.Name)
			}
		}
		c.Rooms = owner
	}
	defaultRepo := os.Getenv(envDefaultRepo)
	if defaultRepo != "" && !approvalgate.ValidRepo(defaultRepo) {
		return s, fmt.Errorf("%s must be OWNER/NAME", envDefaultRepo)
	}

	// Targets: from the file, or Orca's claude and codex agents plus the
	// Archon stub when only the Orca variables are set.
	if len(c.Targets) == 0 && os.Getenv(envOrcaHandoff) != "" {
		c.Targets = map[string]TargetConfig{
			"orca":   {Kind: "orca", Agent: "claude", Aliases: []string{"orka", "claude"}},
			"codex":  {Kind: "orca", Agent: "codex", Aliases: []string{"codecs", "code x", "codec"}},
			"archon": {Kind: "archon", Aliases: []string{"arkon", "archon factory"}},
		}
	}
	for name, t := range c.Targets {
		if t.Kind == "orca" {
			setIf(&t.HandoffURL, envOrcaHandoff)
			setIf(&t.RunRecordsURL, envOrcaRunRecords)
			if t.BriefFooter == "" {
				t.BriefFooter = orDefault(os.Getenv(envOrcaFooter), defaultOrcaFooter)
			}
			c.Targets[name] = t
		}
	}
	firstTarget := ""
	for _, n := range []string{"orca", "codex"} {
		if _, ok := c.Targets[n]; ok {
			firstTarget = n
			break
		}
	}
	for i := range c.Rooms {
		if c.Rooms[i].Target == "" {
			c.Rooms[i].Target = firstTarget
		}
		if c.Rooms[i].Repo == "" {
			c.Rooms[i].Repo = defaultRepo
		}
	}

	if s.ForgejoToken, err = secret(envForgejoToken, false); err != nil {
		return s, err
	}
	if s.OrcaToken, err = secret(envOrcaToken, false); err != nil {
		return s, err
	}
	if s.SpeechKey, err = secret(envSpeechKey, false); err != nil {
		return s, err
	}
	if s.SummarizerKey, err = secret(envSummarizerKey, false); err != nil {
		return s, err
	}
	s.DatabaseURLFile, s.DatabaseCAFile = os.Getenv(envDatabaseURL), os.Getenv(envDatabaseCA)
	if v := os.Getenv(envDatabaseConns); v != "" {
		if s.DatabaseConns, err = strconv.Atoi(v); err != nil || s.DatabaseConns <= 0 {
			return s, fmt.Errorf("%s must be a positive number", envDatabaseConns)
		}
	}
	s.Listen = orDefault(os.Getenv(envListen), ":8080")
	s.Health = orDefault(os.Getenv(envHealth), s.Listen)
	approvals := false
	for _, r := range c.Rooms {
		approvals = approvals || r.Approvals
	}
	s.GateEnabled = approvals && s.ForgejoToken != ""
	return s, s.validate()
}

func (s Settings) validate() error {
	c := s.Config
	switch {
	case s.Steve == "":
		return fmt.Errorf("Steve's MXID is required (%s/mxid)", envOwnerDir)
	case len(c.Rooms) == 0:
		return fmt.Errorf("rooms are required (%s/rooms)", envOwnerDir)
	case c.Matrix.Homeserver == "":
		return fmt.Errorf("%s is required", envHomeserver)
	case c.Speech.BaseURL == "":
		return fmt.Errorf("%s is required", envSpeechURL)
	case len(c.Targets) == 0:
		return fmt.Errorf("no agent targets: set %s or targets in %s", envOrcaHandoff, envConfig)
	case s.DatabaseURLFile == "":
		return fmt.Errorf("%s is required", envDatabaseURL)
	}
	approvals := 0
	for _, r := range c.Rooms {
		if r.Approvals {
			approvals++
		}
	}
	if approvals > 1 {
		return errors.New("only one Approvals room")
	}
	if s.GateEnabled {
		if c.Gate.ForgejoOrigin == "" {
			return fmt.Errorf("the approval gate needs %s", envForgejoURL)
		}
		if len(c.Gate.Repos) == 0 {
			return fmt.Errorf("the approval gate needs %s (OWNER/* or OWNER/NAME, comma-separated)", envApprovalRepos)
		}
	}
	for _, d := range []string{c.PollInterval, c.Matrix.MaxEventAge, c.Gate.CodeTTL} {
		if d != "" {
			if _, err := time.ParseDuration(d); err != nil {
				return fmt.Errorf("bad duration %q", d)
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

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}
