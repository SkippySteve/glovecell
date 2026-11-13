package providerbroker

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func clientMerge(before *corev1.Secret) client.Patch { return client.MergeFrom(before) }

func grokLogin(t *testing.T, key string, expires time.Time) []byte {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"https://auth.x.ai::client": map[string]any{"oidc_issuer": "https://auth.x.ai", "oidc_client_id": "client", "key": key, "refresh_token": "refresh-" + key, "expires_at": expires.Format(time.RFC3339Nano), "email": "someone@example.com"}})
	return raw
}

func cursorLogin(t *testing.T, sub string, expires time.Time) []byte {
	t.Helper()
	claims, _ := json.Marshal(map[string]any{"exp": expires.Unix(), "sub": sub})
	raw, _ := json.Marshal(map[string]any{"accessToken": "header." + base64.RawURLEncoding.EncodeToString(claims) + ".signature", "refreshToken": "refresh-" + sub})
	return raw
}

// ownerFake is an in-memory owner pod: it stores what ownerInstall writes and
// serves it to the renewal readers.
type ownerFake struct {
	mu    sync.Mutex
	files map[string]map[string]any
	calls []string
	fail  bool
}

func (o *ownerFake) execute(_ context.Context, _ Owner, program string, input []byte) ([]byte, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	var x map[string]any
	_ = json.Unmarshal(input, &x)
	provider, _ := x["provider"].(string)
	switch program {
	case ownerInstall:
		o.calls = append(o.calls, "install:"+provider)
		if o.fail {
			return nil, errors.New("busy")
		}
		o.files[provider] = x["document"].(map[string]any)
		return []byte(`{"sha256":"x"}`), nil
	case ownerRead, oauthOwnerRead:
		o.calls = append(o.calls, "read:"+provider)
		doc, ok := o.files[provider]
		if !ok {
			return nil, errors.New("missing")
		}
		return json.Marshal(map[string]any{"sha256": "x", "document": doc})
	}
	o.calls = append(o.calls, "other")
	return []byte("saved"), nil
}

func enrollFixture(t *testing.T) (*Broker, *ownerFake) {
	t.Helper()
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	owner := &ownerFake{files: map[string]map[string]any{}}
	now := time.Now().UTC()
	b := &Broker{Client: c, Config: Config{Control: "broker", Journal: "journal", Leases: "provider-leases", Providers: []string{"grok", "cursor"}, Owners: map[string]Owner{"grok": {}, "cursor": {}}, OAuthClientIDs: map[string]string{"cursor": "id"}}, Now: func() time.Time { return now }, Execute: owner.execute}
	// The first tick creates the bound journal and lease Secrets.
	_ = b.TickLeases(context.Background())
	return b, owner
}

func journal(t *testing.T, b *Broker) *corev1.Secret {
	t.Helper()
	var s corev1.Secret
	if err := b.Client.Get(context.Background(), types.NamespacedName{Namespace: "broker", Name: "journal"}, &s); err != nil {
		t.Fatal(err)
	}
	return &s
}

