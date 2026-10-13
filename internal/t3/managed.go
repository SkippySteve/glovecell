package t3

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/connect"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type Managed struct {
	Registry  *Registry
	Config    *rest.Config
	Connector string
	// Exec is an optional transport override for disposable qualification.
	Exec     func(context.Context, *corev1.Pod, []string, io.Reader, io.Writer) error
	mu       sync.Mutex
	ctx      context.Context
	sessions map[string]*managedSession
}
type managedSession struct {
	pod    *corev1.Pod
	cancel context.CancelFunc
	done   chan struct{}
	ready  bool
	err    error
}

func (m *Managed) NeedLeaderElection() bool { return true }
func (m *Managed) Start(ctx context.Context) error {
	m.mu.Lock()
	m.ctx = ctx
	m.sessions = map[string]*managedSession{}
	m.mu.Unlock()
	<-ctx.Done()
	m.mu.Lock()
	var done []chan struct{}
	for _, s := range m.sessions {
		s.cancel()
		done = append(done, s.done)
	}
	m.mu.Unlock()
	for _, d := range done {
		<-d
	}
	return nil
}
func workerNamespace(w *api.ProjectWorkspace) string {
	return "apc-" + strings.ReplaceAll(string(w.UID), "-", "")
}
func instance(w *api.ProjectWorkspace) string { return strings.ReplaceAll(string(w.UID), "-", "") }
func (m *Managed) Ensure(ctx context.Context, w *api.ProjectWorkspace) error {
	if w.Spec.DesiredState != "Running" || !w.DeletionTimestamp.IsZero() {
		return errors.New("T3 registration requires a running project")
	}
	var pods corev1.PodList
	if e := m.Registry.Client.List(ctx, &pods, client.InNamespace(workerNamespace(w)), client.MatchingLabels{api.OwnerLabel: string(w.UID), "projects.agentwork.dev/role": "worker"}); e != nil {
		return e
	}
	if len(pods.Items) != 1 || pods.Items[0].UID == "" || !pods.Items[0].DeletionTimestamp.IsZero() {
		return errors.New("T3 requires exactly one current worker")
	}
	pod := pods.Items[0].DeepCopy()
	healthy := false
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
			healthy = true
		}
	}
	if !healthy || pod.Spec.AutomountServiceAccountToken == nil || *pod.Spec.AutomountServiceAccountToken {
		return errors.New("T3 requires a ready tokenless worker")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ctx == nil || m.ctx.Err() != nil {
		return errors.New("T3 connection manager is starting")
	}
	if s := m.sessions[string(w.UID)]; s != nil {
		if s.pod.UID != pod.UID {
			s.cancel()
			select {
			case <-s.done:
				delete(m.sessions, string(w.UID))
				if s.err != nil {
					return s.err
				}
			default:
				return errors.New("previous T3 connection is stopping")
			}
		}
		if s.pod.UID == pod.UID {
			select {
			case <-s.done:
				delete(m.sessions, string(w.UID))
				if s.err != nil {
					return s.err
				}
			default:
				if s.ready {
					return nil
				}
				if s.err != nil {
					return s.err
				}
				return errors.New("T3 Connect registration and reachability checks pending")
			}
		}
	}
	runCtx, cancel := context.WithCancel(m.ctx)
	s := &managedSession{pod: pod, cancel: cancel, done: make(chan struct{})}
	m.sessions[string(w.UID)] = s
	copyW := w.DeepCopyObject().(*api.ProjectWorkspace)
	go func() {
		e := m.run(runCtx, copyW, s)
		m.mu.Lock()
		s.err = e
		s.ready = false
		close(s.done)
		m.mu.Unlock()
	}()
	return errors.New("T3 Connect registration and reachability checks pending")
}
func (m *Managed) Suspend(ctx context.Context, w *api.ProjectWorkspace) error {
	m.mu.Lock()
	s := m.sessions[string(w.UID)]
	if s != nil {
		s.ready = false
		s.cancel()
	}
	m.mu.Unlock()
	if s != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.done:
		}
		m.mu.Lock()
		if m.sessions[string(w.UID)] == s {
			delete(m.sessions, string(w.UID))
		}
		m.mu.Unlock()
	}
	return nil
}
func (m *Managed) Remove(ctx context.Context, w *api.ProjectWorkspace) error {
	stopErr := m.Suspend(ctx, w)
	return errors.Join(stopErr, m.Registry.Remove(ctx, w))
}
func (m *Managed) workerExec(ctx context.Context, pod *corev1.Pod, args []string, in io.Reader, out io.Writer) error {
	if m.Exec != nil {
		return m.Exec(ctx, pod, args, in, out)
	}
	return connect.Exec(ctx, m.Config, pod.Namespace, pod.Name, args, in, out, io.Discard, false)
}
func (m *Managed) run(ctx context.Context, w *api.ProjectWorkspace, s *managedSession) error {
	serverCtx, serverCancel := context.WithCancel(ctx)
	defer serverCancel()
	go func() {
		_ = m.workerExec(serverCtx, s.pod, []string{"/usr/local/bin/agent-project-controller", "t3-managed", instance(w)}, nil, io.Discard)
	}()
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = m.workerExec(stopCtx, s.pod, []string{"/usr/local/bin/agent-project-controller", "t3-stop", instance(w)}, nil, io.Discard)
	}()
	// Only the trusted connector can reach this listener. Every stream crosses
	// authenticated Kubernetes exec into the selected worker's loopback socket.
	listener, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		return e
	}
	defer listener.Close()
	relayCtx, relayCancel := context.WithCancel(ctx)
	defer relayCancel()
	slots := make(chan struct{}, 32)
	go func() {
		for {
			conn, e := listener.Accept()
			if e != nil {
				return
			}
			select {
			case slots <- struct{}{}:
				go func() {
					defer conn.Close()
					defer func() { <-slots }()
					_ = m.workerExec(relayCtx, s.pod, []string{"/usr/local/bin/agent-project-controller", "t3-relay"}, conn, conn)
				}()
			default:
				conn.Close()
			}
		}
	}()
	origin := "http://" + listener.Addr().String()
	local := &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer local.CloseIdleConnections()
	var descriptor struct {
		ID      string `json:"environmentId"`
		Version string `json:"serverVersion"`
	}
	if e = retry(ctx, 60, func() error {
		return localRequest(ctx, local, origin, "", http.MethodGet, "/.well-known/t3/environment", nil, &descriptor)
	}); e != nil {
		return errors.New("sandbox T3 did not become reachable")
	}
	if descriptor.ID != string(w.UID) {
		return errors.New("sandbox T3 environment identity does not match this project")
	}
	var issued bytes.Buffer
	if e = m.workerExec(ctx, s.pod, []string{Binary, "auth", "session", "issue", "--base-dir", Data, "--ttl", "10m", "--label", "Controller environment registration", "--json"}, nil, &issued); e != nil {
		return errors.New("sandbox T3 administrative authorization unavailable")
	}
	var admin struct {
		Token string `json:"token"`
		ID    string `json:"sessionId"`
	}
	if json.Unmarshal(issued.Bytes(), &admin) != nil || admin.Token == "" || admin.ID == "" {
		return errors.New("invalid sandbox T3 administrative authorization")
	}
	defer func() {
		revokeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = m.workerExec(revokeCtx, s.pod, []string{Binary, "auth", "session", "revoke", admin.ID, "--base-dir", Data}, nil, io.Discard)
	}()
	pinned, expectedAccount := "", ""
	if previous, e := m.Registry.Ledger(ctx, w); e == nil {
		pinned = string(previous.Data["public-key"])
		expectedAccount = string(previous.Data["account-id"])
	} else if !apierrors.IsNotFound(e) {
		return e
	}
	cloud, accountID, e := m.Registry.Cloud.ForAccount(ctx, expectedAccount)
	if e != nil {
		return e
	}
	challenge, e := cloud.Challenge(ctx)
	if e != nil {
		return e
	}
	request := map[string]any{"challenge": challenge, "relayIssuer": m.Registry.Cloud.URL, "endpoint": Endpoint{HTTP: origin, WS: "ws://" + listener.Addr().String(), Kind: "cloudflare_tunnel"}, "origin": map[string]any{"localHttpHost": "127.0.0.1", "localHttpPort": listener.Addr().(*net.TCPAddr).Port}}
	var proof string
	if e = localRequest(ctx, local, origin, admin.Token, http.MethodPost, "/api/connect/link-proof", request, &proof); e != nil {
		return e
	}
	key, e := verifyProof(proof, descriptor.ID, challenge, m.Registry.Cloud.URL, origin, pinned)
	if e != nil {
		return e
	}
	ledger, e := m.Registry.Intent(ctx, w, descriptor.ID, key, accountID)
	if e != nil {
		return e
	}
	link, e := cloud.Link(ctx, proof, descriptor.ID)
	if e != nil {
		return e
	}
	if link.UserID != accountID {
		return errors.New("T3 relay linked a different account")
	}
	// Install only environment-scoped configuration. The connector runs here,
	// outside the sandbox, so worker egress policy remains unchanged.
	config := map[string]any{"relayUrl": m.Registry.Cloud.URL, "relayIssuer": link.Issuer, "cloudUserId": link.UserID, "environmentCredential": link.Credential, "cloudMintPublicKey": link.MintKey, "endpointRuntime": nil}
	var configured struct {
		OK bool `json:"ok"`
	}
	if e = localRequest(ctx, local, origin, admin.Token, http.MethodPost, "/api/connect/relay-config", config, &configured); e != nil {
		return e
	}
	if !configured.OK {
		return errors.New("T3 sandbox relay configuration rejected")
	}
	connectorCtx, connectorCancel := context.WithCancel(ctx)
	defer connectorCancel()
	cmd := exec.CommandContext(connectorCtx, m.Connector, "tunnel", "--no-autoupdate", "--protocol", "http2", "--management-diagnostics=false", "run")
	cmd.Env = append(os.Environ(), "TUNNEL_TOKEN="+link.Runtime.Token)
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if e = cmd.Start(); e != nil {
		return errors.New("T3 tunnel connector failed to start")
	}
	connectorDone := make(chan error, 1)
	go func() { connectorDone <- cmd.Wait(); connectorCancel() }()
	defer func() { _ = cmd.Process.Kill(); <-connectorDone }()
	remote := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	probe := func() error {
		var d struct {
			ID string `json:"environmentId"`
		}
		if e := localRequest(connectorCtx, remote, strings.TrimRight(link.Endpoint.HTTP, "/"), "", http.MethodGet, "/.well-known/t3/environment", nil, &d); e != nil {
			return e
		}
		if d.ID != descriptor.ID {
			return errors.New("remote T3 environment identity mismatch")
		}
		return nil
	}
	// Let the new managed DNS record propagate before the first recursive
	// lookup. Early NXDOMAIN replies otherwise remain cached after provisioning.
	select {
	case <-connectorCtx.Done():
		return errors.New("T3 connector stopped during startup")
	case <-time.After(10 * time.Second):
	}
	observedProbe := func() error {
		e := probe()
		if e != nil {
			m.mu.Lock()
			s.err = fmt.Errorf("T3 remote endpoint not ready: %w", e)
			m.mu.Unlock()
		}
		return e
	}
	if e = retry(connectorCtx, 90, observedProbe); e != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if connectorCtx.Err() != nil {
			return errors.New("T3 tunnel connector stopped during startup")
		}
		return fmt.Errorf("T3 remote endpoint verification failed: %w", e)
	}
	if e = m.Registry.Linked(ctx, ledger, link); e != nil {
		return e
	}
	m.mu.Lock()
	s.ready = true
	s.err = nil
	m.mu.Unlock()
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-connectorCtx.Done():
			return errors.New("T3 managed tunnel stopped")
		case <-ticker.C:
			if e = probe(); e != nil {
				return errors.New("T3 remote endpoint unavailable; reconnecting")
			}
		}
	}
}
func retry(ctx context.Context, attempts int, f func() error) error {
	var e error
	for i := 0; i < attempts; i++ {
		if e = f(); e == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return e
}
func localRequest(ctx context.Context, h *http.Client, origin, token, method, path string, payload, out any) error {
	var data io.Reader
	if payload != nil {
		b, e := json.Marshal(payload)
		if e != nil {
			return e
		}
		data = bytes.NewReader(b)
	}
	req, e := http.NewRequestWithContext(ctx, method, origin+path, data)
	if e != nil {
		return e
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "agent-project-controller/t3-connect")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	response, e := h.Do(req)
	if e != nil {
		return fmt.Errorf("T3 environment request unavailable: %w", e)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("T3 environment rejected request: HTTP %d", response.StatusCode)
	}
	if json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(out) != nil {
		return errors.New("invalid T3 environment response")
	}
	return nil
}
