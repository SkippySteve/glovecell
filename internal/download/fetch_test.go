package download

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

const testToken = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// upstream is a TLS server standing in for the proxy-forwarded destination.
func upstream(t *testing.T, h http.HandlerFunc) *http.Client {
	t.Helper()
	srv := httptest.NewTLSServer(h)
	t.Cleanup(srv.Close)
	addr, _ := url.Parse(srv.URL)
	roots := x509.NewCertPool()
	roots.AddCert(srv.Certificate())
	tr := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: "example.com"}, DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, addr.Host)
	}}
	return &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func workspace(t *testing.T) (string, string) {
	root := t.TempDir()
	home := filepath.Join(root, "workspace", "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	return root, home
}

func events(t *testing.T, out *bytes.Buffer) []Event {
	var list []Event
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if line == "" {
			continue
		}
		var e Event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("bad event line %q", line)
		}
		list = append(list, e)
	}
	return list
}

func code(err error) string {
	var c *codedError
	if errors.As(err, &c) {
		return c.code
	}
	return ""
}

func noTemps(t *testing.T, dir string) {
	t.Helper()
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".apc-download-") {
			t.Fatalf("temporary file left behind: %s", e.Name())
		}
	}
}

func job(dest string) Job {
	return Job{Mode: "fetch", URL: "https://files.example.com/a.bin", Token: testToken, Destination: dest, MaxBytes: 1 << 20}
}

func TestFetchWritesAtomicallyAndReportsHash(t *testing.T) {
	body := []byte("hello sandbox")
	sum := sha256.Sum256(body)
	var gotToken, gotUA, gotMethod string
	client := upstream(t, func(w http.ResponseWriter, r *http.Request) {
		gotToken, gotUA, gotMethod = r.Header.Get(TokenHeader), r.Header.Get("User-Agent"), r.Method
		w.Write(body)
	})
	root, home := workspace(t)
	j := job("~/downloads/a.bin")
	j.SHA256 = hex.EncodeToString(sum[:])
	var out bytes.Buffer
	if err := Run(context.Background(), j, root, client, &out); err != nil {
		t.Fatal(err)
	}
	if gotToken != testToken || gotUA != UserAgent || gotMethod != "GET" {
		t.Fatal("helper request headers", gotToken, gotUA, gotMethod)
	}
	data, err := os.ReadFile(filepath.Join(home, "downloads", "a.bin"))
	if err != nil || !bytes.Equal(data, body) {
		t.Fatal(string(data), err)
	}
	ev := events(t, &out)
	last := ev[len(ev)-1]
	if last.Event != "done" || last.SHA256 != hex.EncodeToString(sum[:]) || last.Bytes != int64(len(body)) || last.Path != "~/downloads/a.bin" {
		t.Fatalf("%+v", last)
	}
	noTemps(t, filepath.Join(home, "downloads"))
}

func TestFetchUnderWorkspacePath(t *testing.T) {
	client := upstream(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("x")) })
	root, _ := workspace(t)
	j := job("/workspace/repo/vendor/a.bin")
	if err := Run(context.Background(), j, root, client, io.Discard); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "workspace", "repo", "vendor", "a.bin")); err != nil {
		t.Fatal(err)
	}
}

func TestFetchRefusesSymlinksAndSpecialFiles(t *testing.T) {
	hits := 0
	client := upstream(t, func(w http.ResponseWriter, r *http.Request) { hits++; w.Write([]byte("x")) })
	root, home := workspace(t)
	outside := t.TempDir()
	// A symlinked directory on the path, a symlink at the destination, a FIFO and a directory.
	if err := os.Symlink(outside, filepath.Join(home, "linked")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "target"), filepath.Join(home, "file-link")); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(home, "fifo"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(home, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	for dest, want := range map[string]string{
		"~/linked/a.bin":   CodeSymlink,
		"~/file-link":      CodeSymlink,
		"~/fifo":           CodeNotRegular,
		"~/dir":            CodeNotRegular,
		"~/../../etc/x":    CodeInvalid,
		"/etc/passwd":      CodeInvalid,
		"/workspace/../x":  CodeInvalid,
		"~/linked/sub/a.b": CodeSymlink,
	} {
		j := job(dest)
		j.Overwrite = true
		err := Run(context.Background(), j, root, client, io.Discard)
		if code(err) != want {
			t.Errorf("%s: got %v (%s), want %s", dest, err, code(err), want)
		}
	}
	if hits != 0 {
		t.Fatalf("refused destinations still fetched %d times", hits)
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatal("wrote through a symlink")
	}
}

func TestFetchHomeItselfSymlinkedIsRefused(t *testing.T) {
	client := upstream(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("x")) })
	root := t.TempDir()
	outside := t.TempDir()
	os.Mkdir(filepath.Join(root, "workspace"), 0o755)
	if err := os.Symlink(outside, filepath.Join(root, "workspace", "home")); err != nil {
		t.Fatal(err)
	}
	j := job("~/a.bin")
	if err := Run(context.Background(), j, root, client, io.Discard); code(err) != CodeSymlink {
		t.Fatal(err)
	}
}

