package registration

import (
	"context"
	"fmt"
	"io"
	"os"

	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/connect"
	"k8s.io/client-go/rest"
)

type kubectlTransport struct {
	stdin  io.WriteCloser
	stdout io.ReadCloser
	cancel context.CancelFunc
	done   <-chan error
}

func (t *kubectlTransport) Read(p []byte) (int, error)  { return t.stdout.Read(p) }
func (t *kubectlTransport) Write(p []byte) (int, error) { return t.stdin.Write(p) }
func (t *kubectlTransport) Close() error {
	_ = t.stdin.Close()
	if t.cancel != nil {
		t.cancel()
	}
	if t.done != nil {
		return <-t.done
	}
	return nil
}

// StartKubectlHelper runs the embedded helper inside the worker via kubectl exec.
func StartKubectlHelper(ctx context.Context, config *rest.Config, namespace, pod string, roots ...string) (*Remote, func() error, error) {
	if len(roots) == 0 {
		roots = DefaultRoots
	}
	argv := []string{"python3", "-c", string(helperPy), "--"}
	argv = append(argv, roots...)
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	cctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		err := connect.Exec(cctx, config, namespace, pod, argv, stdinR, stdoutW, os.Stderr, false)
		_ = stdoutW.Close()
		_ = stdinR.Close()
		done <- err
	}()
	remote := NewRemote(&kubectlTransport{stdin: stdinW, stdout: stdoutR, cancel: cancel, done: done})
	if err := remote.Ping(); err != nil {
		_ = remote.Close()
		return nil, nil, fmt.Errorf("helper ping: %w", err)
	}
	closer := func() error { return remote.Close() }
	return remote, closer, nil
}
