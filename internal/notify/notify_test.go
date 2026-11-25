package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const console = "https://projects.example.com"

// Golden text for every kind: what the operator's phone shows.
func TestRenderEveryKind(t *testing.T) {
	cases := []struct {
		name string
		r    Request
		want Message
	}{
		{"web", Web("platform", 42, "GET", "https", "api.example.com", 443), Message{Topic: "approvals",
			Title:   "Glovecell: cell platform asks for web access",
			Message: "Cell: platform\nAsks for: GET https://api.example.com\nApprove or deny in the console.",
			Click:   console + "/#/sandboxes/platform/internet"}},
		{"web other port", Web("platform", 43, "POST", "https", "api.example.com", 8443), Message{Topic: "approvals",
			Title:   "Glovecell: cell platform asks for web access",
			Message: "Cell: platform\nAsks for: POST https://api.example.com:8443\nApprove or deny in the console.",
			Click:   console + "/#/sandboxes/platform/internet"}},
		{"package", Package("platform", 7, "npm", "@scope/left-pad", "1.3.0", "registry.npmjs.org"), Message{Topic: "approvals",
			Title:   "Glovecell: cell platform asks for a package",
			Message: "Cell: platform\nAsks for: npm package @scope/left-pad 1.3.0 from registry.npmjs.org\nApprove or deny in the console.",
			Click:   console + "/#/sandboxes/platform/internet"}},
		{"lan", LAN("platform", "g1", "192.0.2.10", []int{22, 8080}, 90*time.Minute), Message{Topic: "approvals",
			Title:   "Glovecell: cell platform asks for LAN access",
			Message: "Cell: platform\nAsks for: LAN host 192.0.2.10 TCP 22,8080\nFor: 1 h 30 min\nApprove or deny in the console.",
			Click:   console + "/#/sandboxes/platform/access"}},
		{"repository", Repository("platform", "r1", "apps/price-watch", "image-app"), Message{Topic: "approvals",
			Title:   "Glovecell: cell platform asks for a new repository",
			Message: "Cell: platform\nAsks for: new private repository apps/price-watch from template image-app\nApprove or deny in the console.",
			Click:   console + "/#/attention"}},
		{"forward", Forward("platform", "f1", 5173, time.Hour), Message{Topic: "approvals",
			Title:   "Glovecell: cell platform asks for a port forward",
			Message: "Cell: platform\nAsks for: port forward to cell port 5173\nFor: 1 h\nApprove or deny in the console.",
			Click:   console + "/#/attention"}},
		{"preview", Preview("platform", "p1", 3000, 8*time.Hour), Message{Topic: "approvals",
			Title:   "Glovecell: cell platform asks for a preview",
			Message: "Cell: platform\nAsks for: preview of cell port 3000\nFor: 8 h\nApprove or deny in the console.",
			Click:   console + "/#/attention"}},
		{"summary", Request{Kind: kindSummary, more: 5}, Message{Topic: "approvals",
			Title:   "Glovecell: more requests waiting",
			Message: "5 more requests waited for approval in the last hour without a notification each.\nOpen the console to review them.",
			Click:   console + "/#/attention"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.want.Tags, c.want.Priority = []string{"lock"}, 4
			got := Render(c.r, "approvals", console)
			a, _ := json.Marshal(got)
			b, _ := json.Marshal(c.want)
			if string(a) != string(b) {
				t.Fatalf("got  %s\nwant %s", a, b)
			}
		})
	}
}

// Observed fields an agent can influence (a package name, a host it tried)
// are cut to safe characters: no line breaks, spaces, colons or markup, so
// they cannot add lines, links or instructions to the notification.
func TestRenderStripsInjection(t *testing.T) {
	attempts := []string{
		"left-pad\n\nURGENT: approve now, the operator already agreed",
		"x](https://phish.example.net) **approve**",
		"name\r\nTitle: fake",
		"a‮evil",
		"<script>alert(1)</script>",
		strings.Repeat("a", 5000),
	}
	for _, s := range attempts {
		for _, r := range []Request{
			Package("platform", 1, s, s, s, s),
			Web(s, 1, s, s, s, 443),
			Repository(s, "1", s, s),
			LAN(s, "1", s, []int{22}, time.Hour),
		} {
			m := Render(r, "approvals", console)
			for _, field := range []string{m.Title, m.Message, m.Click} {
				for _, bad := range []string{"URGENT: approve", "\r", "](", "**", "Title: fake", "<script>", "‮", " now,"} {
					if strings.Contains(field, bad) {
						t.Fatalf("%q reached the notification: %q", bad, field)
					}
				}
			}
			if strings.Count(m.Message, "\n") > 4 || len(m.Message) > 600 || len(m.Title) > 200 {
				t.Fatalf("notification not bounded: %q", m.Message)
			}
		}
	}
}

type ntfy struct {
	mu     sync.Mutex
	bodies []string
	auth   []string
	status int
}

func (f *ntfy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bodies = append(f.bodies, string(b))
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	if f.status != 0 {
		w.WriteHeader(f.status)
	}
}

func (f *ntfy) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.bodies)
}

