// Package notify tells the operator, through one ntfy topic, that a request
// is waiting for approval in the console (docs/notifications.md, design
// repository-requests.md section 6).
//
// One notifier serves every request kind. Its text is built here from fields
// the controller observed (the kind, the cell, the requested access, a
// duration and a console link) and never from text an agent supplied, such
// as a request's reason: an agent could otherwise write the operator's
// phone notification. Approval happens only in the console after sign-in;
// a notification carries no action buttons.
//
// Sending never blocks or fails the request: Pending only queues, a worker
// posts, and failures are logged and counted. A repeat of the same pending
// request is not sent again, and at most MaxPerHour notifications go out per
// hour; the rest are summed up in one line when the hour ends.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Kind is the type of request waiting for approval.
type Kind string

// Request kinds. Web and Package are recorded by the controller today; the
// others are the kinds the design adds (LAN grant requests, repository
// requests, port forwards and previews), so their text is fixed and tested
// before they exist.
const (
	KindWeb        Kind = "web"
	KindPackage    Kind = "package"
	KindLAN        Kind = "lan"
	KindRepository Kind = "repository"
	KindForward    Kind = "forward"
	KindPreview    Kind = "preview"
	kindSummary    Kind = "summary"
)

// Request is one pending request as the notifier sends it. Build it with
// the constructors below, which take only observed fields; there is no
// field for agent text.
type Request struct {
	Kind Kind
	// Cell is the project sandbox that asked.
	Cell string
	// ID identifies the pending request within its kind; with Kind and
	// Cell it is the de-duplication key.
	ID string
	// Access is the requested access, in the notifier's own words.
	Access string
	// Duration is how long the access would last; zero for none.
	Duration time.Duration
	// Section is the console page that shows the request (a fragment such
	// as "#/sandboxes/platform/internet").
	Section string
	// more is the count a summary stands for.
	more int
}

// Field limits. Values are cut, never wrapped, so a notification stays one
// screen on a phone.
const (
	maxName   = 63
	maxAccess = 160
)

// Web is a pending exact web request seen by the cell's proxy.
func Web(cell string, id int64, method, scheme, host string, port int) Request {
	access := fmt.Sprintf("%s %s://%s", word(method, 16), word(scheme, 8), hostname(host))
	if port > 0 && !(scheme == "https" && port == 443) && !(scheme == "http" && port == 80) {
		access += ":" + strconv.Itoa(port)
	}
	return Request{Kind: KindWeb, Cell: cell, ID: strconv.FormatInt(id, 10), Access: access, Section: sandboxSection(cell, "internet")}
}

// Package is a pending registry download the proxy recognised.
func Package(cell string, id int64, ecosystem, name, version, host string) Request {
	access := fmt.Sprintf("%s package %s %s from %s", word(ecosystem, 32), packageName(name), packageName(version), hostname(host))
	return Request{Kind: KindPackage, Cell: cell, ID: strconv.FormatInt(id, 10), Access: access, Section: sandboxSection(cell, "internet")}
}

// LAN is a pending request for one home-network host and its TCP ports.
func LAN(cell, id, address string, ports []int, d time.Duration) Request {
	ps := make([]string, 0, len(ports))
	for _, p := range ports {
		ps = append(ps, strconv.Itoa(p))
	}
	access := fmt.Sprintf("LAN host %s TCP %s", hostname(address), strings.Join(ps, ","))
	return Request{Kind: KindLAN, Cell: cell, ID: id, Access: access, Duration: d, Section: sandboxSection(cell, "access")}
}

// Repository is a pending request for a new repository from a template.
func Repository(cell, id, fullName, template string) Request {
	access := fmt.Sprintf("new private repository %s from template %s", packageName(fullName), packageName(template))
	return Request{Kind: KindRepository, Cell: cell, ID: id, Access: access, Section: "#/attention"}
}

// Forward is a pending request to forward one port of the cell to the
// operator's machine.
func Forward(cell, id string, port int, d time.Duration) Request {
	return Request{Kind: KindForward, Cell: cell, ID: id, Access: "port forward to cell port " + strconv.Itoa(port), Duration: d, Section: "#/attention"}
}

