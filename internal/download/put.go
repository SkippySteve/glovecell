package download

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// PutOptions is one "Upload into sandbox": the console streams a file from the
// operator's browser through the controller into the helper's stdin, and the
// helper writes it as the sandbox user with the same path rules as fetch.
type PutOptions struct {
	Home        string
	Destination string
	// Size is the exact number of bytes the controller sends. Fewer bytes
	// before stdin ends means the upload was interrupted; nothing is kept.
	Size      int64
	MaxBytes  int64
	SHA256    string
	Overwrite bool
}

// PutArgs is the helper argv after the binary path. The order and the
// --key=value form are fixed so an admission policy can check the shape:
//
//	put --home=H --dest=D --size=N --max-bytes=M --sha256=[hex] --overwrite=true|false
func PutArgs(o PutOptions) []string {
	return []string{"put", "--home=" + o.Home, "--dest=" + o.Destination, "--size=" + strconv.FormatInt(o.Size, 10),
		"--max-bytes=" + strconv.FormatInt(o.MaxBytes, 10), "--sha256=" + o.SHA256, "--overwrite=" + strconv.FormatBool(o.Overwrite)}
}

// ParsePutArgs reads the arguments after "put". Every flag is --key=value and
// given at most once; unknown flags are refused.
func ParsePutArgs(args []string) (PutOptions, error) {
	var o PutOptions
	seen := map[string]bool{}
	for _, a := range args {
		k, v, ok := strings.Cut(a, "=")
		if !ok || !strings.HasPrefix(k, "--") || seen[k] {
			return o, fail(CodeInvalid, "arguments must be --key=value, each at most once")
		}
		seen[k] = true
		var err error
		switch k {
		case "--home":
			o.Home = v
		case "--dest":
			o.Destination = v
		case "--size":
			o.Size, err = strconv.ParseInt(v, 10, 64)
		case "--max-bytes":
			o.MaxBytes, err = strconv.ParseInt(v, 10, 64)
		case "--sha256":
			o.SHA256 = v
		case "--overwrite":
			o.Overwrite, err = strconv.ParseBool(v)
		default:
			return o, fail(CodeInvalid, "unknown argument %s", k)
		}
		if err != nil {
			return o, fail(CodeInvalid, "argument %s does not parse", k)
		}
	}
	if !seen["--dest"] || !seen["--size"] || !seen["--max-bytes"] {
		return o, fail(CodeInvalid, "--dest, --size and --max-bytes are required")
	}
	return o, nil
}

// RunPut is `agent-project-controller put ...` (`sandbox-download put ...` in
// sandbox-control workspaces). It reads exactly --size bytes from stdin.
// SIGHUP, SIGTERM or SIGINT remove the temporary file before exiting. A broken
// stdout (the controller went away) does not kill the helper; it then sees
// stdin end early and cleans up.
func RunPut(args []string, stdin io.Reader, stdout io.Writer) error {
	o, err := ParsePutArgs(args)
	if err != nil {
		return emitErr(stdout, err)
	}
	var mu sync.Mutex
	var cleanup func()
	register := func(f func()) { mu.Lock(); cleanup = f; mu.Unlock() }
	sig := make(chan os.Signal, 4)
	signal.Notify(sig, syscall.SIGPIPE, syscall.SIGHUP, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(sig)
	go func() {
		for s := range sig {
			if s == syscall.SIGPIPE {
				continue
			}
			mu.Lock()
			if cleanup != nil {
				cleanup()
			}
			os.Exit(1)
		}
	}()
	return emitErr(stdout, Put(o, "", stdin, stdout, register))
}

// EmitError writes the helper's final error event, as the helper does.
func EmitError(out io.Writer, err error) error { return emitErr(out, err) }

// Put writes stdin to the destination. base prefixes every path (tests only);
// register, when set, receives a function that removes the temporary file
// (and nil once there is nothing left to remove).
func Put(o PutOptions, base string, stdin io.Reader, out io.Writer, register func(func())) error {
	if o.MaxBytes <= 0 || o.MaxBytes > MaxMaxBytes {
		return fail(CodeInvalid, "size limit missing")
	}
	if o.Size < 0 {
		return fail(CodeInvalid, "size must not be negative")
	}
	if o.Size > o.MaxBytes {
		return fail(CodeTooLarge, "file is %d bytes; the limit is %d", o.Size, o.MaxBytes)
	}
	want, err := CheckSHA256(o.SHA256)
	if err != nil {
		return fail(CodeInvalid, "%s", err.Error())
	}
	dir, name, shown, err := openDestination(base, o.Home, o.Destination, o.Overwrite)
	if err != nil {
		return err
	}
	defer unix.Close(dir)
	tmp, f, err := createTemp(dir, name, ".apc-upload-")
	if err != nil {
		return err
	}
	var once sync.Once
	remove := func() { once.Do(func() { f.Close(); _ = unix.Unlinkat(dir, tmp, 0) }) }
	if register != nil {
		register(remove)
		defer register(nil)
	}
	keep := false
	defer func() {
		if !keep {
			remove()
		}
	}()
	emit(out, Event{Event: "ready", Path: shown, Total: o.Size})
	hash := sha256.New()
	var n int64
	buf := make([]byte, 256<<10)
	last := time.Now()
	src := io.LimitReader(stdin, o.Size)
	for n < o.Size {
		k, rerr := src.Read(buf)
		if k > 0 {
			n += int64(k)
			hash.Write(buf[:k])
			if _, werr := f.Write(buf[:k]); werr != nil {
				return fail(CodeWrite, "writing the file failed (disk full?)")
			}
			if time.Since(last) >= time.Second {
				emit(out, Event{Event: "progress", Bytes: n, Total: o.Size})
				last = time.Now()
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return fail(CodeTruncated, "the upload stream failed after %d of %d bytes; nothing was kept", n, o.Size)
		}
	}
	if n != o.Size {
		return fail(CodeTruncated, "the upload stopped after %d of %d bytes; nothing was kept", n, o.Size)
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
	if register != nil {
		register(func() { _ = unix.Unlinkat(dir, tmp, 0) })
	}
	if err := publish(dir, tmp, name, o.Overwrite); err != nil {
		_ = unix.Unlinkat(dir, tmp, 0)
		return err
	}
	_ = unix.Fsync(dir)
	emit(out, Event{Event: "done", Bytes: n, Total: n, SHA256: got, Path: shown})
	return nil
}
