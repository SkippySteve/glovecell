package registration

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	"github.com/hanwen/go-fuse/v2/fuse"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Options configures the control-host registrar.
// Run this process on the host that serves central T3/Orca (or another
// Adapter), so registered mount paths are local to those tools. Phone and
// browser sessions attach to that control client, not to the registrar.
type Options struct {
	ControlNamespace string
	MountRoot        string
	T3Binary         string
	OrcaBinary       string
	Integrations     []string // empty means t3,orca when binaries exist
	Watch            bool
}

// DefaultMountRoot returns ~/.local/share/agent-project-controller/mounts.
func DefaultMountRoot() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "/tmp/agent-project-controller/mounts"
	}
	return filepath.Join(home, ".local", "share", "agent-project-controller", "mounts")
}

// ResolveOrcaBinary uses the session CLI when configured. Outside a managed
// terminal, never resolve the bare orca command, which can be a screen reader.
func ResolveOrcaBinary() string {
	if configured := os.Getenv("ORCA_CLI_COMMAND"); configured != "" {
		p, _ := exec.LookPath(configured)
		return p
	}
	command := "orca-ide"
	if os.Getenv("ORCA_DEV_REPO_ROOT") != "" {
		command = "orca-dev"
	} else if os.Getenv("ORCA_WORKTREE_ID") != "" {
		command = "orca"
	}
	p, _ := exec.LookPath(command)
	return p
}

type heldMount struct {
	name   string
	uid    string
	server *fuse.Server
	close  func() error
	mount  string
}

// RunRegister mounts Ready workspaces on this host and registers them with the
// configured control clients. When a workspace leaves Ready (including Removed),
// clients are revoked before the mount is dropped. Watch must be true so mounts
// stay available for the control client's mobile/browser sessions.
func RunRegister(ctx context.Context, c client.Client, opts Options, helperFor func(namespace, pod string) (*Remote, func() error, error)) error {
	if opts.ControlNamespace == "" {
		return errors.New("control namespace required")
	}
	if !opts.Watch {
		return errors.New("register requires --watch so mounts stay available to control clients")
	}
	if opts.MountRoot == "" {
		opts.MountRoot = DefaultMountRoot()
	}
	if helperFor == nil {
		return errors.New("helper factory required")
	}
	clients, err := ResolveClients(opts, os.Getenv)
	if err != nil {
		return err
	}
	clientKinds := make([]string, 0, len(clients))
	for _, cl := range clients {
		clientKinds = append(clientKinds, cl.Adapter.Kind())
	}
	if err := os.MkdirAll(opts.MountRoot, 0o700); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	held := map[string]*heldMount{}
	defer func() {
		for _, h := range held {
			_ = revokeClients(context.Background(), c, opts, h.name, clients)
			if h.server != nil {
				_ = h.server.Unmount()
			}
			if h.close != nil {
				_ = h.close()
			}
			slog.Info("unregistered workspace", "workspace", h.name, "mount", h.mount)
		}
	}()

	syncOnce := func() error {
		var list api.ProjectWorkspaceList
		if err := c.List(ctx, &list, client.InNamespace(opts.ControlNamespace)); err != nil {
			return err
		}
		ready := map[string]api.ProjectWorkspace{}
		for _, w := range list.Items {
			if w.Status.Phase == api.PhaseReady && w.Status.Namespace != "" {
				ready[w.Name] = w
			}
		}
		for name, h := range held {
			if _, ok := ready[name]; ok {
				continue
			}
			_ = revokeClients(ctx, c, opts, h.name, clients)
			if h.server != nil {
				_ = h.server.Unmount()
			}
			if h.close != nil {
				_ = h.close()
			}
			delete(held, name)
			slog.Info("dropped non-ready workspace", "workspace", name)
		}
		for name, w := range ready {
			if h, ok := held[name]; ok {
				if !MountedPath(h.mount) {
					slog.Warn("remounting dead mount", "workspace", name, "mount", h.mount)
					if h.server != nil {
						_ = h.server.Unmount()
					}
					if h.close != nil {
						_ = h.close()
					}
					delete(held, name)
				} else {
					if err := registerClients(ctx, c, opts, &w, h.mount, clients); err != nil {
						slog.Error("client registration failed", "workspace", w.Name, "error", err)
					}
					continue
				}
			}
			var pods corev1.PodList
			if err := c.List(ctx, &pods, client.InNamespace(w.Status.Namespace), client.MatchingLabels{api.OwnerLabel: string(w.UID), "projects.agentwork.dev/role": "worker"}); err != nil {
				return err
			}
			if len(pods.Items) != 1 {
				slog.Warn("skip workspace without single worker", "workspace", name, "pods", len(pods.Items))
				continue
			}
			mount := filepath.Join(opts.MountRoot, w.Name)
			if MountedPath(mount) {
				slog.Warn("clearing stale mount", "mount", mount)
				_ = exec.Command("fusermount3", "-u", mount).Run()
				_ = exec.Command("umount", mount).Run()
			}
			remote, closer, err := helperFor(w.Status.Namespace, pods.Items[0].Name)
			if err != nil {
				return fmt.Errorf("%s: %w", w.Name, err)
			}
			server, err := MountRemote(mount, "apc:"+w.Name, "/workspace/repo", remote)
			if err != nil {
				_ = closer()
				return fmt.Errorf("%s mount: %w", w.Name, err)
			}
			held[name] = &heldMount{name: w.Name, uid: string(w.UID), server: server, close: closer, mount: mount}
			if err := waitMountReady(ctx, mount); err != nil {
				return fmt.Errorf("%s mount ready: %w", w.Name, err)
			}
			if err := registerClients(ctx, c, opts, &w, mount, clients); err != nil {
				slog.Error("client registration failed", "workspace", w.Name, "error", err)
				continue
			}
			slog.Info("registered workspace", "workspace", w.Name, "mount", mount, "clients", strings.Join(clientKinds, ","))
		}
		return nil
	}

	if err := syncOnce(); err != nil {
		return err
	}
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := syncOnce(); err != nil {
				slog.Error("register sync failed", "error", err)
			}
		}
	}
}

