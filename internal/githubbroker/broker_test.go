package githubbroker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/proxy"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// fakeGitHub implements the OAuth endpoints with rotating refresh tokens.
type fakeGitHub struct {
	mu        sync.Mutex
	refresh   string // the one valid refresh token
	n         int
	pending   int // device polls before approval
	revoked   bool
	refreshes int
	userID    int64
	loseReply bool // process a refresh but fail the response (crash window)
}

func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r.ParseForm()
	next := func() map[string]any {
		f.n++
		f.refresh = "ghr_RefreshTokenNumber0000000" + string(rune('a'+f.n))
		return map[string]any{"access_token": "ghu_AccessTokenNumber00000000" + string(rune('a'+f.n)), "expires_in": 28800, "refresh_token": f.refresh, "refresh_token_expires_in": 15897600, "token_type": "bearer"}
	}
	switch {
	case r.URL.Path == "/login/device/code":
		json.NewEncoder(w).Encode(map[string]any{"device_code": "dev", "user_code": "ABCD-1234", "verification_uri": "https://github.com/login/device", "expires_in": 900, "interval": 5})
	case r.URL.Path == "/login/oauth/access_token" && r.Form.Get("grant_type") == "urn:ietf:params:oauth:grant-type:device_code":
		if f.pending > 0 {
			f.pending--
			json.NewEncoder(w).Encode(map[string]string{"error": "authorization_pending"})
			return
		}
		json.NewEncoder(w).Encode(next())
	case r.URL.Path == "/login/oauth/access_token" && r.Form.Get("grant_type") == "refresh_token":
		if r.Form.Get("client_secret") != "secret" || f.revoked || r.Form.Get("refresh_token") != f.refresh {
			json.NewEncoder(w).Encode(map[string]string{"error": "bad_refresh_token"})
			return
		}
		f.refreshes++
		reply := next()
		if f.loseReply {
			f.loseReply = false
			panic(http.ErrAbortHandler) // GitHub rotated; the broker never hears.
		}
		json.NewEncoder(w).Encode(reply)
	case r.URL.Path == "/user":
		json.NewEncoder(w).Encode(map[string]any{"id": f.userID, "login": "SkippySteve"})
	case r.Method == "DELETE" && r.URL.Path == "/applications/Iv1.client/grant":
		if u, p, _ := r.BasicAuth(); u != "Iv1.client" || p != "secret" {
			w.WriteHeader(401)
			return
		}
		f.revoked = true
		w.WriteHeader(204)
	default:
		w.WriteHeader(404)
	}
}

type harness struct {
	b         *Broker
	gh        *fakeGitHub
	c         client.Client
	now       time.Time
	failPatch bool
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{gh: &fakeGitHub{userID: 42}, now: time.Unix(2_000_000_000, 0)}
	server := httptest.NewServer(h.gh)
	t.Cleanup(server.Close)
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	label := map[string]string{BindingLabel: "github-lease"}
	h.c = fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "github-state", Namespace: "broker", Labels: label}},
	).WithInterceptorFuncs(interceptor.Funcs{Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
		if h.failPatch {
			return errors.New("API unavailable")
		}
		return c.Patch(ctx, obj, patch, opts...)
	}}).Build()
	h.b = &Broker{Client: h.c, Config: Config{Namespace: "broker", State: "github-state", Lease: "github-lease", ClientID: "Iv1.client", ClientSecret: "secret"}, Web: server.URL, API: server.URL, Now: func() time.Time { return h.now }, Sleep: func(context.Context, time.Duration) error { return nil }}
	return h
}

func (h *harness) data(t *testing.T, name string) map[string][]byte {
	t.Helper()
	var s corev1.Secret
	if err := h.c.Get(context.Background(), types.NamespacedName{Namespace: "broker", Name: name}, &s); err != nil {
		t.Fatal(err)
	}
	return s.Data
}