// sandbox-control workspaces keep HOME at /home/agent, outside /workspace.
func TestFetchHomeOutsideWorkspace(t *testing.T) {
	client := upstream(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("x")) })
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "home", "agent"), 0o755)
	os.MkdirAll(filepath.Join(root, "workspace"), 0o755)
	j := job("~/downloads/a.bin")
	j.Home = "/home/agent"
	if err := Run(context.Background(), j, root, client, io.Discard); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "home", "agent", "downloads", "a.bin")); err != nil {
		t.Fatal(err)
	}
	// A missing tree is not created, and a symlinked step towards home is refused.
	j.Home = "/home/other"
	if err := Run(context.Background(), j, root, client, io.Discard); code(err) != CodeOutside {
		t.Fatal(err)
	}
	os.Symlink(filepath.Join(root, "workspace"), filepath.Join(root, "home", "link"))
	j.Home = "/home/link"
	if err := Run(context.Background(), j, root, client, io.Discard); code(err) != CodeSymlink {
		t.Fatal(err)
	}
	for _, bad := range []string{"/", "home/agent", "/home/agent/../root", "/home//agent"} {
		j.Home = bad
		if err := Run(context.Background(), j, root, client, io.Discard); code(err) != CodeInvalid {
			t.Errorf("home %q: %v", bad, err)
		}
	}
}

func TestFetchOverwriteOnlyWhenTicked(t *testing.T) {
	client := upstream(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("new")) })
	root, home := workspace(t)
	path := filepath.Join(home, "a.bin")
	os.WriteFile(path, []byte("old"), 0o644)
	j := job("~/a.bin")
	if err := Run(context.Background(), j, root, client, io.Discard); code(err) != CodeExists {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != "old" {
		t.Fatal("overwrote without permission")
	}
	j.Overwrite = true
	if err := Run(context.Background(), j, root, client, io.Discard); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != "new" {
		t.Fatal("overwrite did not replace")
	}
	noTemps(t, home)
}

func TestPublishNeverReplacesAFileThatAppearsDuringTheDownload(t *testing.T) {
	root, home := workspace(t)
	dir, name, _, err := openDestination(root, "", "~/late.bin", false)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(dir)
	tmp, f, err := createTemp(dir, name)
	if err != nil {
		t.Fatal(err)
	}
	f.Write([]byte("download"))
	f.Close()
	os.WriteFile(filepath.Join(home, "late.bin"), []byte("agent"), 0o644)
	if err := publish(dir, tmp, name, false); code(err) != CodeExists {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(home, "late.bin")); string(b) != "agent" {
		t.Fatal("replaced a file that appeared mid-download")
	}
}

func TestFetchSizeCap(t *testing.T) {
	big := bytes.Repeat([]byte("z"), 2048)
	for name, h := range map[string]http.HandlerFunc{
		"declared": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", strconv.Itoa(len(big)))
			w.Write(big)
		},
		"chunked": func(w http.ResponseWriter, r *http.Request) {
			w.(http.Flusher).Flush()
			for i := 0; i < 8; i++ {
				w.Write(big[:256])
				w.(http.Flusher).Flush()
			}
		},
	} {
		client := upstream(t, h)
		root, home := workspace(t)
		j := job("~/a.bin")
		j.MaxBytes = 1000
		if err := Run(context.Background(), j, root, client, io.Discard); code(err) != CodeTooLarge {
			t.Errorf("%s: %v", name, err)
		}
		if _, err := os.Stat(filepath.Join(home, "a.bin")); !os.IsNotExist(err) {
			t.Errorf("%s: oversize file kept", name)
		}
		noTemps(t, home)
	}
}

