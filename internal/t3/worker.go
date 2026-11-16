package t3

import (
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
)

//go:embed bridge.py
var bridge string

const Binary = "/opt/t3/t3"
const Data = "/workspace/.apc/t3"
const pidFile = Data + "/server.pid"

func Codex(args []string) error {
	if len(args) == 1 && (args[0] == "--version" || args[0] == "-v") {
		cmd := exec.Command("/opt/codex/bin/codex", "--version")
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		return cmd.Run()
	}
	if len(args) == 0 || args[0] != "app-server" {
		return errors.New("T3 supports operator-managed Codex app-server sessions only")
	}
	return syscall.Exec("/usr/bin/python3", []string{"python3", "-c", bridge}, os.Environ())
}

func ensureWorker() error {
	if os.Getuid() != 1000 {
		return errors.New("non-root project worker required")
	}
	if _, err := os.Stat("/var/run/secrets/kubernetes.io/serviceaccount/token"); !os.IsNotExist(err) {
		return errors.New("tokenless project worker required")
	}
	if _, err := os.Stat("/workspace/repo/.git"); err != nil {
		return errors.New("prepared project required")
	}
	if err := os.MkdirAll(Data+"/userdata", 0700); err != nil {
		return err
	}
	resolved, err := filepath.EvalSymlinks(Data)
	if err != nil || resolved != Data {
		return errors.New("T3 data directory must not use symlinks")
	}
	return nil
}

type processRecord struct {
	PID       int    `json:"pid"`
	Instance  string `json:"instance"`
	StartTime string `json:"startTime,omitempty"`
}

func activeRecord() (*processRecord, error) { return activeRecordAt(pidFile) }
func activeRecordAt(path string) (*processRecord, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var record processRecord
	if json.Unmarshal(raw, &record) != nil || record.PID <= 1 {
		return nil, errors.New("invalid T3 process identity")
	}
	if record.StartTime != "" {
		current, e := processStartTime(record.PID)
		if os.IsNotExist(e) {
			return nil, nil
		}
		if e != nil {
			return nil, e
		}
		if current != record.StartTime {
			return nil, nil
		}
	}
	target, err := os.Readlink("/proc/" + strconv.Itoa(record.PID) + "/exe")
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if target != Binary {
		return nil, errors.New("T3 process identity changed")
	}
	return &record, nil
}

func launchLock(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		return nil, errors.New("T3 launch already in progress")
	}
	return file, nil
}

func Serve(instance string) error { return serve(instance, false) }

// ManagedServe reuses only the controller-owned launch after a controller restart.
func ManagedServe(instance string) error {
	if len(instance) != 32 {
		return errors.New("managed T3 project identity required")
	}
	if _, e := hex.DecodeString(instance); e != nil {
		return errors.New("invalid managed T3 project identity")
	}
	if e := ensureWorker(); e != nil {
		return e
	}
	id := instance[:8] + "-" + instance[8:12] + "-" + instance[12:16] + "-" + instance[16:20] + "-" + instance[20:]
	path := Data + "/userdata/environment-id"
	file, e := os.CreateTemp(Data+"/userdata", ".environment-id-")
	if e != nil {
		return e
	}
	defer os.Remove(file.Name())
	if _, e = file.WriteString(id + "\n"); e != nil {
		file.Close()
		return e
	}
	if e = file.Sync(); e != nil {
		file.Close()
		return e
	}
	if e = file.Close(); e != nil {
		return e
	}
	// Publish a complete ID atomically. An interrupted write never leaves an
	// empty identity that prevents the next controller attempt from recovering.
	if e = os.Link(file.Name(), path); e != nil && !os.IsExist(e) {
		return e
	}
	info, e := os.Lstat(path)
	if e != nil || !info.Mode().IsRegular() {
		return errors.New("managed T3 environment identity must be a regular file")
	}
	raw, e := os.ReadFile(path)
	if e != nil || strings.TrimSpace(string(raw)) != id {
		return errors.New("existing T3 environment is not owned by this managed project")
	}
	return serve(instance, true)
}