func (h *harness) lease(t *testing.T) proxy.GitHubLease {
	t.Helper()
	l, err := proxy.ParseGitHubLease(h.data(t, "github-lease")[LeaseKey])
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestAuthorizeStoresTokensWithoutShowingThem(t *testing.T) {
	h := newHarness(t)
	h.gh.pending = 2
	var out bytes.Buffer
	if err := h.b.Authorize(context.Background(), &out); err != nil {
		t.Fatal(err)
	}
	state := h.data(t, "github-state")
	if !strings.Contains(out.String(), "ABCD-1234") || strings.Contains(out.String(), "ghu_") || strings.Contains(out.String(), "ghr_") {
		t.Fatal("authorize output", out.String())
	}
	if string(state[keyLogin]) != "SkippySteve" || string(state[keyUserID]) != "42" || len(state[keyRefresh]) == 0 {
		t.Fatal("state not stored", state)
	}
	if err := h.b.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	l := h.lease(t)
	if !l.Enabled || l.Login != "SkippySteve" || l.ExpiresAt != h.now.Add(LeaseLifetime).Unix() {
		t.Fatal("lease", l)
	}
	if raw := h.data(t, "github-lease")[LeaseKey]; bytes.Contains(raw, []byte("ghr_")) {
		t.Fatal("refresh token published")
	}
	// Another account is refused.
	h.gh.userID = 7
	if err := h.b.Authorize(context.Background(), &out); err == nil {
		t.Fatal("account switch accepted")
	}
}

func TestRefreshRotatesAndExpiresLeases(t *testing.T) {
	h := newHarness(t)
	if err := h.b.Tick(context.Background()); err == nil || h.lease(t).Enabled {
		t.Fatal("unauthorized broker published a lease")
	}
	h.b.Authorize(context.Background(), &bytes.Buffer{})
	h.b.Tick(context.Background())
	first := h.lease(t).Token
	h.now = h.now.Add(8*time.Hour - 20*time.Minute) // inside the refresh window
	if err := h.b.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	second := h.lease(t)
	if second.Token == first || h.gh.refreshes != 1 || string(h.data(t, "github-state")[keyRefresh]) != h.gh.refresh {
		t.Fatal("refresh did not rotate", second)
	}
	// The broker stops: the lease runs out on its own within five minutes.
	if second.ExpiresAt > h.now.Add(LeaseLifetime).Unix() {
		t.Fatal("lease outlives its cap")
	}
}

func TestInterruptedRefreshIsNeverReplayedAndFailsClosed(t *testing.T) {
	h := newHarness(t)
	h.b.Authorize(context.Background(), &bytes.Buffer{})
	h.b.Tick(context.Background())
	h.now = h.now.Add(8*time.Hour - 20*time.Minute)

	// 1. GitHub answers, but storing the answer fails: the answer is kept and
	// stored on the next tick without a second refresh.
	h.failPatch = true
	h.b.Tick(context.Background())
	h.failPatch = false
	// The stage write failed first, so nothing reached GitHub yet.
	if h.gh.refreshes != 0 {
		t.Fatal("refreshed without recording the stage")
	}
	// Fail only the store: let the stage write through, then fail.
	calls := 0
	h.b.Client = interceptor.NewClient(h.c.(client.WithWatch), interceptor.Funcs{Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
		calls++
		if calls == 2 {
			return errors.New("API unavailable")
		}
		return c.Patch(ctx, obj, patch, opts...)
	}})
	if err := h.b.Tick(context.Background()); err == nil {
		t.Fatal("unstored refresh reported success")
	}
	if h.gh.refreshes != 1 || h.b.pending == nil {
		t.Fatal("refresh result not held", h.gh.refreshes)
	}
	if err := h.b.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if h.gh.refreshes != 1 || string(h.data(t, "github-state")[keyRefresh]) != h.gh.refresh || !h.lease(t).Enabled {
		t.Fatal("held refresh not stored or replayed")
	}

	// 2. GitHub rotates but the broker never hears (crash window): the spent
	// token is refused, the broker withdraws the lease and asks for a new
	// authorization instead of publishing anything stale.
	h.now = h.now.Add(8*time.Hour - 20*time.Minute)
	h.gh.loseReply = true
	h.b.Tick(context.Background())
	if stage := string(h.data(t, "github-state")[keyStage]); stage != stageRefreshing {
		t.Fatal("refresh start not recorded", stage)
	}
	if err := h.b.Tick(context.Background()); err == nil {
		t.Fatal("spent refresh token accepted")
	}
	state := h.data(t, "github-state")
	if string(state[keyStage]) != stageReauthorize || len(state[keyRefresh]) != 0 || h.lease(t).Enabled || string(h.data(t, "github-lease")[StatusKey]) != "authorization-required" {
		t.Fatal("broker did not fail closed", state)
	}
	// A new authorization resumes.
	h.b.Authorize(context.Background(), &bytes.Buffer{})
	if err := h.b.Tick(context.Background()); err != nil || !h.lease(t).Enabled {
		t.Fatal("reauthorization did not resume", err)
	}
}

