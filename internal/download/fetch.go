package download

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// Job is what the controller sends the helper on stdin, one JSON line. The
// helper runs in the worker as the worker's own user (`kubectl exec`
// semantics), so it can write only what the sandbox could already write.
type Job struct {
	// Mode "probe" sends one HEAD for a redirect-chain check; the proxy never
	// returns a body for it. Mode "fetch" downloads the body to Destination.
	Mode           string `json:"mode"`
	URL            string `json:"url"`
	Token          string `json:"token"`
	Proxy          string `json:"proxy"`
	CAFile         string `json:"caFile"`
	TimeoutSeconds int    `json:"timeoutSeconds"`
	Home           string `json:"home,omitempty"`
	Destination    string `json:"destination,omitempty"`
	SHA256         string `json:"sha256,omitempty"`
	Overwrite      bool   `json:"overwrite,omitempty"`
	MaxBytes       int64  `json:"maxBytes,omitempty"`
}

// Event is one JSON line the helper writes to stdout.
type Event struct {
	Event  string `json:"event"` // ready, progress, probe, redirect, done, error
	Status int    `json:"status,omitempty"`
	Bytes  int64  `json:"bytes,omitempty"`
	Total  int64  `json:"total,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
	Path   string `json:"path,omitempty"`
	Code   string `json:"code,omitempty"`
	Error  string `json:"error,omitempty"`
}

// Error codes the console maps to plain messages.
const (
	CodeInvalid    = "invalid"
	CodeOutside    = "outside"
	CodeSymlink    = "symlink"
	CodeNotRegular = "not-regular"
	CodeExists     = "exists"
	CodeTooLarge   = "too-large"
	CodeChecksum   = "checksum"
	CodeStatus     = "status"
	CodeNetwork    = "network"
	CodeTimeout    = "timeout"
	CodeCancelled  = "cancelled"
	CodeTruncated  = "truncated"
	CodeWrite      = "write"
)

type codedError struct {
	code string
	msg  string
}

func (e *codedError) Error() string { return e.msg }
func fail(code, format string, args ...any) error {
	return &codedError{code: code, msg: fmt.Sprintf(format, args...)}
}

// WorkspaceRoot is the only tree the helper writes to. The worker's HOME
// (/workspace/home) is inside it; the root filesystem is read-only anyway.
const WorkspaceRoot = "/workspace"

// RunHelper is `agent-project-controller fetch`. It reads one Job line, then
// treats end of stdin as cancellation: if the controller's exec stream ends,
// the helper stops and removes its temporary file.
func RunHelper(stdin io.Reader, stdout io.Writer) error {
	reader := bufio.NewReaderSize(stdin, 16<<10)
	line, err := reader.ReadSlice('\n')
	if err != nil {
		return emitErr(stdout, fail(CodeInvalid, "job line missing or longer than 16 KiB"))
	}
	var job Job
	d := json.NewDecoder(strings.NewReader(string(line)))
	d.DisallowUnknownFields()
	if err := d.Decode(&job); err != nil {
		return emitErr(stdout, fail(CodeInvalid, "job does not parse"))
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_, _ = io.Copy(io.Discard, reader)
		cancel()
	}()
	client, err := helperClient(job)
	if err != nil {
		return emitErr(stdout, err)
	}
	return emitErr(stdout, Run(ctx, job, WorkspaceRoot, client, stdout))
}

func emitErr(out io.Writer, err error) error {
	if err == nil {
		return nil
	}
	code := CodeWrite
	var c *codedError
	if errors.As(err, &c) {
		code = c.code
	}
	emit(out, Event{Event: "error", Code: code, Error: err.Error()})
	return err
}

func emit(out io.Writer, e Event) {
	b, _ := json.Marshal(e)
	_, _ = out.Write(append(b, '\n'))
}

// helperClient trusts only the project's inspection CA and only the given
// proxy, speaks HTTP/1.1 (the proxy serves grants there) and never follows
// redirects: every hop needs its own grant.
func helperClient(job Job) (*http.Client, error) {
	p, err := url.Parse(job.Proxy)
	if err != nil || p.Scheme != "http" || p.Host == "" || p.User != nil || (p.Path != "" && p.Path != "/") {
		return nil, fail(CodeInvalid, "proxy must be http://host:port")
	}
	pem, err := os.ReadFile(job.CAFile)
	if err != nil {
		return nil, fail(CodeInvalid, "inspection CA unavailable")
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fail(CodeInvalid, "inspection CA unreadable")
	}
	tr := &http.Transport{
		Proxy:                 http.ProxyURL(p),
		TLSClientConfig:       &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12, NextProtos: []string{"http/1.1"}},
		TLSNextProto:          map[string]func(string, *tls.Conn) http.RoundTripper{},
		ForceAttemptHTTP2:     false,
		DisableCompression:    true,
		MaxIdleConns:          1,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 90 * time.Second,
	}
	return &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}

func (j Job) timeout() time.Duration {
	if j.TimeoutSeconds <= 0 {
		return DefaultTimeout
	}
	t := time.Duration(j.TimeoutSeconds) * time.Second
	if t > MaxTimeout {
		return MaxTimeout
	}
	return t
}

// Run executes one job under root with the given client. Tests pass a temp
// root and a client for a local TLS server.
func Run(ctx context.Context, job Job, root string, client *http.Client, out io.Writer) error {
	u, err := ParseURL(job.URL)
	if err != nil {
		return fail(CodeInvalid, "%s", err.Error())
	}
	if !ValidToken(job.Token) {
		return fail(CodeInvalid, "grant token missing")
	}
	ctx, cancel := context.WithTimeout(ctx, job.timeout())
	defer cancel()
	switch job.Mode {
	case "probe":
		return probe(ctx, job, u, client, out)
	case "fetch":
		return fetch(ctx, job, u, root, client, out)
	}
	return fail(CodeInvalid, "mode must be probe or fetch")
}

func request(ctx context.Context, method string, u *url.URL, token string) *http.Request {
	req, _ := http.NewRequestWithContext(ctx, method, Exact(u), nil)
	req.Header.Set(TokenHeader, token)
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept-Encoding", "identity")
	return req
}

func netErr(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fail(CodeTimeout, "download timed out")
	}
	if ctx.Err() != nil {
		return fail(CodeCancelled, "download cancelled")
	}
	// Transport errors can contain the URL; keep the message generic.
	return fail(CodeNetwork, "request through the sandbox proxy failed")
}

func probe(ctx context.Context, job Job, u *url.URL, client *http.Client, out io.Writer) error {
	res, err := client.Do(request(ctx, http.MethodHead, u, job.Token))
	if err != nil {
		return netErr(ctx, err)
	}
	res.Body.Close()
	emit(out, Event{Event: "probe", Status: res.StatusCode})
	return nil
}

func fetch(ctx context.Context, job Job, u *url.URL, root string, client *http.Client, out io.Writer) error {
	if job.MaxBytes <= 0 || job.MaxBytes > MaxMaxBytes {
		return fail(CodeInvalid, "size limit missing")
	}
	want, err := CheckSHA256(job.SHA256)
	if err != nil {
		return fail(CodeInvalid, "%s", err.Error())
	}
	dir, name, shown, err := openDestination(root, job.Home, job.Destination, job.Overwrite)
	if err != nil {
		return err
	}
	defer unix.Close(dir)
	emit(out, Event{Event: "ready", Path: shown})
	res, err := client.Do(request(ctx, http.MethodGet, u, job.Token))
	if err != nil {
		return netErr(ctx, err)
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 && res.StatusCode < 400 {
		emit(out, Event{Event: "redirect", Status: res.StatusCode})
		return nil
	}
	if res.StatusCode != http.StatusOK {
		return &codedError{code: CodeStatus, msg: fmt.Sprintf("server answered %d %s", res.StatusCode, http.StatusText(res.StatusCode))}
	}
	if res.ContentLength > job.MaxBytes {
		return fail(CodeTooLarge, "file is %d bytes; the limit is %d", res.ContentLength, job.MaxBytes)
	}
	tmp, f, err := createTemp(dir, name)
	if err != nil {
		return err
	}
	keep := false
	defer func() {
		if !keep {
			f.Close()
			_ = unix.Unlinkat(dir, tmp, 0)
		}
	}()
	hash := sha256.New()
	var n int64
	buf := make([]byte, 256<<10)
	last := time.Now()
	limited := io.LimitReader(res.Body, job.MaxBytes+1)
	for {
		k, rerr := limited.Read(buf)
		if k > 0 {
			n += int64(k)
			if n > job.MaxBytes {
				return fail(CodeTooLarge, "file exceeds the %d byte limit", job.MaxBytes)
			}
			hash.Write(buf[:k])
			if _, werr := f.Write(buf[:k]); werr != nil {
				return fail(CodeWrite, "writing the file failed (disk full?)")
			}
			if time.Since(last) >= time.Second {
				emit(out, Event{Event: "progress", Bytes: n, Total: res.ContentLength})
				last = time.Now()
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return netErr(ctx, rerr)
		}
	}
	if res.ContentLength >= 0 && n != res.ContentLength {
		return fail(CodeTruncated, "received %d of %d bytes", n, res.ContentLength)
	}
	got := hex.EncodeToString(hash.Sum(nil))
	if want != "" && got != want {
		return fail(CodeChecksum, "SHA-256 mismatch: got %s, expected %s; the file was deleted", got, want)
	}
	if err := f.Sync(); err != nil {
		return fail(CodeWrite, "writing the file failed")
	}
	if err := f.Close(); err != nil {
		return fail(CodeWrite, "writing the file failed")
	}
	keep = true
	if err := publish(dir, tmp, name, job.Overwrite); err != nil {
		_ = unix.Unlinkat(dir, tmp, 0)
		return err
	}
	_ = unix.Fsync(dir)
	emit(out, Event{Event: "done", Bytes: n, Total: n, SHA256: got, Path: shown})
	return nil
}

// openDestination resolves destination under root without following any
// symlink: every directory from root down is opened with O_NOFOLLOW (missing
// ones are created 0755), so a symlink planted anywhere on the path, before or
// during the download, makes the open fail instead of redirecting the write.
// It returns the parent directory descriptor and the final name.
func openDestination(root, home, destination string, overwrite bool) (int, string, string, error) {
	if err := CheckDestination(destination); err != nil {
		return -1, "", "", fail(CodeInvalid, "%s", err.Error())
	}
	if home == "" {
		home = root + "/home"
	}
	full := destination
	if strings.HasPrefix(destination, "~/") {
		full = strings.TrimSuffix(home, "/") + "/" + destination[2:]
	} else {
		full = root + destination[len(WorkspaceRoot):]
	}
	if !strings.HasPrefix(full, root+"/") {
		return -1, "", "", fail(CodeOutside, "destination is outside /workspace and the sandbox home")
	}
	parts := strings.Split(strings.TrimPrefix(full, root+"/"), "/")
	for _, p := range parts {
		if p == "" || p == "." || p == ".." {
			return -1, "", "", fail(CodeInvalid, "destination must not contain empty, . or .. segments")
		}
	}
	fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, "", "", fail(CodeOutside, "workspace volume unavailable")
	}
	for _, p := range parts[:len(parts)-1] {
		next, err := openDir(fd, p)
		if errors.Is(err, unix.ENOENT) {
			if merr := unix.Mkdirat(fd, p, 0o755); merr != nil && !errors.Is(merr, unix.EEXIST) {
				unix.Close(fd)
				return -1, "", "", fail(CodeWrite, "cannot create directory %s", p)
			}
			next, err = openDir(fd, p)
		}
		unix.Close(fd)
		if err != nil {
			if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) {
				return -1, "", "", fail(CodeSymlink, "%s on the destination path is a symlink or not a directory; refused", p)
			}
			return -1, "", "", fail(CodeWrite, "cannot open directory %s", p)
		}
		fd = next
	}
	name := parts[len(parts)-1]
	if err := checkTarget(fd, name, overwrite); err != nil {
		unix.Close(fd)
		return -1, "", "", err
	}
	return fd, name, destination, nil
}

// openDir opens one existing path component without following a symlink and
// confirms it is a directory. It deliberately avoids O_DIRECTORY: gVisor (the
// worker runtime) follows a final symlink when O_DIRECTORY|O_NOFOLLOW are
// combined, while O_NOFOLLOW alone returns ELOOP there as on Linux. O_NONBLOCK
// keeps a planted FIFO from blocking the open.
func openDir(dir int, name string) (int, error) {
	fd, err := unix.Openat(dir, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFDIR {
		unix.Close(fd)
		return -1, unix.ENOTDIR
	}
	return fd, nil
}

// checkTarget refuses an existing symlink, device, FIFO, socket or directory at
// the destination, and an existing regular file unless overwrite is set.
func checkTarget(dir int, name string, overwrite bool) error {
	var st unix.Stat_t
	err := unix.Fstatat(dir, name, &st, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return fail(CodeWrite, "cannot inspect the destination")
	}
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFLNK:
		return fail(CodeSymlink, "the destination is a symlink; refused")
	case unix.S_IFREG:
		if !overwrite {
			return fail(CodeExists, "a file already exists at the destination; tick overwrite to replace it")
		}
		return nil
	}
	return fail(CodeNotRegular, "the destination exists and is not a regular file; refused")
}

func createTemp(dir int, name string) (string, *os.File, error) {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	base := name
	if len(base) > 200 {
		base = base[:200]
	}
	tmp := "." + base + ".apc-download-" + hex.EncodeToString(b)
	fd, err := unix.Openat(dir, tmp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o644)
	if err != nil {
		return "", nil, fail(CodeWrite, "cannot create a temporary file next to the destination")
	}
	return tmp, os.NewFile(uintptr(fd), tmp), nil
}

// publish moves the finished temporary file into place atomically. Without
// overwrite it never replaces anything: a hard link fails if the name exists,
// and renameat2(RENAME_NOREPLACE) is the fallback where links are unsupported.
func publish(dir int, tmp, name string, overwrite bool) error {
	if err := checkTarget(dir, name, overwrite); err != nil {
		return err
	}
	if overwrite {
		if err := unix.Renameat(dir, tmp, dir, name); err != nil {
			return fail(CodeWrite, "cannot move the file into place")
		}
		return nil
	}
	err := unix.Linkat(dir, tmp, dir, name, 0)
	if err == nil {
		_ = unix.Unlinkat(dir, tmp, 0)
		return nil
	}
	if errors.Is(err, unix.EEXIST) {
		return fail(CodeExists, "a file appeared at the destination during the download; kept it")
	}
	err = unix.Renameat2(dir, tmp, dir, name, unix.RENAME_NOREPLACE)
	if errors.Is(err, unix.EEXIST) {
		return fail(CodeExists, "a file appeared at the destination during the download; kept it")
	}
	if err != nil {
		return fail(CodeWrite, "cannot move the file into place")
	}
	return nil
}