// Preview is a pending request to publish one port of the cell as a preview.
func Preview(cell, id string, port int, d time.Duration) Request {
	return Request{Kind: KindPreview, Cell: cell, ID: id, Access: "preview of cell port " + strconv.Itoa(port), Duration: d, Section: "#/attention"}
}

var kindTitle = map[Kind]string{
	KindWeb:        "web access",
	KindPackage:    "a package",
	KindLAN:        "LAN access",
	KindRepository: "a new repository",
	KindForward:    "a port forward",
	KindPreview:    "a preview",
}

// Message is what is posted to ntfy (its JSON publish format).
type Message struct {
	Topic    string   `json:"topic"`
	Title    string   `json:"title"`
	Message  string   `json:"message"`
	Click    string   `json:"click,omitempty"`
	Tags     []string `json:"tags,omitempty"`
	Priority int      `json:"priority,omitempty"`
}

// Render builds the notification for r. consoleURL is the console's origin;
// the link is that origin plus r.Section.
func Render(r Request, topic, consoleURL string) Message {
	cell := cellName(r.Cell)
	m := Message{Topic: topic, Tags: []string{"lock"}, Priority: 4}
	if r.Kind == kindSummary {
		m.Title = "Glovecell: more requests waiting"
		m.Message = fmt.Sprintf("%d more requests waited for approval in the last hour without a notification each.\nOpen the console to review them.", r.more)
		m.Click = link(consoleURL, "#/attention")
		return m
	}
	m.Title = "Glovecell: cell " + cell + " asks for " + kindTitle[r.Kind]
	lines := []string{"Cell: " + cell, "Asks for: " + clip(r.Access, maxAccess)}
	if r.Duration > 0 {
		lines = append(lines, "For: "+duration(r.Duration))
	}
	lines = append(lines, "Approve or deny in the console.")
	m.Message = strings.Join(lines, "\n")
	m.Click = link(consoleURL, r.Section)
	return m
}

// duration writes 90m as "1 h 30 min" and 8h as "8 h".
func duration(d time.Duration) string {
	if d%time.Minute != 0 || d < time.Minute {
		return d.String()
	}
	h, m := int(d/time.Hour), int(d%time.Hour/time.Minute)
	switch {
	case h == 0:
		return fmt.Sprintf("%d min", m)
	case m == 0:
		return fmt.Sprintf("%d h", h)
	}
	return fmt.Sprintf("%d h %d min", h, m)
}

func link(consoleURL, section string) string {
	if consoleURL == "" {
		return ""
	}
	return strings.TrimRight(consoleURL, "/") + "/" + section
}

func sandboxSection(cell, section string) string {
	return "#/sandboxes/" + url.PathEscape(cellName(cell)) + "/" + section
}

// keep returns s with only the bytes ok allows, cut to max.
func keep(s string, max int, ok func(rune) bool) string {
	var b strings.Builder
	for _, c := range s {
		if ok(c) {
			b.WriteRune(c)
		}
	}
	return clip(b.String(), max)
}

func clip(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}

func alnum(c rune) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// cellName keeps a Kubernetes object name.
func cellName(s string) string {
	return keep(s, maxName, func(c rune) bool { return alnum(c) || c == '-' || c == '.' })
}

// hostname keeps a DNS name or an IP address (with IPv6 colons).
func hostname(s string) string {
	return keep(s, 253, func(c rune) bool { return alnum(c) || c == '-' || c == '.' || c == ':' || c == '[' || c == ']' })
}

// word keeps one token such as a method, scheme or ecosystem.
func word(s string, max int) string {
	return keep(s, max, func(c rune) bool { return alnum(c) || c == '-' || c == '_' })
}

// packageName keeps package names, versions and owner/repository names
// ("@scope/name", "1.2.3+build", "apps/price-watch"): no spaces, line
// breaks, colons or markup.
func packageName(s string) string {
	return keep(s, 100, func(c rune) bool {
		return alnum(c) || strings.ContainsRune("-._@/+~", c)
	})
}

