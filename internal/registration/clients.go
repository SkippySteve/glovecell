package registration

import (
	"errors"
	"fmt"
	"strings"
)

// Runner executes an argv without a shell.
type Runner func(argv []string) (stdout string, stderr string, err error)

// ClientPlan is the argv and how to interpret success for one client action.
type ClientPlan struct {
	Kind string
	Argv []string
}

// ParseRegisterSuccess interprets a client register command result.
func ParseRegisterSuccess(kind, stdout, stderr string, err error) (id string, already bool, parseErr error) {
	combined := stdout + "\n" + stderr
	switch kind {
	case "t3":
		if err == nil {
			id = parseT3AddedID(stdout)
			if id == "" {
				return "", false, errors.New("t3 add succeeded without project id")
			}
			return id, false, nil
		}
		if strings.Contains(combined, "ProjectAlreadyExistsError") || strings.Contains(combined, "An active project already exists") {
			return "", true, nil
		}
		return "", false, fmt.Errorf("t3 register failed: %w", err)
	case "orca":
		if err != nil {
			return "", false, fmt.Errorf("orca register failed: %w", err)
		}
		id = parseOrcaRepoID(stdout)
		if id == "" {
			return "", false, errors.New("orca add succeeded without repo id")
		}
		return id, false, nil
	case "command":
		if err != nil {
			return "", false, err
		}
		return "", false, nil
	default:
		return "", false, fmt.Errorf("unknown client kind %q", kind)
	}
}

// ParseRevokeSuccess interprets a client revoke command result.
func ParseRevokeSuccess(kind, stdout, stderr string, err error) error {
	combined := stdout + "\n" + stderr
	switch kind {
	case "t3":
		if err == nil {
			return nil
		}
		if strings.Contains(combined, "ProjectNotFoundError") || strings.Contains(combined, "No active project found") {
			return nil
		}
		return fmt.Errorf("t3 revoke failed: %w", err)
	case "orca":
		if err != nil {
			return fmt.Errorf("orca revoke failed: %w", err)
		}
		return nil
	case "command":
		return err
	default:
		return fmt.Errorf("unknown client kind %q", kind)
	}
}

// RegisterPlan builds the argv for registering a mount path.
func RegisterPlan(kind, binary, mountPath, title string) (ClientPlan, error) {
	kind = strings.TrimSpace(kind)
	if kind == "command" {
		return ClientPlan{}, errors.New("command client rejects shell strings; supply argv via RegisterCommandPlan")
	}
	a, ok := LookupAdapter(kind)
	if !ok {
		return ClientPlan{}, fmt.Errorf("unknown client kind %q", kind)
	}
	return a.RegisterPlan(binary, mountPath, title)
}

// RegisterCommandPlan validates an argv-only command client registration.
func RegisterCommandPlan(argv []string) (ClientPlan, error) {
	if len(argv) == 0 {
		return ClientPlan{}, errors.New("command argv required")
	}
	for _, arg := range argv {
		if strings.ContainsAny(arg, "\n\r") {
			return ClientPlan{}, errors.New("command argv rejects newlines")
		}
	}
	// Reject a single shell string disguised as argv[0] with spaces and metacharacters
	// when callers pass one combined string.
	if len(argv) == 1 && strings.ContainsAny(argv[0], ";|&`$") {
		return ClientPlan{}, errors.New("command client rejects shell strings")
	}
	return ClientPlan{Kind: "command", Argv: append([]string{}, argv...)}, nil
}

// RevokePlan builds the argv for revoking a stored registration id.
func RevokePlan(kind, binary, id string) (ClientPlan, error) {
	kind = strings.TrimSpace(kind)
	if kind == "command" {
		return ClientPlan{}, errors.New("command client rejects shell strings; supply argv via RegisterCommandPlan")
	}
	a, ok := LookupAdapter(kind)
	if !ok {
		return ClientPlan{}, fmt.Errorf("unknown client kind %q", kind)
	}
	return a.RevokePlan(binary, id)
}

func parseT3AddedID(stdout string) string {
	// "Added project <uuid> (<title>) at <path>."
	// A preamble line may precede the confirmation.
	const prefix = "Added project "
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		rest := strings.TrimPrefix(line, prefix)
		id, _, _ := strings.Cut(rest, " ")
		id = strings.Trim(strings.TrimSpace(id), ".")
		if id != "" {
			return id
		}
	}
	return ""
}

func parseOrcaRepoID(stdout string) string {
	trimmed := strings.TrimSpace(stdout)
	// Prefer result.repo.id so the envelope request id is not mistaken for the repo.
	for _, key := range []string{`"repo":{"id":"`, `"repo": { "id": "`, `"repo":{"id": "`, `"repo": { "id":"`} {
		if idx := strings.Index(trimmed, key); idx >= 0 {
			start := idx + len(key)
			end := strings.IndexByte(trimmed[start:], '"')
			if end > 0 {
				id := trimmed[start : start+end]
				if looksLikeUUID(id) {
					return id
				}
			}
		}
	}
	// Fallback: last UUID-looking "id" value (repo id appears after the envelope id).
	var last string
	for _, key := range []string{`"id":"`, `"id": "`} {
		rest := trimmed
		for {
			idx := strings.Index(rest, key)
			if idx < 0 {
				break
			}
			start := idx + len(key)
			end := strings.IndexByte(rest[start:], '"')
			if end < 0 {
				break
			}
			id := rest[start : start+end]
			if looksLikeUUID(id) {
				last = id
			}
			rest = rest[start+end+1:]
		}
	}
	return last
}

func looksLikeUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
				return false
			}
		}
	}
	return true
}