func waitMountReady(ctx context.Context, mount string) error {
	deadline := time.Now().Add(30 * time.Second)
	head := filepath.Join(mount, ".git", "HEAD")
	var last string
	for {
		if _, err := os.Stat(head); err == nil {
			if _, lookErr := exec.LookPath("git"); lookErr != nil {
				return nil
			}
			// Checkouts are owned by the sandbox user. The registrar runs as
			// the operator, so Git must accept that ownership.
			out, err := exec.CommandContext(ctx, "git", "-c", "safe.directory=*", "-C", mount, "rev-parse", "--is-inside-work-tree").CombinedOutput()
			if err == nil && strings.TrimSpace(string(out)) == "true" {
				return nil
			}
			last = strings.TrimSpace(string(out))
			if len(last) > 200 {
				last = last[:200]
			}
		}
		if time.Now().After(deadline) {
			if last != "" {
				return fmt.Errorf("timed out waiting for mount git checkout: %s", last)
			}
			return errors.New("timed out waiting for mount git checkout")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func registerClients(ctx context.Context, c client.Client, opts Options, w *api.ProjectWorkspace, mount string, clients []ResolvedClient) error {
	ledger, err := loadLedger(ctx, c, opts.ControlNamespace, string(w.UID))
	if err != nil {
		return err
	}
	run := func(argv []string) (string, string, error) {
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		out, err := cmd.CombinedOutput()
		text := string(out)
		return text, text, err
	}
	var first error
	for _, cl := range clients {
		kind := cl.Adapter.Kind()
		var err error
		for attempt := 1; attempt <= 3; attempt++ {
			err = SyncRegisterAdapter(ledger, cl.Adapter, cl.Binary, mount, w.Name, run)
			if err == nil {
				break
			}
			slog.Warn("client register retry", "workspace", w.Name, "client", kind, "attempt", attempt, "error", err)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt) * time.Second):
			}
		}
		_ = storeLedger(ctx, c, opts.ControlNamespace, w, ledger)
		if err != nil && first == nil {
			first = fmt.Errorf("%s %s: %w", w.Name, kind, err)
		}
	}
	if err := storeLedger(ctx, c, opts.ControlNamespace, w, ledger); err != nil {
		return err
	}
	return first
}

func revokeClients(ctx context.Context, c client.Client, opts Options, name string, clients []ResolvedClient) error {
	w := &api.ProjectWorkspace{}
	if err := c.Get(ctx, types.NamespacedName{Namespace: opts.ControlNamespace, Name: name}, w); err != nil {
		return err
	}
	ledger, err := loadLedger(ctx, c, opts.ControlNamespace, string(w.UID))
	if err != nil {
		return err
	}
	run := func(argv []string) (string, string, error) {
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		out, err := cmd.CombinedOutput()
		text := string(out)
		return text, text, err
	}
	for _, cl := range clients {
		_ = SyncRevokeAdapter(ledger, cl.Adapter, cl.Binary, run)
	}
	return storeLedger(ctx, c, opts.ControlNamespace, w, ledger)
}

func loadLedger(ctx context.Context, c client.Client, ns, uid string) (*Ledger, error) {
	cm := &corev1.ConfigMap{}
	err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: ConfigMapName(uid)}, cm)
	if apierrors.IsNotFound(err) {
		return NewLedger(), nil
	}
	if err != nil {
		return nil, err
	}
	return DecodeConfigMapData(cm.Data)
}

func storeLedger(ctx context.Context, c client.Client, ns string, w *api.ProjectWorkspace, ledger *Ledger) error {
	name := ConfigMapName(string(w.UID))
	cm := &corev1.ConfigMap{}
	err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, cm)
	data := EncodeConfigMapData(ledger)
	if apierrors.IsNotFound(err) {
		cm = &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: ns,
				Labels:    map[string]string{api.OwnerLabel: string(w.UID)},
			},
			Data: data,
		}
		return c.Create(ctx, cm)
	}
	if err != nil {
		return err
	}
	if cm.Labels[api.OwnerLabel] != string(w.UID) {
		return errors.New("client ledger ownership mismatch")
	}
	cm.Data = data
	return c.Update(ctx, cm)
}

// ParseIntegrations splits CLIENT_INTEGRATIONS.
func ParseIntegrations(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