// Config configures the notifier.
type Config struct {
	// URL is the ntfy server's base URL, e.g. https://ntfy.example.com.
	URL string
	// Topic is the ntfy topic to publish to.
	Topic string
	// TokenFile holds the ntfy access token (publish permission on Topic).
	// It is read for every send, so a rotated Secret is picked up.
	TokenFile string
	// ConsoleURL is the console origin the notification links to.
	ConsoleURL string
	// MaxPerHour caps notifications per hour; DefaultMaxPerHour when zero.
	MaxPerHour int
}

// DefaultMaxPerHour is the design's cap (repository-requests.md section 6).
const DefaultMaxPerHour = 20

// Validate checks the configuration. The server must be HTTPS, or plain
// HTTP to an in-cluster Service name, since the request carries a token.
func (c Config) Validate() error {
	u, err := url.Parse(c.URL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("notifications: ntfy URL must be an absolute URL without credentials or query")
	}
	inCluster := strings.HasSuffix(u.Hostname(), ".svc") || strings.HasSuffix(u.Hostname(), ".svc.cluster.local")
	if u.Scheme != "https" && !(u.Scheme == "http" && inCluster) {
		return errors.New("notifications: ntfy URL must use https (http only for an in-cluster Service)")
	}
	if c.Topic == "" || len(c.Topic) > 64 || keep(c.Topic, 64, func(r rune) bool { return alnum(r) || r == '-' || r == '_' }) != c.Topic {
		return errors.New("notifications: ntfy topic must be 1-64 letters, digits, - or _")
	}
	if c.TokenFile == "" {
		return errors.New("notifications: an ntfy token file is required")
	}
	if c.ConsoleURL != "" {
		if cu, err := url.Parse(c.ConsoleURL); err != nil || cu.Scheme != "https" || cu.Host == "" {
			return errors.New("notifications: console URL must be an https origin")
		}
	}
	if c.MaxPerHour < 0 {
		return errors.New("notifications: max per hour must not be negative")
	}
	return nil
}

var (
	meter        = otel.Meter("agent-project-controller")
	sentCount, _ = meter.Int64Counter("glovecell.notifications.sent",
		metric.WithDescription("Pending-request notifications delivered to ntfy"))
	failedCount, _ = meter.Int64Counter("glovecell.notifications.failed",
		metric.WithDescription("Pending-request notifications that could not be delivered (the request is still in the console)"))
	skippedCount, _ = meter.Int64Counter("glovecell.notifications.skipped",
		metric.WithDescription("Pending-request notifications not sent: duplicate or over the hourly cap"))
)

// Stats counts outcomes since start, for tests and the log.
type Stats struct {
	Sent, Failed, Duplicate, RateLimited int
}

// Notifier sends pending-request notifications. A nil *Notifier does
// nothing, so callers need no "enabled" check.
type Notifier struct {
	cfg    Config
	client *http.Client
	now    func() time.Time
	queue  chan Request

	mu         sync.Mutex
	seen       map[string]time.Time
	window     time.Time
	inWindow   int
	suppressed int
	stats      Stats
	maxPerHour int
	dedupeFor  time.Duration
}

// queueSize bounds the notifications waiting to be posted; past it Pending
// drops and counts a failure rather than wait.
const queueSize = 64

// New returns a notifier for a valid cfg. Run must be started to send.
func New(cfg Config, client *http.Client) (*Notifier, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	max := cfg.MaxPerHour
	if max == 0 {
		max = DefaultMaxPerHour
	}
	return &Notifier{cfg: cfg, client: client, now: time.Now, queue: make(chan Request, queueSize), seen: map[string]time.Time{}, maxPerHour: max, dedupeFor: 24 * time.Hour}, nil
}

