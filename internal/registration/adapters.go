package registration

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

type t3Adapter struct{}

func (t3Adapter) Kind() string { return "t3" }

func (t3Adapter) RegisterPlan(binary, mountPath, title string) (ClientPlan, error) {
	if binary == "" {
		return ClientPlan{}, errors.New("client binary required")
	}
	if mountPath == "" || !strings.HasPrefix(mountPath, "/") {
		return ClientPlan{}, errors.New("absolute mount path required")
	}
	argv := []string{binary, "project", "add", mountPath}
	if title != "" {
		argv = append(argv, "--title", title)
	}
	argv = appendT3BaseDir(argv)
	return ClientPlan{Kind: "t3", Argv: argv}, nil
}

func (t3Adapter) RevokePlan(binary, id string) (ClientPlan, error) {
	if binary == "" {
		return ClientPlan{}, errors.New("client binary required")
	}
	if strings.TrimSpace(id) == "" {
		return ClientPlan{}, errors.New("registration id required")
	}
	return ClientPlan{Kind: "t3", Argv: appendT3BaseDir([]string{binary, "project", "remove", id})}, nil
}

func appendT3BaseDir(argv []string) []string {
	base := strings.TrimSpace(os.Getenv("T3CODE_HOME"))
	if base == "" {
		return argv
	}
	return append(argv, "--base-dir", base)
}

func (t3Adapter) ParseRegister(stdout, stderr string, err error) (string, bool, error) {
	return ParseRegisterSuccess("t3", stdout, stderr, err)
}

func (t3Adapter) ParseRevoke(stdout, stderr string, err error) error {
	return ParseRevokeSuccess("t3", stdout, stderr, err)
}

type orcaAdapter struct{}

func (orcaAdapter) Kind() string { return "orca" }

func (orcaAdapter) RegisterPlan(binary, mountPath, title string) (ClientPlan, error) {
	if binary == "" {
		return ClientPlan{}, errors.New("client binary required")
	}
	if mountPath == "" || !strings.HasPrefix(mountPath, "/") {
		return ClientPlan{}, errors.New("absolute mount path required")
	}
	return ClientPlan{Kind: "orca", Argv: []string{binary, "repo", "add", "--path", mountPath, "--json"}}, nil
}

func (orcaAdapter) RevokePlan(binary, id string) (ClientPlan, error) {
	if binary == "" {
		return ClientPlan{}, errors.New("client binary required")
	}
	if strings.TrimSpace(id) == "" {
		return ClientPlan{}, errors.New("registration id required")
	}
	return ClientPlan{Kind: "orca", Argv: []string{binary, "project", "setup-delete", "--setup", id, "--json"}}, nil
}

func (orcaAdapter) ParseRegister(stdout, stderr string, err error) (string, bool, error) {
	return ParseRegisterSuccess("orca", stdout, stderr, err)
}

func (orcaAdapter) ParseRevoke(stdout, stderr string, err error) error {
	return ParseRevokeSuccess("orca", stdout, stderr, err)
}

// commandAdapter expands argv templates for a named custom control client.
type commandAdapter struct {
	kind     string
	register []string
	revoke   []string
}

func (c commandAdapter) Kind() string { return c.kind }

func (c commandAdapter) RegisterPlan(binary, mountPath, title string) (ClientPlan, error) {
	if binary == "" {
		return ClientPlan{}, errors.New("client binary required")
	}
	if mountPath == "" || !strings.HasPrefix(mountPath, "/") {
		return ClientPlan{}, errors.New("absolute mount path required")
	}
	argv, err := expandTemplate(append([]string{binary}, c.register...), map[string]string{
		"binary": binary,
		"path":   mountPath,
		"title":  title,
		"id":     "",
	})
	if err != nil {
		return ClientPlan{}, err
	}
	return ClientPlan{Kind: c.kind, Argv: argv}, nil
}

func (c commandAdapter) RevokePlan(binary, id string) (ClientPlan, error) {
	if binary == "" {
		return ClientPlan{}, errors.New("client binary required")
	}
	if strings.TrimSpace(id) == "" {
		return ClientPlan{}, errors.New("registration id required")
	}
	argv, err := expandTemplate(append([]string{binary}, c.revoke...), map[string]string{
		"binary": binary,
		"path":   "",
		"title":  "",
		"id":     id,
	})
	if err != nil {
		return ClientPlan{}, err
	}
	return ClientPlan{Kind: c.kind, Argv: argv}, nil
}

func (c commandAdapter) ParseRegister(stdout, stderr string, err error) (string, bool, error) {
	if err != nil {
		return "", false, fmt.Errorf("%s register failed: %w", c.kind, err)
	}
	id := strings.TrimSpace(strings.Split(stdout+"\n", "\n")[0])
	return id, false, nil
}

func (c commandAdapter) ParseRevoke(stdout, stderr string, err error) error {
	if err != nil {
		return fmt.Errorf("%s revoke failed: %w", c.kind, err)
	}
	return nil
}

func expandTemplate(parts []string, vars map[string]string) ([]string, error) {
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if strings.ContainsAny(part, "\n\r;|&`$") {
			return nil, errors.New("client argv rejects shell metacharacters and newlines")
		}
		expanded := part
		for key, value := range vars {
			token := "{" + key + "}"
			if strings.Contains(expanded, token) {
				if value == "" && (key == "path" || key == "id") {
					return nil, fmt.Errorf("template %q requires {%s}", part, key)
				}
				expanded = strings.ReplaceAll(expanded, token, value)
			}
		}
		if strings.Contains(expanded, "{") && strings.Contains(expanded, "}") {
			return nil, fmt.Errorf("unresolved template in %q", part)
		}
		out = append(out, expanded)
	}
	return out, nil
}
