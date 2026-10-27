package download

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func putOpts(dest string, size int64) PutOptions {
	return PutOptions{Destination: dest, Size: size, MaxBytes: 1 << 20}
}

func noUploadTemps(t *testing.T, dir string) {
	t.Helper()
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".apc-upload-") {
			t.Fatalf("temporary file left behind: %s", e.Name())
		}
	}
}

func TestPutArgsRoundTrip(t *testing.T) {
	o := PutOptions{Home: "/home/agent", Destination: "~/uploads/a b.bin", Size: 42, MaxBytes: 1 << 30, SHA256: strings.Repeat("a", 64), Overwrite: true}
	args := PutArgs(o)
	if len(args) != 7 || args[0] != "put" || args[2] != "--dest=~/uploads/a b.bin" || args[6] != "--overwrite=true" {
		t.Fatalf("argv shape changed (the sandbox-control admission policy checks it): %q", args)
	}
	got, err := ParsePutArgs(args[1:])
	if err != nil || !reflect.DeepEqual(got, o) {
		t.Fatalf("%v %+v", err, got)
	}
	for _, bad := range [][]string{
		{"--dest=~/a", "--size=1"},
		{"--dest=~/a", "--size=1", "--max-bytes=9", "--dest=~/b"},
		{"--dest=~/a", "--size=1", "--max-bytes=9", "--exec=sh"},
		{"--dest", "~/a", "--size=1", "--max-bytes=9"},
		{"--dest=~/a", "--size=x", "--max-bytes=9"},
		{"--dest=~/a", "--size=1", "--max-bytes=9", "--overwrite=maybe"},
	} {
		if _, err := ParsePutArgs(bad); code(err) != CodeInvalid {
			t.Fatalf("%q accepted: %v", bad, err)
		}
	}
}

func TestPutWritesAtomicallyAndReportsHash(t *testing.T) {
	root, home := workspace(t)
	body := bytes.Repeat([]byte("upload "), 100000)
	var out bytes.Buffer
	if err := Put(putOpts("~/uploads/file.bin", int64(len(body))), root, bytes.NewReader(body), &out, nil); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(home, "uploads", "file.bin"))
	if err != nil || !bytes.Equal(got, body) {
		t.Fatal("file content differs")
	}
	sum := sha256.Sum256(body)
	ev := events(t, &out)
	last := ev[len(ev)-1]
	if ev[0].Event != "ready" || ev[0].Path != "~/uploads/file.bin" || last.Event != "done" || last.SHA256 != hex.EncodeToString(sum[:]) || last.Bytes != int64(len(body)) {
		t.Fatalf("%+v", ev)
	}
	noUploadTemps(t, filepath.Join(home, "uploads"))
	// Under /workspace too, and an empty file is a valid upload.
	if err := Put(putOpts("/workspace/empty.txt", 0), root, strings.NewReader(""), io.Discard, nil); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(filepath.Join(root, "workspace", "empty.txt")); err != nil || st.Size() != 0 {
		t.Fatal("empty upload missing")
	}
}

func TestPutPathRules(t *testing.T) {
	root, home := workspace(t)
	outside := t.TempDir()
	os.Symlink(outside, filepath.Join(home, "link"))
	os.Mkdir(filepath.Join(home, "dir"), 0o755)
	for dest, want := range map[string]string{
		"~/link/x.bin":    CodeSymlink,
		"~/link":          CodeSymlink,
		"~/dir":           CodeNotRegular,
		"~/../escape.bin": CodeInvalid,
		"/etc/passwd":     CodeInvalid,
		"/workspace":      CodeInvalid,
		"~/a/./b":         CodeInvalid,
		"/workspace//x":   CodeInvalid,
		"~/uploads/":      CodeInvalid,
	} {
		if err := Put(putOpts(dest, 1), root, strings.NewReader("x"), io.Discard, nil); code(err) != want {
			t.Errorf("%s: got %v (%s), want %s", dest, err, code(err), want)
		}
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatal("wrote through a symlink")
	}
	// A home outside /workspace (sandbox-control workspaces) with a symlinked step is refused.
	agentHome := filepath.Join(root, "home", "agent")
	os.MkdirAll(filepath.Join(root, "home"), 0o755)
	os.Symlink(outside, agentHome)
	o := putOpts("~/x.bin", 1)
	o.Home = "/home/agent"
	if err := Put(o, root, strings.NewReader("x"), io.Discard, nil); code(err) != CodeSymlink {
		t.Fatal(err)
	}
}

