// Package herdr bridges Glovecell workers that run herdr-core (`hc`) to the
// hub herdr-core (glove-hub, in the cluster). See docs/herdr.md.
package herdr

import (
	"context"
	"errors"
	"fmt"
	"io"

	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/connect"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// DefaultConnect is the remote line herdr-core expects on a machine that
// already has `hc` on PATH (stdin/stdout protocol).
const DefaultConnect = "hc connect"

// WorkerHome and WorkerRuntimeDir are where the worker's `hc server` keeps
// its state and socket (on the workspace PVC). A `kubectl exec` does not
// inherit the worker command's environment, so the connect line sets them.
const (
	WorkerHome       = "/workspace/home"
	WorkerRuntimeDir = "/workspace/herdr"
)

// ViaConfig selects the Ready worker for a ProjectWorkspace name.
type ViaConfig struct {
	ControlNamespace string
	Workspace        string
}

// ConnectCommand is the fixed argv run in the worker. herdr-core 0.1.19
// passes `--via` a probe script (`sh -c 'p=$(command -v herdr-core) ...
// exec "$p" connect'`); the hub's line is never run, so it cannot choose
// another command.
func ConnectCommand() []string {
	return []string{"env", "HOME=" + WorkerHome, "XDG_RUNTIME_DIR=" + WorkerRuntimeDir, "herdr-core", "connect"}
}

// Via runs herdr-core connect inside the workspace worker with a raw
// stdin/stdout pipe (no TTY). Suitable as:
//
//	hc machine add --name <cell> --via 'agent-project-controller herdr-via <workspace>'
func Via(ctx context.Context, c client.Client, config *rest.Config, cfg ViaConfig, in io.Reader, out, errOut io.Writer) error {
	if cfg.ControlNamespace == "" || cfg.Workspace == "" {
		return errors.New("control namespace and workspace required")
	}
	_, pod, err := connect.Pod(ctx, c, cfg.ControlNamespace, cfg.Workspace)
	if err != nil {
		return err
	}
	return connect.Exec(ctx, config, pod.Namespace, pod.Name, ConnectCommand(), in, out, errOut, false)
}

// MachineViaCommand is the --via recipe the hub uses for a workspace.
func MachineViaCommand(workspace string) string {
	return fmt.Sprintf("%s %s", viaPrefix, workspace)
}

const viaPrefix = "agent-project-controller herdr-via"