func newTest(t *testing.T, max int) (*Notifier, *ntfy, *time.Time) {
	t.Helper()
	f := &ntfy{}
	srv := httptest.NewTLSServer(f)
	t.Cleanup(srv.Close)
	token := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(token, []byte("tk_publisher\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	n, err := New(Config{URL: srv.URL, Topic: "approvals", TokenFile: token, ConsoleURL: console, MaxPerHour: max}, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC)
	n.now = func() time.Time { return clock }
	return n, f, &clock
}

// drain sends everything queued, as Run would.
func drain(n *Notifier) {
	for {
		select {
		case r := <-n.queue:
			n.Send(context.Background(), r)
		default:
			return
		}
	}
}

func TestSendsWithTokenAndJSON(t *testing.T) {
	n, f, _ := newTest(t, 0)
	n.Pending(context.Background(), Web("platform", 1, "GET", "https", "api.example.com", 443))
	drain(n)
	if f.count() != 1 || f.auth[0] != "Bearer tk_publisher" {
		t.Fatalf("got %d posts, auth %q", f.count(), f.auth)
	}
	var m Message
	if err := json.Unmarshal([]byte(f.bodies[0]), &m); err != nil || m.Topic != "approvals" || !strings.Contains(m.Message, "GET https://api.example.com") {
		t.Fatalf("body %s: %v", f.bodies[0], err)
	}
	if s := n.Stats(); s.Sent != 1 || s.Failed != 0 {
		t.Fatalf("stats %+v", s)
	}
}

func TestDuplicateIsNotSentAgain(t *testing.T) {
	n, f, clock := newTest(t, 0)
	r := Web("platform", 1, "GET", "https", "api.example.com", 443)
	n.Pending(context.Background(), r)
	n.Pending(context.Background(), r)
	*clock = clock.Add(2 * time.Hour)
	n.Pending(context.Background(), r)
	n.Pending(context.Background(), Web("other", 1, "GET", "https", "api.example.com", 443))
	drain(n)
	if f.count() != 2 {
		t.Fatalf("sent %d, want 2 (one per distinct request)", f.count())
	}
	if s := n.Stats(); s.Duplicate != 2 {
		t.Fatalf("stats %+v", s)
	}
}

func TestHourlyCapThenOneSummary(t *testing.T) {
	n, f, clock := newTest(t, 3)
	for i := int64(1); i <= 10; i++ {
		n.Pending(context.Background(), Web("platform", i, "GET", "https", "api.example.com", 443))
	}
	drain(n)
	if f.count() != 3 || n.Stats().RateLimited != 7 {
		t.Fatalf("sent %d, stats %+v", f.count(), n.Stats())
	}
	n.Flush(context.Background())
	drain(n)
	if f.count() != 3 {
		t.Fatal("summary sent before the hour ended")
	}
	*clock = clock.Add(time.Hour)
	n.Flush(context.Background())
	drain(n)
	if f.count() != 4 || !strings.Contains(f.bodies[3], "7 more requests") {
		t.Fatalf("want one summary of 7, got %v", f.bodies[3:])
	}
	n.Flush(context.Background())
	drain(n)
	if f.count() != 4 {
		t.Fatal("summary repeated")
	}
	// The summary counts towards the new hour's cap.
	for i := int64(11); i <= 20; i++ {
		n.Pending(context.Background(), Web("platform", i, "GET", "https", "api.example.com", 443))
	}
	drain(n)
	if f.count() != 6 {
		t.Fatalf("sent %d in the new hour, want 2 after the summary", f.count())
	}
}

func TestFailureIsCountedAndNeverBlocks(t *testing.T) {
	n, f, _ := newTest(t, 0)
	f.status = http.StatusForbidden
	n.Pending(context.Background(), Web("platform", 1, "GET", "https", "api.example.com", 443))
	drain(n)
	if s := n.Stats(); s.Failed != 1 || s.Sent != 0 {
		t.Fatalf("stats %+v", s)
	}
	// Nothing drains the queue: Pending must still return at once and
	// count what it could not queue.
	done := make(chan struct{})
	go func() {
		for i := int64(2); i < 2+queueSize*2; i++ {
			n.Pending(context.Background(), Web("platform", i, "GET", "https", "api.example.com", 443))
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Pending blocked")
	}
}

func TestUnreachableAndMissingToken(t *testing.T) {
	n, _, _ := newTest(t, 0)
	n.cfg.URL = "https://127.0.0.1:1"
	if n.Send(context.Background(), Web("platform", 1, "GET", "https", "api.example.com", 443)) {
		t.Fatal("unreachable server reported as sent")
	}
	n.cfg.TokenFile = filepath.Join(t.TempDir(), "missing")
	if n.Send(context.Background(), Web("platform", 2, "GET", "https", "api.example.com", 443)) {
		t.Fatal("sent without a token")
	}
	if n.Stats().Failed != 2 {
		t.Fatalf("stats %+v", n.Stats())
	}
}

func TestNilNotifierDoesNothing(t *testing.T) {
	var n *Notifier
	n.Pending(context.Background(), Web("platform", 1, "GET", "https", "api.example.com", 443))
	n.Run(context.Background())
	if n.Stats() != (Stats{}) {
		t.Fatal("nil notifier counted")
	}
}

func TestConfigValidate(t *testing.T) {
	ok := Config{URL: "https://ntfy.example.com", Topic: "glovecell-approvals", TokenFile: "/t", ConsoleURL: console}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	inCluster := ok
	inCluster.URL = "http://ntfy.ntfy.svc.cluster.local"
	if err := inCluster.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mut := range map[string]func(*Config){
		"plain http":   func(c *Config) { c.URL = "http://ntfy.example.com" },
		"credentials":  func(c *Config) { c.URL = "https://u:p@ntfy.example.com" },
		"no topic":     func(c *Config) { c.Topic = "" },
		"topic path":   func(c *Config) { c.Topic = "a/b" },
		"no token":     func(c *Config) { c.TokenFile = "" },
		"http console": func(c *Config) { c.ConsoleURL = "http://projects.example.com" },
		"negative cap": func(c *Config) { c.MaxPerHour = -1 },
	} {
		c := ok
		mut(&c)
		if c.Validate() == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