func TestPutSizeCap(t *testing.T) {
	root, home := workspace(t)
	o := putOpts("~/big.bin", 11)
	o.MaxBytes = 10
	if err := Put(o, root, strings.NewReader(strings.Repeat("x", 11)), io.Discard, nil); code(err) != CodeTooLarge {
		t.Fatal(err)
	}
	o.MaxBytes = MaxMaxBytes + 1
	if err := Put(o, root, strings.NewReader(""), io.Discard, nil); code(err) != CodeInvalid {
		t.Fatal(err)
	}
	// More bytes than declared: only the declared size is read and written.
	o = putOpts("~/exact.bin", 4)
	if err := Put(o, root, strings.NewReader("abcdefgh"), io.Discard, nil); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(home, "exact.bin")); string(b) != "abcd" {
		t.Fatalf("%q", b)
	}
	if _, err := os.Stat(filepath.Join(home, "big.bin")); !os.IsNotExist(err) {
		t.Fatal("over-limit file written")
	}
}

type failingReader struct{ left int }

func (f *failingReader) Read(p []byte) (int, error) {
	if f.left == 0 {
		return 0, errors.New("connection reset")
	}
	n := min(len(p), f.left)
	f.left -= n
	return n, nil
}

func TestPutAbortRemovesTheTemporaryFile(t *testing.T) {
	root, home := workspace(t)
	// Stdin ends early (the controller closed it after a disconnect).
	if err := Put(putOpts("~/short.bin", 100), root, strings.NewReader("only part"), io.Discard, nil); code(err) != CodeTruncated {
		t.Fatal(err)
	}
	// Stdin fails mid-stream.
	if err := Put(putOpts("~/reset.bin", 100), root, &failingReader{left: 10}, io.Discard, nil); code(err) != CodeTruncated {
		t.Fatal(err)
	}
	for _, n := range []string{"short.bin", "reset.bin"} {
		if _, err := os.Stat(filepath.Join(home, n)); !os.IsNotExist(err) {
			t.Fatalf("%s kept after an interrupted upload", n)
		}
	}
	noUploadTemps(t, home)

	// While the upload waits for data the temporary file exists; the cleanup
	// handed to register (what a SIGTERM runs) removes it.
	pr, pw := io.Pipe()
	var cleanup func()
	registered := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		done <- Put(putOpts("~/slow.bin", 100), root, pr, io.Discard, func(f func()) {
			if f != nil && cleanup == nil {
				cleanup = f
				registered <- struct{}{}
			}
		})
	}()
	<-registered
	pw.Write([]byte("0123456789"))
	if entries, _ := os.ReadDir(home); !hasTemp(entries) {
		t.Fatal("expected a temporary file during the upload")
	}
	cleanup()
	noUploadTemps(t, home)
	pw.Close()
	select {
	case err := <-done:
		if code(err) != CodeTruncated && code(err) != CodeWrite {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("helper did not finish")
	}
	noUploadTemps(t, home)
}

func hasTemp(entries []os.DirEntry) bool {
	for _, e := range entries {
		if strings.Contains(e.Name(), ".apc-upload-") {
			return true
		}
	}
	return false
}

func TestPutOverwriteOnlyWhenTicked(t *testing.T) {
	root, home := workspace(t)
	path := filepath.Join(home, "a.bin")
	os.WriteFile(path, []byte("old"), 0o644)
	if err := Put(putOpts("~/a.bin", 3), root, strings.NewReader("new"), io.Discard, nil); code(err) != CodeExists {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != "old" {
		t.Fatal("overwrote without permission")
	}
	o := putOpts("~/a.bin", 3)
	o.Overwrite = true
	if err := Put(o, root, strings.NewReader("new"), io.Discard, nil); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != "new" {
		t.Fatal("overwrite did not replace")
	}
	noUploadTemps(t, home)
}

func TestPutChecksum(t *testing.T) {
	root, home := workspace(t)
	sum := sha256.Sum256([]byte("abc"))
	o := putOpts("~/c.bin", 3)
	o.SHA256 = strings.Repeat("0", 64)
	if err := Put(o, root, strings.NewReader("abc"), io.Discard, nil); code(err) != CodeChecksum {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "c.bin")); !os.IsNotExist(err) {
		t.Fatal("mismatched file kept")
	}
	o.SHA256 = "SHA256:" + strings.ToUpper(hex.EncodeToString(sum[:]))
	if err := Put(o, root, strings.NewReader("abc"), io.Discard, nil); err != nil {
		t.Fatal(err)
	}
	o.SHA256 = "nothex"
	o.Destination = "~/d.bin"
	if err := Put(o, root, strings.NewReader("abc"), io.Discard, nil); code(err) != CodeInvalid {
		t.Fatal(err)
	}
	noUploadTemps(t, home)
}

func TestRunPutReportsBadArguments(t *testing.T) {
	var out bytes.Buffer
	if err := RunPut([]string{"--dest=~/a"}, strings.NewReader(""), &out); err == nil {
		t.Fatal("accepted")
	}
	ev := events(t, &out)
	if len(ev) != 1 || ev[0].Event != "error" || ev[0].Code != CodeInvalid {
		t.Fatalf("%+v", ev)
	}
}