func TestValidateLogin(t *testing.T) {
	now := time.Now()
	if _, err := ValidateLogin("grok", grokLogin(t, "k", now.Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	if sub, err := ValidateLogin("cursor", cursorLogin(t, "user-1", now.Add(time.Hour))); err != nil || sub != "user-1" {
		t.Fatal(sub, err)
	}
	var doc map[string]map[string]any
	_ = json.Unmarshal(grokLogin(t, "k", now), &doc)
	mutate := func(change func(map[string]any)) []byte {
		copyDoc := map[string]map[string]any{}
		for k, v := range doc {
			entry := map[string]any{}
			for a, b := range v {
				entry[a] = b
			}
			change(entry)
			copyDoc[k] = entry
		}
		raw, _ := json.Marshal(copyDoc)
		return raw
	}
	for name, c := range map[string]struct {
		provider string
		raw      []byte
	}{
		"not json":          {"grok", []byte("nope")},
		"too large":         {"grok", bytes.Repeat([]byte("a"), maxLogin+1)},
		"other issuer":      {"grok", mutate(func(e map[string]any) { e["oidc_issuer"] = "https://evil.example" })},
		"no refresh":        {"grok", mutate(func(e map[string]any) { delete(e, "refresh_token") })},
		"bad expiry":        {"grok", mutate(func(e map[string]any) { e["expires_at"] = "soon" })},
		"two accounts":      {"grok", []byte(`{"a":{},"b":{}}`)},
		"cursor no token":   {"cursor", []byte(`{"refreshToken":"r"}`)},
		"cursor no refresh": {"cursor", []byte(`{"accessToken":"a.eyJzdWIiOiJ4In0.c"}`)},
		"unknown provider":  {"codex", []byte(`{}`)},
	} {
		if _, err := ValidateLogin(c.provider, c.raw); err == nil {
			t.Errorf("%s: accepted", name)
		} else if strings.Contains(err.Error(), "refresh-") {
			t.Errorf("%s: message leaks material: %v", name, err)
		}
	}
}

// A new login replaces an interrupted renewal instead of being overwritten by
// it, and is published right away.
func TestInstallClearsInterruptedRenewalFirst(t *testing.T) {
	b, owner := enrollFixture(t)
	ctx := context.Background()
	j := journal(t, b)
	before := j.DeepCopy()
	if j.Data == nil {
		j.Data = map[string][]byte{}
	}
	j.Data["grok-renewal"] = []byte(`{"stage":"response-saved","document":{"old":{}}}`)
	if err := b.Client.Patch(ctx, j, clientMerge(before)); err != nil {
		t.Fatal(err)
	}
	if err := b.Install(ctx, "grok", grokLogin(t, "fresh", b.now().Add(time.Hour)), false); err != nil {
		t.Fatal(err)
	}
	if _, ok := journal(t, b).Data["grok-renewal"]; ok {
		t.Fatal("interrupted renewal kept")
	}
	if dig := owner.files["grok"]["https://auth.x.ai::client"].(map[string]any)["key"]; dig != "fresh" {
		t.Fatalf("owner holds %v", dig)
	}
	grants, _ := published(t, b)
	if g := grants["grok"]; !g.Enabled || g.Access.Token != "fresh" {
		t.Fatalf("lease %+v", g)
	}
}

func TestInstallCursorAccountPin(t *testing.T) {
	b, owner := enrollFixture(t)
	ctx := context.Background()
	exp := b.now().Add(time.Hour)
	if err := b.Install(ctx, "cursor", cursorLogin(t, "first", exp), false); err != nil {
		t.Fatal(err)
	}
	if string(journal(t, b).Data[cursorSubjectKey]) != "first" {
		t.Fatal("account not pinned")
	}
	if err := b.Install(ctx, "cursor", cursorLogin(t, "second", exp), false); !errors.Is(err, ErrAccountChanged) {
		t.Fatalf("different account accepted: %v", err)
	}
	if owner.files["cursor"]["refreshToken"] != "refresh-first" {
		t.Fatal("refused login was written")
	}
	if err := b.Install(ctx, "cursor", cursorLogin(t, "second", exp), true); err != nil {
		t.Fatal(err)
	}
	if string(journal(t, b).Data[cursorSubjectKey]) != "second" || owner.files["cursor"]["refreshToken"] != "refresh-second" {
		t.Fatal("replacement not installed and pinned")
	}
}

func TestInstallRefusesUnconfiguredOrInvalid(t *testing.T) {
	b, owner := enrollFixture(t)
	b.Config.Providers = []string{"grok"}
	owner.calls = nil
	if err := b.Install(context.Background(), "cursor", cursorLogin(t, "x", time.Now().Add(time.Hour)), false); err == nil {
		t.Fatal("unconfigured provider accepted")
	}
	if err := b.Install(context.Background(), "grok", []byte(`{}`), false); err == nil {
		t.Fatal("invalid login accepted")
	}
	if len(owner.calls) != 0 {
		t.Fatalf("owner touched: %v", owner.calls)
	}
}

func TestImportSecret(t *testing.T) {
	b, owner := enrollFixture(t)
	ctx := context.Background()
	good := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "broker", Name: ImportSecretName("grok")}, Data: map[string][]byte{ImportKey: grokLogin(t, "imported", b.now().Add(time.Hour))}}
	bad := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "broker", Name: ImportSecretName("cursor")}, Data: map[string][]byte{ImportKey: []byte(`{"accessToken":"nope"}`)}}
	for _, s := range []*corev1.Secret{good, bad} {
		if err := b.Client.Create(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	_ = b.TickLeases(ctx)
	if err := b.Client.Get(ctx, types.NamespacedName{Namespace: "broker", Name: good.Name}, &corev1.Secret{}); !apierrors.IsNotFound(err) {
		t.Fatalf("consumed import Secret kept: %v", err)
	}
	grants, _ := published(t, b)
	if grants["grok"].Access == nil || grants["grok"].Access.Token != "imported" {
		t.Fatalf("import not published: %+v", grants["grok"])
	}
	var refused corev1.Secret
	if err := b.Client.Get(ctx, types.NamespacedName{Namespace: "broker", Name: bad.Name}, &refused); err != nil || refused.Annotations[ImportErrorAnnotation] == "" {
		t.Fatalf("refusal not recorded: %v %v", err, refused.Annotations)
	}
	calls := len(owner.calls)
	_ = b.TickLeases(ctx)
	for _, c := range owner.calls[calls:] {
		if c == "install:cursor" {
			t.Fatal("refused import retried")
		}
	}
}

// The real owner and login programs, run against a temporary home.
func TestOwnerProgramsOnDisk(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not installed")
	}
	home := t.TempDir()
	proc := t.TempDir() // an empty /proc: no provider CLI running
	run := func(program string, input any) (string, error) {
		raw, _ := json.Marshal(input)
		cmd := exec.Command("python3", "-c", strings.ReplaceAll(program, "pathlib.Path('/proc')", "pathlib.Path('"+proc+"')"))
		cmd.Env = append(os.Environ(), "HOME="+home)
		cmd.Stdin = bytes.NewReader(raw)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	var doc map[string]any
	_ = json.Unmarshal(cursorLogin(t, "disk", time.Now().Add(time.Hour)), &doc)
	if out, err := run(ownerInstall, map[string]any{"provider": "cursor", "document": doc}); err != nil {
		t.Fatal(out)
	}
	path := filepath.Join(home, ".config/cursor/auth.json")
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("written file %v %v", info, err)
	}
	if out, err := run(oauthOwnerRead, map[string]any{"provider": "cursor"}); err != nil || !strings.Contains(out, "refresh-disk") {
		t.Fatalf("renewal reader cannot read the installed login: %s", out)
	}
	if out, err := run(loginRead, map[string]any{"provider": "cursor"}); err != nil || !strings.Contains(out, "refresh-disk") {
		t.Fatalf("login reader: %s", out)
	}
	// A running provider CLI blocks the write.
	_ = os.MkdirAll(filepath.Join(proc, "42"), 0o755)
	_ = os.WriteFile(filepath.Join(proc, "42", "comm"), []byte("cursor-agent\n"), 0o644)
	if _, err := run(ownerInstall, map[string]any{"provider": "cursor", "document": doc}); err == nil {
		t.Fatal("installed while the CLI was running")
	}
}

