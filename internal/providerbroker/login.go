package providerbroker

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

// Console sign-in. The operator clicks Connect; the broker starts a
// short-lived login pod (no login volume, network only to the provider's
// sign-in hosts), runs the provider's own device or browser login there and
// shows its link and code in the console. When the operator approves, the
// broker reads the new login file from that pod, installs it in the offline
// owner and deletes the pod. Only one sign-in runs at a time.

// LoginPods runs the provider CLI in a login pod. The Kubernetes
// implementation is in login_pods.go; tests use a fake.
type LoginPods interface {
	Start(ctx context.Context, provider string) error
	Run(ctx context.Context, provider string, output io.Writer) error
	Read(ctx context.Context, provider string) ([]byte, error)
	Delete(ctx context.Context, provider string) error
}

// LoginStatus is what the console shows. It never carries credentials; URL
// and Code are the provider's sign-in link and device code.
type LoginStatus struct {
	Provider       string     `json:"provider"`
	State          string     `json:"state"` // idle, starting, waiting, completing, done, failed, canceled
	URL            string     `json:"url,omitempty"`
	Code           string     `json:"code,omitempty"`
	Message        string     `json:"message,omitempty"`
	ReplaceAccount bool       `json:"replaceAccount,omitempty"`
	Started        *time.Time `json:"started,omitempty"`
	Updated        *time.Time `json:"updated,omitempty"`
}

var ErrLoginBusy = errors.New("another provider sign-in is running; finish or cancel it first")

// LoginTimeout bounds one sign-in, including the operator's approval.
const LoginTimeout = 15 * time.Minute

type Logins struct {
	Broker *Broker
	Pods   LoginPods
	Now    func() time.Time

	mu     sync.Mutex
	status map[string]*LoginStatus
	active string
	cancel context.CancelFunc
	done   chan struct{}
}

func (l *Logins) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

func (l *Logins) set(provider string, change func(*LoginStatus)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.status == nil {
		l.status = map[string]*LoginStatus{}
	}
	s := l.status[provider]
	if s == nil {
		s = &LoginStatus{Provider: provider, State: "idle"}
		l.status[provider] = s
	}
	change(s)
	t := l.now()
	s.Updated = &t
}

// Status reports every configured provider.
func (l *Logins) Status() []LoginStatus {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []LoginStatus
	for _, p := range l.Broker.Config.Providers {
		if s := l.status[p]; s != nil {
			out = append(out, *s)
		} else {
			out = append(out, LoginStatus{Provider: p, State: "idle"})
		}
	}
	return out
}

// Start begins a sign-in for provider and returns at once.
func (l *Logins) Start(provider string, replace bool) (LoginStatus, error) {
	if !slices.Contains(l.Broker.Config.Providers, provider) {
		return LoginStatus{}, errors.New("provider is not enabled on this broker")
	}
	l.mu.Lock()
	if l.active != "" {
		l.mu.Unlock()
		return LoginStatus{}, ErrLoginBusy
	}
	ctx, cancel := context.WithTimeout(context.Background(), LoginTimeout)
	l.active, l.cancel, l.done = provider, cancel, make(chan struct{})
	done := l.done
	l.mu.Unlock()
	t := l.now()
	l.set(provider, func(s *LoginStatus) {
		*s = LoginStatus{Provider: provider, State: "starting", ReplaceAccount: replace, Started: &t, Message: "Starting a sign-in pod."}
	})
	go l.run(ctx, provider, replace, done)
	for _, s := range l.Status() {
		if s.Provider == provider {
			return s, nil
		}
	}
	return LoginStatus{}, nil
}

// Cancel stops the running sign-in for provider, if any, and waits for its
// pod to be removed.
func (l *Logins) Cancel(provider string) {
	l.mu.Lock()
	cancel, done := l.cancel, l.done
	running := l.active == provider
	l.mu.Unlock()
	if !running {
		return
	}
	cancel()
	select {
	case <-done:
	case <-time.After(45 * time.Second):
	}
}

