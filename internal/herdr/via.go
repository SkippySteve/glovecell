// Package herdr bridges Glovecell workers that run herdr-core (`hc`) to a
// hub herdr-core on Steve's PC or phone path. See docs/herdr.md.
package herdr

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/connect"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// DefaultConnect is the remote line herdr-core expects on a machine that
// already has `hc` on PATH (stdin/stdout protocol).
const DefaultConnect = "hc connect"

// ViaConfig selects the Ready worker for a ProjectWorkspace name.
type ViaConfig struct {
	ControlNamespace string
	Workspace        string
	// RemoteLine is the shell line hc appends (usually `hc connect`). Empty
	// uses DefaultConnect.
	RemoteLine string
}

func (c ViaConfig) line() (string, error) {
	line := strings.TrimSpace(c.RemoteLine)
	if line == "" {
		line = DefaultConnect
	}
	if line == "" || strings.ContainsAny(line, "\x00\r\n") {
		return "", errors.New("herdr via remote line is invalid")
	}
	// Refuse shell metacharacters that would let the hub invent a command
	// beyond connecting to an already-installed hc.
	if strings.ContainsAny(line, ";|&`$<>") || strings.Contains(line, "..") {
		return "", errors.New("herdr via remote line must be a simple hc connect invocation")
	}
	if !strings.HasPrefix(line, "hc ") && line != "hc" && !strings.HasPrefix(line, "herdr-core ") {
		return "", errors.New("herdr via remote line must start with hc or herdr-core")
	}
	return line, nil
}

// Via runs the remote herdr-core connect line inside the workspace worker
// with a raw stdin/stdout pipe (no TTY). Suitable as:
//
//	hc machine add --name <cell> --via 'agent-project-controller herdr-via <workspace>'
func Via(ctx context.Context, c client.Client, config *rest.Config, cfg ViaConfig, in io.Reader, out, errOut io.Writer) error {
	if cfg.ControlNamespace == "" || cfg.Workspace == "" {
		return errors.New("control namespace and workspace required")
	}
	line, err := cfg.line()
	if err != nil {
		return err
	}
	_, pod, err := connect.Pod(ctx, c, cfg.ControlNamespace, cfg.Workspace)
	if err != nil {
		return err
	}
	// sh -c keeps a single argv for hc's appended shell line while still
	// refusing metacharacters above.
	cmd := []string{"/bin/sh", "-c", line}
	return connect.Exec(ctx, config, pod.Namespace, pod.Name, cmd, in, out, errOut, false)
}

// MachineViaCommand is the local --via recipe for operators.
func MachineViaCommand(workspace string) string {
	return fmt.Sprintf("agent-project-controller herdr-via %s", workspace)
}
