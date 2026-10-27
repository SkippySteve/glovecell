package web

import (
	"context"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/network"
)

// TestBrowserDownloads drives the Downloads section in Chromium against the
// fake project sandbox: check a GitHub-style redirect chain, approve it, see
// the chain change (fresh signature), re-approve with one click and finish.
// Then upload a 17 MiB file from "this computer" in three parts and compare the
// sandbox's SHA-256 with the browser's.
func TestBrowserDownloads(t *testing.T) {
	if os.Getenv("APC_BROWSER_TEST") != "true" {
		t.Skip("set APC_BROWSER_TEST=true with node, Playwright and Chromium installed")
	}
	e := downloadFixture(t)
	sig := 0
	e.box.routes[releaseURL] = fakeRoute{status: 302, location: func() string { sig++; return fmt.Sprintf("%s?sig=%d&token=secret-%d", objectURL, sig, sig) }}
	for i := 1; i < 10; i++ {
		e.box.routes[fmt.Sprintf("%s?sig=%d&token=secret-%d", objectURL, i, i)] = fakeRoute{status: 200, body: "tool bytes", headFail: true}
	}
	// Uploads run the real helper (download.Put) against a temporary filesystem.
	upRoot := t.TempDir()
	upHome := filepath.Join(upRoot, "workspace", "home")
	if err := os.MkdirAll(upHome, 0o755); err != nil {
		t.Fatal(err)
	}
	up := &upBox{root: upRoot, home: upHome}
	fetchExec := e.s.Downloads.Exec
	e.s.Downloads.Exec = func(ctx context.Context, ns, pod, container string, cmd []string, stdin io.Reader, stdout, stderr io.Writer) error {
		if len(cmd) > 1 && cmd[1] == "put" {
			return up.exec(ctx, ns, pod, container, cmd, stdin, stdout, stderr)
		}
		return fetchExec(ctx, ns, pod, container, cmd, stdin, stdout, stderr)
	}
	e.s.Downloads.MaxBytes = 32 << 20
	server := httptest.NewUnstartedServer(nil)
	origin := "https://" + server.Listener.Addr().String()
	e.s.Origin = origin
	e.s.Downloads.Settle = 50 * time.Millisecond
	h, err := e.s.Handler()
	if err != nil {
		t.Fatal(err)
	}
	server.Config.Handler = h
	server.StartTLS()
	defer server.Close()
	cmd := exec.Command("node", "../../scripts/qualify-downloads.cjs")
	cmd.Env = append(os.Environ(), "APC_TEST_WEB_ORIGIN="+origin, "APC_TEST_SESSION=operator-session")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("browser qualification failed: %v\n%s", err, out)
	}
	if e.box.files["~/downloads/tool.tgz"] != "tool bytes" {
		t.Fatalf("file not written: %v", e.box.files)
	}
	for _, r := range e.box.worker {
		if strings.Contains(r, "fetch") && !strings.HasPrefix(strings.TrimPrefix(r, "fetch "), "https://github.com/") && !strings.HasPrefix(strings.TrimPrefix(r, "fetch "), objectURL) {
			t.Fatalf("unexpected fetch %s", r)
		}
	}
	if e.policies(t) != 0 {
		t.Fatal("download policy left behind")
	}
	// The upload (two parts of 8 MiB and one of 1 MiB) arrived intact.
	got, err := os.ReadFile(filepath.Join(upHome, "uploads", "report.bin"))
	if err != nil || len(got) != 17<<20 || got[0] != 0 || got[len(got)-1] != byte((17<<20-1)%251) {
		t.Fatalf("uploaded file wrong: %v %d", err, len(got))
	}
	_ = network.DownloadLabel
	t.Log(string(out))
}