type fakePods struct {
	mu      sync.Mutex
	output  string
	runErr  error
	login   []byte
	block   chan struct{}
	started []string
	deleted []string
}

func (f *fakePods) Start(_ context.Context, p string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started = append(f.started, p)
	return nil
}
func (f *fakePods) Run(ctx context.Context, _ string, out io.Writer) error {
	_, _ = io.WriteString(out, f.output)
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return f.runErr
}
func (f *fakePods) Read(context.Context, string) ([]byte, error) {
	if f.login == nil {
		return nil, errors.New("none")
	}
	return f.login, nil
}
func (f *fakePods) Delete(_ context.Context, p string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, p)
	return nil
}

// Output captured from grok 1.0.77 and cursor-agent (codes replaced).
const grokOutput = "\nTo sign in, open this URL in your browser:\n\n  https://accounts.x.ai/oauth2/device?user_code=ABCD-1234\n\nConfirm this code in your browser:\n\n  ABCD-1234\n\n\x1b[90mOnly continue with a code you requested. Don't share it with anyone.\x1b[0m\n\nWaiting for authorization...\n"
const cursorOutput = "Starting login process...\nAuthenticating with Cursor...\nWaiting for browser authentication...\nOpen a browser and navigate to this link: https://cursor.com/loginDeepControl?challenge=c&uuid=u&mode=login&redirectTarget=cli\n"

