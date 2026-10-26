package web

import (
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/network"
)

// TestBrowserDownloads drives the Downloads section in Chromium against the
// fake project sandbox: check a GitHub-style redirect chain, approve it, see
// the chain change (fresh signature), re-approve with one click and finish.
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
	_ = network.DownloadLabel
	t.Log(string(out))
}