func TestRevokeDisablesFirstAndConfirmsAtGitHub(t *testing.T) {
	h := newHarness(t)
	h.b.Authorize(context.Background(), &bytes.Buffer{})
	h.b.Tick(context.Background())
	var out bytes.Buffer
	if err := h.b.Revoke(context.Background(), &out); err != nil {
		t.Fatal(err, out.String())
	}
	if !h.gh.revoked || h.lease(t).Enabled || len(h.data(t, "github-state")[keyRefresh]) != 0 {
		t.Fatal("revocation incomplete")
	}
	if err := h.b.Tick(context.Background()); err == nil || h.lease(t).Enabled {
		t.Fatal("revoked broker published a lease")
	}
	// GitHub unreachable: local access still ends, the command fails and
	// keeps the tokens for a retry.
	h.gh.revoked = false
	h.b.Authorize(context.Background(), &bytes.Buffer{})
	h.b.Tick(context.Background())
	h.b.API = "http://127.0.0.1:1"
	if err := h.b.Revoke(context.Background(), &out); err == nil {
		t.Fatal("unconfirmed revocation reported success")
	}
	if h.lease(t).Enabled || len(h.data(t, "github-state")[keyRefresh]) == 0 {
		t.Fatal("failed revocation must disable access and keep tokens for a retry")
	}
}

func TestUnboundSecretsRefused(t *testing.T) {
	h := newHarness(t)
	// The missing lease Secret is created with the binding label.
	if err := h.b.publish(context.Background(), nil, "test"); err != nil {
		t.Fatal(err)
	}
	var s corev1.Secret
	h.c.Get(context.Background(), types.NamespacedName{Namespace: "broker", Name: "github-lease"}, &s)
	s.Labels = nil
	h.c.Update(context.Background(), &s)
	if err := h.b.Authorize(context.Background(), &bytes.Buffer{}); err == nil {
		t.Fatal("unbound lease Secret used")
	}
}

func TestConfigFromEnvReadsClientFiles(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(dir+"/id", []byte("Iv1.client\n"), 0o600)
	os.WriteFile(dir+"/secret", []byte("secret\n"), 0o600)
	t.Setenv("POD_NAMESPACE", "broker")
	t.Setenv("GITHUB_BROKER_STATE", "github-state")
	t.Setenv("GITHUB_BROKER_LEASE", "github-lease")
	t.Setenv("GITHUB_APP_CLIENT_ID", "")
	t.Setenv("GITHUB_APP_CLIENT_ID_FILE", dir+"/id")
	t.Setenv("GITHUB_APP_CLIENT_SECRET_FILE", dir+"/secret")
	c, err := ConfigFromEnv()
	if err != nil || c.ClientID != "Iv1.client" || c.ClientSecret != "secret" {
		t.Fatal(c, err)
	}
	t.Setenv("GITHUB_BROKER_LEASE", "github-state")
	if _, err := ConfigFromEnv(); err == nil {
		t.Fatal("state and lease in one Secret accepted")
	}
}
