package hubspoke

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// OrcaRuntime parses orcad's orca_server_ready JSON.
type OrcaRuntime struct{}

func (OrcaRuntime) Kind() string { return "orca" }

type orcaServerReady struct {
	Type    string `json:"type"`
	Pairing *struct {
		Available bool   `json:"available"`
		URL       string `json:"url"`
		Endpoint  string `json:"endpoint"`
		DeviceID  string `json:"deviceId"`
		Scope     string `json:"scope"`
	} `json:"pairing"`
}

// ParseOffer accepts one orca_server_ready JSON object (or a log line that
// starts with one). It returns only a runtime-scope offer.
func (OrcaRuntime) ParseOffer(readyJSON []byte) (Offer, error) {
	line := bytes.TrimSpace(readyJSON)
	if i := bytes.IndexByte(line, '{'); i >= 0 {
		line = line[i:]
	}
	var ready orcaServerReady
	if err := json.Unmarshal(line, &ready); err != nil {
		return Offer{}, fmt.Errorf("orca ready JSON: %w", err)
	}
	if ready.Type != "" && ready.Type != "orca_server_ready" {
		return Offer{}, fmt.Errorf("unexpected ready type %q", ready.Type)
	}
	if ready.Pairing == nil || !ready.Pairing.Available || ready.Pairing.URL == "" {
		return Offer{}, errors.New("orca runtime pairing offer unavailable")
	}
	if ready.Pairing.Scope != "" && ready.Pairing.Scope != "runtime" {
		return Offer{}, fmt.Errorf("orca offer scope %q; need runtime", ready.Pairing.Scope)
	}
	return Offer{
		Kind:     "orca",
		URL:      ready.Pairing.URL,
		Endpoint: ready.Pairing.Endpoint,
		DeviceID: ready.Pairing.DeviceID,
		Scope:    "runtime",
	}, nil
}

// OrcaEnroller drives the hub's Orca CLI (desktop or orca-ide shim).
type OrcaEnroller struct {
	Binary string
	Run    Runner
	// ProjectPath is registered inside each spoke after enroll (default /workspace/repo).
	ProjectPath string
}

func (e *OrcaEnroller) Kind() string { return "orca" }

func (e *OrcaEnroller) binary() (string, error) {
	if e.Binary != "" {
		return e.Binary, nil
	}
	if p := strings.TrimSpace(os.Getenv("APC_ORCA_BINARY")); p != "" {
		return p, nil
	}
	if p := strings.TrimSpace(os.Getenv("ORCA_CLI_COMMAND")); p != "" {
		return p, nil
	}
	return "", errors.New("APC_ORCA_BINARY or ORCA_CLI_COMMAND required")
}

func (e *OrcaEnroller) run(ctx context.Context, argv []string) (string, error) {
	if e.Run != nil {
		return e.Run(ctx, argv)
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	out := stdout.String()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(out)
		}
		return out, fmt.Errorf("%v: %s", err, msg)
	}
	return out, nil
}

type orcaJSON struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func decodeOrca(stdout string) (json.RawMessage, error) {
	var wrap orcaJSON
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &wrap); err != nil {
		return nil, fmt.Errorf("orca JSON: %w", err)
	}
	if wrap.Error != nil {
		return nil, fmt.Errorf("orca %s: %s", wrap.Error.Code, wrap.Error.Message)
	}
	if !wrap.OK && wrap.Result == nil {
		return nil, errors.New("orca command failed")
	}
	return wrap.Result, nil
}