func TestFetchChecksumMismatchDeletesTheFile(t *testing.T) {
	client := upstream(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("tampered")) })
	root, home := workspace(t)
	j := job("~/a.bin")
	j.SHA256 = strings.Repeat("0", 64)
	var out bytes.Buffer
	err := Run(context.Background(), j, root, client, &out)
	if code(err) != CodeChecksum || !strings.Contains(err.Error(), "deleted") {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "a.bin")); !os.IsNotExist(err) {
		t.Fatal("mismatched file kept")
	}
	noTemps(t, home)
}

func TestFetchStopsAtRedirectAndProbeSendsHead(t *testing.T) {
	var methods []string
	client := upstream(t, func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		http.Redirect(w, r, "https://elsewhere.example.com/x", http.StatusFound)
	})
	root, home := workspace(t)
	j := job("~/a.bin")
	var out bytes.Buffer
	if err := Run(context.Background(), j, root, client, &out); err != nil {
		t.Fatal(err)
	}
	ev := events(t, &out)
	if ev[len(ev)-1].Event != "redirect" || ev[len(ev)-1].Status != 302 {
		t.Fatalf("%+v", ev)
	}
	if _, err := os.Stat(filepath.Join(home, "a.bin")); !os.IsNotExist(err) {
		t.Fatal("redirect body written")
	}
	out.Reset()
	j.Mode = "probe"
	if err := Run(context.Background(), j, root, client, &out); err != nil {
		t.Fatal(err)
	}
	ev = events(t, &out)
	if len(methods) != 2 || methods[1] != "HEAD" || ev[0].Event != "probe" || ev[0].Status != 302 {
		t.Fatalf("%v %+v", methods, ev)
	}
}

func TestFetchStatusErrorAndTruncation(t *testing.T) {
	client := upstream(t, func(w http.ResponseWriter, r *http.Request) { http.Error(w, "nope", 404) })
	root, home := workspace(t)
	j := job("~/a.bin")
	if err := Run(context.Background(), j, root, client, io.Discard); code(err) != CodeStatus {
		t.Fatal(err)
	}
	client = upstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		w.Write([]byte("short"))
		if hj, ok := w.(http.Hijacker); ok {
			c, _, _ := hj.Hijack()
			c.Close()
		}
	})
	if err := Run(context.Background(), j, root, client, io.Discard); err == nil {
		t.Fatal("truncated body accepted")
	}
	noTemps(t, home)
}

func TestRunRefusesMissingTokenAndBadMode(t *testing.T) {
	root, _ := workspace(t)
	j := job("~/a.bin")
	j.Token = ""
	if err := Run(context.Background(), j, root, http.DefaultClient, io.Discard); code(err) != CodeInvalid {
		t.Fatal(err)
	}
	j.Token = testToken
	j.Mode = "exec"
	if err := Run(context.Background(), j, root, http.DefaultClient, io.Discard); code(err) != CodeInvalid {
		t.Fatal(err)
	}
	j.Mode = "fetch"
	j.URL = "http://files.example.com/a"
	if err := Run(context.Background(), j, root, http.DefaultClient, io.Discard); code(err) != CodeInvalid {
		t.Fatal(err)
	}
}

func TestRunHelperRejectsMalformedJobs(t *testing.T) {
	var out bytes.Buffer
	if err := RunHelper(strings.NewReader("{\"mode\":\"fetch\",\"extra\":1}\n"), &out); err == nil || !strings.Contains(out.String(), `"code":"invalid"`) {
		t.Fatal(err, out.String())
	}
	out.Reset()
	if err := RunHelper(strings.NewReader("no newline"), &out); err == nil {
		t.Fatal("job without newline accepted")
	}
	out.Reset()
	if err := RunHelper(strings.NewReader(`{"mode":"fetch","proxy":"https://proxy:3128","caFile":"/nonexistent"}`+"\n"), &out); err == nil {
		t.Fatal("https proxy accepted")
	}
}