// Pending reports that r has just entered the pending state. It returns at
// once; delivery happens on the Run goroutine.
func (n *Notifier) Pending(ctx context.Context, r Request) {
	if n == nil {
		return
	}
	n.mu.Lock()
	now := n.now()
	summary := n.roll(now)
	key := string(r.Kind) + "/" + r.Cell + "/" + r.ID
	outcome := ""
	switch {
	case !n.seen[key].IsZero() && now.Sub(n.seen[key]) < n.dedupeFor:
		n.stats.Duplicate++
		outcome = "duplicate"
	case n.inWindow >= n.maxPerHour:
		n.seen[key] = now
		n.suppressed++
		n.stats.RateLimited++
		outcome = "rate_limited"
	default:
		n.seen[key] = now
		n.inWindow++
	}
	n.mu.Unlock()
	if summary != nil {
		n.enqueue(ctx, *summary)
	}
	if outcome != "" {
		skippedCount.Add(ctx, 1, metric.WithAttributes(attribute.String("kind", string(r.Kind)), attribute.String("reason", outcome)))
		return
	}
	n.enqueue(ctx, r)
}

// roll starts a new hour when the current one is over and returns the
// summary of what the last hour suppressed, if anything. Callers hold mu.
func (n *Notifier) roll(now time.Time) *Request {
	if !n.window.IsZero() && now.Sub(n.window) < time.Hour {
		return nil
	}
	n.window, n.inWindow = now, 0
	for k, t := range n.seen {
		if now.Sub(t) >= n.dedupeFor {
			delete(n.seen, k)
		}
	}
	if n.suppressed == 0 {
		return nil
	}
	s := &Request{Kind: kindSummary, more: n.suppressed}
	n.suppressed = 0
	n.inWindow = 1
	return s
}

func (n *Notifier) enqueue(ctx context.Context, r Request) {
	select {
	case n.queue <- r:
	default:
		n.count(ctx, r, false, "queue full")
	}
}

// Run posts queued notifications until ctx ends, and sends the summary of
// a capped hour when that hour ends even if nothing new arrives.
func (n *Notifier) Run(ctx context.Context) {
	if n == nil {
		return
	}
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case r := <-n.queue:
			n.Send(ctx, r)
		case <-tick.C:
			n.Flush(ctx)
		}
	}
}

// Flush queues the summary of a finished, capped hour.
func (n *Notifier) Flush(ctx context.Context) {
	n.mu.Lock()
	s := n.roll(n.now())
	n.mu.Unlock()
	if s != nil {
		n.enqueue(ctx, *s)
	}
}

// Send posts one notification now. It reports whether ntfy accepted it.
func (n *Notifier) Send(ctx context.Context, r Request) bool {
	token, err := os.ReadFile(n.cfg.TokenFile)
	if err != nil {
		n.count(ctx, r, false, "token unreadable")
		return false
	}
	body, _ := json.Marshal(Render(r, n.cfg.Topic, n.cfg.ConsoleURL))
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(n.cfg.URL, "/")+"/", bytes.NewReader(body))
	if err != nil {
		n.count(ctx, r, false, "request")
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	resp, err := n.client.Do(req)
	if err != nil {
		n.count(ctx, r, false, "unreachable")
		return false
	}
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		n.count(ctx, r, false, "status "+strconv.Itoa(resp.StatusCode))
		return false
	}
	n.count(ctx, r, true, "")
	return true
}

func (n *Notifier) count(ctx context.Context, r Request, ok bool, why string) {
	attrs := metric.WithAttributes(attribute.String("kind", string(r.Kind)))
	n.mu.Lock()
	if ok {
		n.stats.Sent++
	} else {
		n.stats.Failed++
	}
	n.mu.Unlock()
	if ok {
		sentCount.Add(ctx, 1, attrs)
		return
	}
	failedCount.Add(ctx, 1, attrs)
	// The request stays in the console; only the phone ping is lost. The
	// token and the server's reply are never logged.
	slog.WarnContext(ctx, "pending-request notification not sent", "kind", string(r.Kind), "cell", cellName(r.Cell), "error", why)
}

// Stats returns the outcome counts so far.
func (n *Notifier) Stats() Stats {
	if n == nil {
		return Stats{}
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.stats
}