func serve(instance string, managed bool) error {
	if len(instance) != 32 {
		return errors.New("T3 launch instance required")
	}
	if err := ensureWorker(); err != nil {
		return err
	}
	startupLock, err := launchLock(Data + "/launch.lock")
	if err != nil {
		return err
	}
	defer startupLock.Close() // close-on-exec releases the lock after the PID record is published.
	if record, err := activeRecord(); err != nil {
		return err
	} else if record != nil {
		if managed && record.Instance == instance {
			return nil
		}
		return errors.New("T3 is already running")
	}
	startTime, err := processStartTime(os.Getpid())
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(Data, ".server-pid-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	rawRecord, _ := json.Marshal(processRecord{PID: os.Getpid(), Instance: instance, StartTime: startTime})
	if _, err = file.Write(rawRecord); err != nil {
		file.Close()
		return err
	}
	if err = file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if err = os.Rename(file.Name(), pidFile); err != nil {
		return err
	}
	raw, _ := json.Marshal(serverSettings(hubAgentsFrom(HubAgentsFile)))
	// Overwrite provider configuration on each launch. Device/session state is retained separately.
	if err := os.WriteFile(Data+"/userdata/settings.json", raw, 0600); err != nil {
		return err
	}
	ensureProject()
	return syscall.Exec(Binary, []string{Binary, "serve", "--host", "127.0.0.1", "--port", "8788", "--base-dir", Data, "--no-browser", "--auto-bootstrap-project-from-cwd", "/workspace/repo"}, os.Environ())
}

func Stop(instance string) error {
	if err := ensureWorker(); err != nil {
		return err
	}
	record, err := activeRecord()
	if err != nil {
		return err
	}
	if record == nil {
		return nil
	}
	if record.Instance != instance {
		return errors.New("T3 launch instance changed; refusing stop")
	}
	return syscall.Kill(record.PID, syscall.SIGTERM)
}

func processStartTime(pid int) (string, error) {
	raw, e := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if e != nil {
		return "", e
	}
	return parseProcessStartTime(string(raw))
}
func parseProcessStartTime(stat string) (string, error) {
	end := strings.LastIndex(stat, ")")
	if end < 0 {
		return "", errors.New("invalid T3 process start time")
	}
	fields := strings.Fields(stat[end+1:])
	if len(fields) < 20 {
		return "", errors.New("invalid T3 process start time")
	}
	if _, e := strconv.ParseUint(fields[19], 10, 64); e != nil {
		return "", errors.New("invalid T3 process start time")
	}
	return fields[19], nil
}

// HubAgentsFile is the session hub's allowlist as the controller mounts it
// into the workspace container of a session-hub cell (docs/session-hub.md).
// A file, not an environment variable: an allowlist change then reaches the
// running worker without changing its pod spec.
const HubAgentsFile = "/run/session-hub-client/agents.json"

// HubAgent is one allowlisted agent of the cell's session hub.
type HubAgent struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
}

var hubAgentID = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// hubAgentsFrom reads the hub allowlist. The file is absent outside
// session-hub cells; a malformed one adds no hub providers.
func hubAgentsFrom(path string) []HubAgent {
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) > 65536 {
		return nil
	}
	var agents []HubAgent
	if json.Unmarshal(raw, &agents) != nil {
		return nil
	}
	out := []HubAgent{}
	for _, a := range agents {
		if hubAgentID.MatchString(a.ID) && len(a.DisplayName) <= 64 {
			out = append(out, a)
		}
	}
	return out
}

// serverSettings is T3's settings.json (T3 0.1 providerInstances format),
// rewritten on every launch. Codex goes through the brokered bridge; Cursor
// and Grok through their launchers; other built-in drivers stay off. In a
// session-hub cell each allowlisted hub agent is one more provider: T3 starts
// the hub connector as an ACP agent, so chats are shared with the cell's other
// apps and T3 can import chats started elsewhere.
func serverSettings(hub []HubAgent) map[string]any {
	instances := map[string]any{
		"codex":  map[string]any{"driver": "codex", "enabled": true, "config": map[string]any{"setupMode": "existing", "binaryPath": "/usr/local/bin/apc-codex", "homePath": "/workspace/.apc/codex"}},
		"cursor": map[string]any{"driver": "cursor", "enabled": true, "config": map[string]any{"binaryPath": "/usr/local/bin/cursor-agent"}},
		"grok":   map[string]any{"driver": "grok", "enabled": true, "config": map[string]any{"binaryPath": "/usr/local/bin/grok"}},
	}
	for _, name := range []string{"claudeAgent", "opencode", "antigravity"} {
		instances[name] = map[string]any{"driver": name, "enabled": false}
	}
	for _, a := range hub {
		name := a.DisplayName
		if name == "" {
			name = a.ID
		}
		instances["hub-"+a.ID] = map[string]any{"driver": "acpRegistry", "enabled": true, "displayName": name + " (shared)",
			"config": map[string]any{"source": "local", "commandPath": "/usr/local/bin/agent-project-controller",
				"commandArgs": []string{"session-hub-acp", "--agent", a.ID}}}
	}
	return map[string]any{"providerInstances": instances}
}

// ensureProject registers /workspace/repo as a T3 project before the server
// starts. T3 0.1 no longer creates it from --auto-bootstrap-project-from-cwd,
// so a new cell would otherwise open with no project. `t3 project add` works
// without a running server; "already exists" on later launches is success.
// Any other failure is logged and T3 starts anyway.
func ensureProject() {
	out, err := exec.Command(Binary, "project", "add", "--base-dir", Data, "/workspace/repo").CombinedOutput()
	if err != nil && !projectExists(out) {
		fmt.Fprintf(os.Stderr, "T3 project registration failed: %v\n", err)
	}
}

func projectExists(output []byte) bool {
	return strings.Contains(string(output), "ProjectAlreadyExistsError")
}