func waitState(t *testing.T, l *Logins, provider string, states ...string) LoginStatus {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, s := range l.Status() {
			if s.Provider == provider {
				for _, want := range states {
					if s.State == want {
						return s
					}
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s never reached %v: %+v", provider, states, l.Status())
	return LoginStatus{}
}

func TestConsoleSignIn(t *testing.T) {
	b, owner := enrollFixture(t)
	pods := &fakePods{output: grokOutput, block: make(chan struct{}), login: grokLogin(t, "console", b.now().Add(time.Hour))}
	l := &Logins{Broker: b, Pods: pods}
	if _, err := l.Start("grok", false); err != nil {
		t.Fatal(err)
	}
	s := waitState(t, l, "grok", "waiting")
	for s.Code == "" {
		time.Sleep(10 * time.Millisecond)
		s = waitState(t, l, "grok", "waiting")
	}
	if s.URL != "https://accounts.x.ai/oauth2/device?user_code=ABCD-1234" || s.Code != "ABCD-1234" {
		t.Fatalf("shown %q %q", s.URL, s.Code)
	}
	if _, err := l.Start("cursor", false); !errors.Is(err, ErrLoginBusy) {
		t.Fatalf("second sign-in not refused: %v", err)
	}
	close(pods.block)
	s = waitState(t, l, "grok", "done", "failed")
	if s.State != "done" || s.URL != "" || s.Code != "" {
		t.Fatalf("final %+v", s)
	}
	if owner.files["grok"]["https://auth.x.ai::client"].(map[string]any)["key"] != "console" {
		t.Fatal("login not installed")
	}
	if len(pods.deleted) == 0 {
		t.Fatal("login pod kept")
	}
}

func TestConsoleSignInCancelAndFailure(t *testing.T) {
	b, owner := enrollFixture(t)
	pods := &fakePods{output: cursorOutput, block: make(chan struct{})}
	l := &Logins{Broker: b, Pods: pods}
	if _, err := l.Start("cursor", false); err != nil {
		t.Fatal(err)
	}
	if s := waitState(t, l, "cursor", "waiting"); !strings.HasPrefix(s.URL, "https://cursor.com/loginDeepControl") && s.URL != "" {
		t.Fatalf("link %q", s.URL)
	}
	l.Cancel("cursor")
	if s := waitState(t, l, "cursor", "canceled"); s.URL != "" {
		t.Fatalf("link kept after cancel: %+v", s)
	}
	if len(pods.deleted) == 0 {
		t.Fatal("pod kept after cancel")
	}
	// The CLI exits without a login file: failed, nothing installed.
	pods2 := &fakePods{output: cursorOutput}
	l2 := &Logins{Broker: b, Pods: pods2}
	if _, err := l2.Start("cursor", false); err != nil {
		t.Fatal(err)
	}
	waitState(t, l2, "cursor", "failed")
	if _, ok := owner.files["cursor"]; ok {
		t.Fatal("installed without a login")
	}
}

func TestLoginOutputShowsOnlyProviderLinks(t *testing.T) {
	var link, code string
	o := &loginOutput{provider: "cursor", found: func(l, c string) { link, code = l, c }}
	_, _ = io.WriteString(o, "see https://evil.example/phish\nOpen https://cursor.com.evil.example/x\n")
	if link != "" {
		t.Fatalf("foreign link shown: %q", link)
	}
	// Split writes still parse.
	_, _ = io.WriteString(o, "Open this link: https://cursor.com/loginDeep")
	_, _ = io.WriteString(o, "Control?challenge=c\n")
	if link != "https://cursor.com/loginDeepControl?challenge=c" || code != "" {
		t.Fatalf("link %q code %q", link, code)
	}
}

type reviewer map[string]string

func (r reviewer) Review(_ context.Context, token string) (string, error) {
	if u, ok := r[token]; ok {
		return u, nil
	}
	return "", errors.New("unauthenticated")
}

func TestSignInAPIAcceptsOnlyTheController(t *testing.T) {
	b, _ := enrollFixture(t)
	pods := &fakePods{output: grokOutput, block: make(chan struct{})}
	defer close(pods.block)
	srv := httptest.NewServer(&Server{Logins: &Logins{Broker: b, Pods: pods}, Reviewer: reviewer{"good": "system:serviceaccount:c:controller", "other": "system:serviceaccount:c:someone"}, Caller: "system:serviceaccount:c:controller"})
	defer srv.Close()
	call := func(method, path, token string) int {
		req, _ := http.NewRequest(method, srv.URL+path, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	for _, token := range []string{"", "other", "unknown"} {
		if code := call(http.MethodPost, "/v1/logins/grok", token); code != http.StatusUnauthorized {
			t.Errorf("token %q: %d", token, code)
		}
	}
	if code := call(http.MethodGet, "/healthz", ""); code != http.StatusOK {
		t.Fatal("health", code)
	}
	if code := call(http.MethodPost, "/v1/logins/grok", "good"); code != http.StatusAccepted {
		t.Fatal("start", code)
	}
	if code := call(http.MethodPost, "/v1/logins/cursor", "good"); code != http.StatusConflict {
		t.Fatal("busy", code)
	}
	if code := call(http.MethodPost, "/v1/logins/codex", "good"); code != http.StatusBadRequest {
		t.Fatal("unknown provider", code)
	}
	if code := call(http.MethodGet, "/v1/logins", "good"); code != http.StatusOK {
		t.Fatal("status", code)
	}
}