func (l *Logins) run(ctx context.Context, provider string, replace bool, done chan struct{}) {
	defer close(done)
	defer func() {
		l.mu.Lock()
		l.active, l.cancel = "", nil
		l.mu.Unlock()
	}()
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		if err := l.Pods.Delete(cleanup, provider); err != nil {
			slog.Warn("provider sign-in pod not removed", "provider", provider)
		}
	}()
	fail := func(message string) {
		state := "failed"
		if errors.Is(ctx.Err(), context.Canceled) {
			state, message = "canceled", "Sign-in canceled."
		} else if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			message = "Sign-in timed out; start it again."
		}
		l.set(provider, func(s *LoginStatus) { s.State, s.Message, s.URL, s.Code = state, message, "", "" })
		slog.Info("provider sign-in ended", "provider", provider, "state", state)
	}
	if err := l.Pods.Start(ctx, provider); err != nil {
		fail("The sign-in pod did not start; check the provider broker's namespace.")
		return
	}
	l.set(provider, func(s *LoginStatus) { s.State, s.Message = "waiting", "Waiting for the provider's sign-in link." })
	parser := &loginOutput{provider: provider, found: func(link, code string) {
		l.set(provider, func(s *LoginStatus) {
			s.URL, s.Code, s.Message = link, code, "Open the link, approve the sign-in, then come back here."
		})
	}}
	if err := l.Pods.Run(ctx, provider, parser); err != nil {
		fail("Sign-in did not complete.")
		return
	}
	l.set(provider, func(s *LoginStatus) { s.State, s.URL, s.Code, s.Message = "completing", "", "", "Saving the login." })
	raw, err := l.Pods.Read(ctx, provider)
	if err != nil {
		fail("The provider did not write a login.")
		return
	}
	if err := l.Broker.Install(ctx, provider, raw, replace); err != nil {
		fail(err.Error())
		return
	}
	l.set(provider, func(s *LoginStatus) {
		s.State, s.Message = "done", "Signed in. Sandboxes that use this provider get access within a minute."
	})
	slog.Info("provider sign-in completed", "provider", provider)
}

var (
	ansi      = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)
	link      = regexp.MustCompile(`https://[^\s"'<>]+`)
	grokCode  = regexp.MustCompile(`^[A-Z0-9]{4}-[A-Z0-9]{4}$`)
	linkHosts = map[string][]string{"grok": {"accounts.x.ai", "auth.x.ai"}, "cursor": {"cursor.com", "www.cursor.com"}}
)

// loginOutput reads the CLI's output line by line and reports the first
// sign-in link on the provider's own host, plus Grok's device code. Anything
// else is ignored, so the console never shows arbitrary text or links.
type loginOutput struct {
	provider  string
	found     func(link, code string)
	buf       bytes.Buffer
	read      int
	link      string
	code      string
	reported  bool
	truncated bool
}

func (o *loginOutput) Write(p []byte) (int, error) {
	o.read += len(p)
	if o.read > 64<<10 {
		o.truncated = true
		return len(p), nil
	}
	o.buf.Write(p)
	for {
		line, err := o.buf.ReadString('\n')
		if err != nil {
			o.buf.Reset()
			o.buf.WriteString(line)
			break
		}
		o.line(strings.TrimSpace(ansi.ReplaceAllString(line, "")))
	}
	return len(p), nil
}

func (o *loginOutput) line(text string) {
	if o.link == "" {
		for _, candidate := range link.FindAllString(text, -1) {
			if u, err := url.Parse(candidate); err == nil && u.Scheme == "https" && slices.Contains(linkHosts[o.provider], u.Hostname()) && u.User == nil {
				o.link = u.String()
				break
			}
		}
	}
	if o.provider == "grok" && o.code == "" && grokCode.MatchString(text) {
		o.code = text
	}
	if o.link != "" && (o.provider != "grok" || o.code != "") && !o.reported {
		o.reported = true
		o.found(o.link, o.code)
	}
}
