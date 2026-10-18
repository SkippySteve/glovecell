package registration

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
)

//go:embed helper.py
var helperPy []byte

// Transport is a bidirectional stream to a running helper.
type Transport interface {
	io.ReadWriter
	Close() error
}

// Remote is a mutexed client for the jailed helper protocol.
type Remote struct {
	mu   sync.Mutex
	conn Transport
}

// NewRemote wraps an existing helper transport.
func NewRemote(conn Transport) *Remote {
	return &Remote{conn: conn}
}

// StartLocalHelper launches python3 -c <helper> -- <roots...> against a local tree.
func StartLocalHelper(roots ...string) (*Remote, *exec.Cmd, error) {
	if len(roots) == 0 {
		return nil, nil, errors.New("roots required")
	}
	argv := []string{"python3", "-c", string(helperPy), "--"}
	argv = append(argv, roots...)
	cmd := exec.Command(argv[0], argv[1:]...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	return NewRemote(&pipeTransport{stdin: stdin, stdout: stdout, cmd: cmd}), cmd, nil
}

type pipeTransport struct {
	stdin  io.WriteCloser
	stdout io.ReadCloser
	cmd    *exec.Cmd
}

func (p *pipeTransport) Read(b []byte) (int, error)  { return p.stdout.Read(b) }
func (p *pipeTransport) Write(b []byte) (int, error) { return p.stdin.Write(b) }
func (p *pipeTransport) Close() error {
	_ = p.stdin.Close()
	_ = p.stdout.Close()
	if p.cmd != nil {
		return p.cmd.Wait()
	}
	return nil
}

// Call sends one request and returns the response header and payload.
func (r *Remote) Call(req Header, payload []byte) (Header, []byte, error) {
	header, _, body, err := r.CallRaw(req, payload)
	return header, body, err
}

// CallRaw sends one request and returns typed header, raw JSON header, and payload.
func (r *Remote) CallRaw(req Header, payload []byte) (Header, []byte, []byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.conn == nil {
		return Header{}, nil, nil, errors.New("helper closed")
	}
	if err := EncodeFrame(r.conn, req, payload); err != nil {
		return Header{}, nil, nil, err
	}
	header, raw, body, err := DecodeFrameRaw(r.conn)
	if err != nil {
		return Header{}, nil, nil, err
	}
	if !header.OK {
		if header.Error == "" {
			header.Error = "helper failed"
		}
		return header, raw, body, errors.New(header.Error)
	}
	return header, raw, body, nil
}

// Close closes the helper transport.
func (r *Remote) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.conn == nil {
		return nil
	}
	err := r.conn.Close()
	r.conn = nil
	return err
}

// StatAttrs is the JSON-decoded stat response extras.
type StatAttrs struct {
	Mode  uint32 `json:"mode"`
	Size  int64  `json:"size"`
	Mtime int64  `json:"mtime"`
	Atime int64  `json:"atime"`
	UID   uint32 `json:"uid"`
	GID   uint32 `json:"gid"`
	Nlink uint32 `json:"nlink"`
	Ino   uint64 `json:"ino"`
}

// Stat returns attributes for path.
func (r *Remote) Stat(path string) (StatAttrs, error) {
	_, raw, _, err := r.CallRaw(Header{Op: "stat", Path: path}, nil)
	if err != nil {
		return StatAttrs{}, err
	}
	var attrs StatAttrs
	if err := json.Unmarshal(raw, &attrs); err != nil {
		return StatAttrs{}, err
	}
	return attrs, nil
}

// ReadDir lists names under path.
func (r *Remote) ReadDir(path string) ([]string, error) {
	_, raw, _, err := r.CallRaw(Header{Op: "readdir", Path: path}, nil)
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Names []string `json:"names"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, err
	}
	return parsed.Names, nil
}

// Read reads size bytes at offset.
func (r *Remote) Read(path string, offset int64, size int64) ([]byte, error) {
	_, payload, err := r.Call(Header{Op: "read", Path: path, Offset: offset, Size: size}, nil)
	return payload, err
}

// Write writes payload at offset.
func (r *Remote) Write(path string, offset int64, payload []byte) (int, error) {
	header, _, err := r.Call(Header{Op: "write", Path: path, Offset: offset}, payload)
	if err != nil {
		return 0, err
	}
	return int(header.Size), nil
}

// Create creates an exclusive file.
func (r *Remote) Create(path string, mode uint32) error {
	_, _, err := r.Call(Header{Op: "create", Path: path, Mode: mode}, nil)
	return err
}

// Mkdir creates a directory.
func (r *Remote) Mkdir(path string, mode uint32) error {
	_, _, err := r.Call(Header{Op: "mkdir", Path: path, Mode: mode}, nil)
	return err
}

// Unlink removes a file.
func (r *Remote) Unlink(path string) error {
	_, _, err := r.Call(Header{Op: "unlink", Path: path}, nil)
	return err
}

// Rmdir removes a directory.
func (r *Remote) Rmdir(path string) error {
	_, _, err := r.Call(Header{Op: "rmdir", Path: path}, nil)
	return err
}

// Ensure helper.py is non-empty at init for embed sanity in tests.
func helperSource() []byte {
	if len(helperPy) == 0 {
		return nil
	}
	return bytes.Clone(helperPy)
}

// Ping checks the helper is alive.
func (r *Remote) Ping() error {
	_, _, err := r.Call(Header{Op: "ping"}, nil)
	return err
}

// ErrHelper documents a helper protocol failure for callers.
var ErrHelper = fmt.Errorf("helper")