func (e *OrcaEnroller) Enroll(ctx context.Context, name string, offer Offer) (Enrolled, error) {
	if err := ValidateOffer(offer); err != nil {
		return Enrolled{}, err
	}
	bin, err := e.binary()
	if err != nil {
		return Enrolled{}, err
	}
	argv := []string{bin, "environment", "add", "--name", name, "--pairing-code", offer.URL, "--json"}
	stdout, err := e.run(ctx, argv)
	if err != nil {
		// Never include the offer in the returned error.
		return Enrolled{}, errors.New("orca environment add failed")
	}
	raw, err := decodeOrca(stdout)
	if err != nil {
		return Enrolled{}, errors.New("orca environment add failed")
	}
	var env struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		// Some builds nest under "environment".
		var nested struct {
			Environment struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"environment"`
		}
		if err2 := json.Unmarshal(raw, &nested); err2 != nil || nested.Environment.ID == "" {
			// Fall back to list lookup by name.
			list, listErr := e.List(ctx)
			if listErr != nil {
				return Enrolled{}, errors.New("orca environment add returned no id")
			}
			for _, item := range list {
				if item.Name == name {
					return item, nil
				}
			}
			return Enrolled{}, errors.New("orca environment add returned no id")
		}
		env.ID, env.Name = nested.Environment.ID, nested.Environment.Name
	}
	if env.Name == "" {
		env.Name = name
	}
	if env.ID == "" {
		return Enrolled{}, errors.New("orca environment add returned empty id")
	}
	return Enrolled{ID: env.ID, Name: env.Name}, nil
}

func (e *OrcaEnroller) Connect(ctx context.Context, id string) error {
	if strings.TrimSpace(id) == "" {
		return errors.New("environment id required")
	}
	bin, err := e.binary()
	if err != nil {
		return err
	}
	// Desktop IPC expects {selector}; CLI uses --environment.
	argv := []string{bin, "environment", "connect", "--environment", id, "--json"}
	if _, err := e.run(ctx, argv); err != nil {
		return errors.New("orca environment connect failed")
	}
	return nil
}

func (e *OrcaEnroller) RegisterProject(ctx context.Context, id, path string) error {
	if path == "" {
		path = e.ProjectPath
	}
	if path == "" {
		path = "/workspace/repo"
	}
	bin, err := e.binary()
	if err != nil {
		return err
	}
	argv := []string{bin, "repo", "add", "--path", path, "--environment", id, "--json"}
	if _, err := e.run(ctx, argv); err != nil {
		return errors.New("orca repo add failed")
	}
	return nil
}

func (e *OrcaEnroller) Revoke(ctx context.Context, nameOrID string) error {
	if strings.TrimSpace(nameOrID) == "" {
		return errors.New("environment selector required")
	}
	bin, err := e.binary()
	if err != nil {
		return err
	}
	argv := []string{bin, "environment", "rm", "--environment", nameOrID, "--json"}
	if _, err := e.run(ctx, argv); err != nil {
		return errors.New("orca environment rm failed")
	}
	return nil
}

func (e *OrcaEnroller) List(ctx context.Context) ([]Enrolled, error) {
	bin, err := e.binary()
	if err != nil {
		return nil, err
	}
	stdout, err := e.run(ctx, []string{bin, "environment", "list", "--json"})
	if err != nil {
		return nil, errors.New("orca environment list failed")
	}
	raw, err := decodeOrca(stdout)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Environments []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"environments"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("orca environment list: %w", err)
	}
	out := make([]Enrolled, 0, len(payload.Environments))
	for _, env := range payload.Environments {
		out = append(out, Enrolled{ID: env.ID, Name: env.Name})
	}
	return out, nil
}

// FindReadyOffer scans multiline process output for the first usable offer.
func FindReadyOffer(runtime Runtime, logs []byte) (Offer, error) {
	for _, line := range bytes.Split(logs, []byte("\n")) {
		if !bytes.Contains(line, []byte("orca_server_ready")) && !bytes.Contains(line, []byte(`"pairing"`)) {
			continue
		}
		offer, err := runtime.ParseOffer(line)
		if err == nil {
			return offer, nil
		}
	}
	return Offer{}, errors.New("no runtime pairing offer in spoke output")
}

// WaitDeadline is how long Ensure waits for the hub to enroll a spoke.
const WaitDeadline = 2 * time.Minute
