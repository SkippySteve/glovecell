package sessionhub

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// The launcher protocol: the client sends one JSON line {"agent":"<id>"}; the
// launcher answers one JSON line, {"ok":true} or {"error":"..."}, and on ok
// the connection then carries the agent's standard input and output.
const maxHeader = 256

type request struct {
	Agent string `json:"agent"`
}

type reply struct {
	OK    bool   `json:"ok,omitempty"`
	Error string `json:"error,omitempty"`
}

// Launcher starts allowlisted agents for the hub. It runs in the launcher
// container, so agents get the workspace's mounts, environment and proxy.
type Launcher struct {
	Socket string
	// AgentsFile is re-read on every connection, so an allowlist change in
	// the cell's Secret applies without a restart.
	AgentsFile string
	// Dir is the agents' working directory (empty: the launcher's own).
	Dir string
	// MaxSessions bounds concurrently running agents.
	MaxSessions int
	// Grace is how long an agent may keep running after its client has gone
	// before its process group is killed.
	Grace time.Duration

	mu      sync.Mutex
	running map[int]*exec.Cmd
}

// Serve listens on the socket until ctx ends, then kills every agent.
func (l *Launcher) Serve(ctx context.Context) error {
	if l.MaxSessions <= 0 {
		l.MaxSessions = 8
	}
	if l.Grace <= 0 {
		l.Grace = 5 * time.Second
	}
	l.running = map[int]*exec.Cmd{}
	if err := os.Remove(l.Socket); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	ln, err := net.Listen("unix", l.Socket)
	if err != nil {
		return err
	}
	// The hub runs as another user that shares the pod's fsGroup.
	if err := os.Chmod(l.Socket, 0o660); err != nil {
		ln.Close()
		return err
	}
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	defer l.killAll()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go l.handle(conn.(*net.UnixConn))
	}
}

func (l *Launcher) allowlist() ([]Agent, error) {
	raw, err := os.ReadFile(l.AgentsFile)
	if err != nil {
		return nil, err
	}
	return ParseAgents(raw)
}

func answer(conn net.Conn, r reply) error {
	b, _ := json.Marshal(r)
	_, err := conn.Write(append(b, '\n'))
	return err
}

func (l *Launcher) handle(conn *net.UnixConn) {
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	br := bufio.NewReaderSize(conn, maxHeader+1)
	line, err := br.ReadSlice('\n')
	if err != nil {
		_ = answer(conn, reply{Error: "request line missing or longer than 256 bytes"})
		return
	}
	_ = conn.SetReadDeadline(time.Time{})
	var req request
	if err := json.Unmarshal(line, &req); err != nil {
		_ = answer(conn, reply{Error: "request is not JSON"})
		return
	}
	agents, err := l.allowlist()
	if err != nil {
		log.Printf("session hub: allowlist: %v", err)
		_ = answer(conn, reply{Error: "allowlist unavailable"})
		return
	}
	var agent *Agent
	for i := range agents {
		if agents[i].ID == req.Agent {
			agent = &agents[i]
		}
	}
	if agent == nil {
		_ = answer(conn, reply{Error: fmt.Sprintf("agent %q is not allowlisted in this cell", req.Agent)})
		return
	}
	cmd := exec.Command(agent.Command, agent.Args...)
	cmd.Dir = l.Dir
	cmd.Stdout = conn
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		_ = answer(conn, reply{Error: "cannot start agent"})
		return
	}
	l.mu.Lock()
	if len(l.running) >= l.MaxSessions {
		l.mu.Unlock()
		_ = answer(conn, reply{Error: "too many agents running in this cell"})
		return
	}
	if err := cmd.Start(); err != nil {
		l.mu.Unlock()
		log.Printf("session hub: start %s: %v", agent.ID, err)
		_ = answer(conn, reply{Error: fmt.Sprintf("agent %q failed to start", agent.ID)})
		return
	}
	pid := cmd.Process.Pid
	l.running[pid] = cmd
	l.mu.Unlock()
	log.Printf("session hub: started %s (pid %d)", agent.ID, pid)
	if err := answer(conn, reply{OK: true}); err != nil {
		kill(pid)
	}
	done := make(chan struct{})
	go func() {
		// Client gone or stdin closed: give the agent Grace to exit on its
		// own, then end its whole process group.
		_, _ = io.Copy(stdin, br)
		stdin.Close()
		select {
		case <-done:
		case <-time.After(l.Grace):
			kill(pid)
		}
	}()
	err = cmd.Wait()
	close(done)
	l.mu.Lock()
	delete(l.running, pid)
	l.mu.Unlock()
	log.Printf("session hub: %s (pid %d) exited: %v", agent.ID, pid, err)
}

func kill(pid int) {
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}

func (l *Launcher) killAll() {
	l.mu.Lock()
	defer l.mu.Unlock()
	for pid := range l.running {
		kill(pid)
	}
}

// Connect is the hub side: it asks the launcher for agent and relays stdin
// and stdout until the agent's output ends.
func Connect(socket, agent string, stdin io.Reader, stdout io.Writer) error {
	conn, err := net.DialTimeout("unix", socket, 5*time.Second)
	if err != nil {
		return fmt.Errorf("session hub launcher unavailable: %w", err)
	}
	defer conn.Close()
	uc := conn.(*net.UnixConn)
	b, _ := json.Marshal(request{Agent: agent})
	if _, err := uc.Write(append(b, '\n')); err != nil {
		return err
	}
	br := bufio.NewReader(uc)
	line, err := br.ReadSlice('\n')
	if err != nil {
		return fmt.Errorf("session hub launcher: no answer: %w", err)
	}
	var r reply
	if err := json.Unmarshal(line, &r); err != nil {
		return errors.New("session hub launcher: bad answer")
	}
	if !r.OK {
		return fmt.Errorf("session hub launcher: %s", r.Error)
	}
	go func() {
		_, _ = io.Copy(uc, stdin)
		_ = uc.CloseWrite()
	}()
	_, err = io.Copy(stdout, br)
	return err
}
