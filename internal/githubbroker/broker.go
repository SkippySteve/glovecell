// Package githubbroker holds a GitHub App user authorization outside every
// project and publishes short access leases for the project proxies.
//
// The broker keeps the refresh token in one Secret (its state) and never
// writes it anywhere else. Every tick it refreshes the user access token when
// it nears expiry and writes an access lease, capped at five minutes, to a
// second Secret. The project controller copies that lease only into the
// proxies of projects that opt into GitHub (spec.github); workers never
// receive it. Stopping the broker or revoking the authorization therefore
// ends every project's GitHub access within minutes.
//
// GitHub rotates the refresh token on every refresh. The broker records that
// a refresh started before it calls GitHub, keeps a successful response in
// memory until it is stored, and never publishes a lease that is not stored.
// If it crashes between GitHub's answer and the store, the old refresh token
// is spent: the next refresh fails, the broker publishes no lease and asks
// for a new authorization (fail closed).
package githubbroker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/githubpolicy"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/proxy"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// BindingLabel marks the broker's two Secrets. Its value is the lease
// Secret's name. The broker never creates or adopts other Secrets.
const BindingLabel = "github.agentwork.dev/broker"

// LeaseKey is the lease Secret key the controller reads.
const LeaseKey = proxy.GitHubAccessKey

// StatusKey is a lease Secret key with a non-secret status word.
const StatusKey = "github-status"

// State Secret keys.
const (
	keyRefresh        = "refresh-token"
	keyRefreshExpires = "refresh-expires-at"
	keyAccess         = "access-token"
	keyAccessExpires  = "access-expires-at"
	keyLogin          = "login"
	keyUserID         = "user-id"
	keyStage          = "stage"
	keyRevoked        = "revoked"
)

// Stages recorded in the state Secret.
const (
	stageRefreshing  = "refresh-started"
	stageReauthorize = "reauthorization-required"
)

const (
	// LeaseLifetime caps each published lease.
	LeaseLifetime = 5 * time.Minute
	// RefreshBefore is how long before expiry the access token is renewed.
	RefreshBefore = 30 * time.Minute
)

// Config names the broker's Secrets and its GitHub App.
type Config struct {
	Namespace    string
	State        string
	Lease        string
	ClientID     string
	ClientSecret string
}

// Broker renews the authorization and publishes leases.
type Broker struct {
	Client client.Client
	Config Config
	HTTP   *http.Client
	// Web and API default to github.com and api.github.com.
	Web, API string
	Now      func() time.Time
	Sleep    func(context.Context, time.Duration) error
	// pending is a refresh GitHub answered but the Secret does not hold yet.
	pending *tokens
}

type tokens struct {
	Access, Refresh               string
	AccessExpires, RefreshExpires time.Time
}

func (b *Broker) now() time.Time {
	if b.Now != nil {
		return b.Now()
	}
	return time.Now()
}
func (b *Broker) web() string {
	if b.Web != "" {
		return b.Web
	}
	return "https://github.com"
}
func (b *Broker) api() string {
	if b.API != "" {
		return b.API
	}
	return "https://api.github.com"
}
func (b *Broker) http() *http.Client {
	c := &http.Client{Timeout: 25 * time.Second}
	if b.HTTP != nil {
		*c = *b.HTTP
	}
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return errors.New("credential redirects denied") }
	return c
}

// secret reads one of the broker's Secrets and checks its binding.
func (b *Broker) secret(ctx context.Context, name string) (*corev1.Secret, error) {
	var s corev1.Secret
	if err := b.Client.Get(ctx, types.NamespacedName{Namespace: b.Config.Namespace, Name: name}, &s); err != nil {
		return nil, errors.New("broker Secret " + name + " unavailable")
	}
	if s.Labels[BindingLabel] != b.Config.Lease {
		return nil, errors.New("broker Secret " + name + " is not bound to this broker")
	}
	if s.Data == nil {
		s.Data = map[string][]byte{}
	}
	return &s, nil
}

// patch applies change to a Secret with optimistic locking.
func (b *Broker) patch(ctx context.Context, name string, change func(map[string][]byte)) error {
	s, err := b.secret(ctx, name)
	if err != nil {
		return err
	}
	before := s.DeepCopy()
	change(s.Data)
	if err := b.Client.Patch(ctx, s, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})); err != nil {
		return errors.New("broker Secret " + name + " update failed")
	}
	return nil
}

func unix(raw []byte) time.Time {
	n, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil || n <= 0 {
		return time.Time{}
	}
	return time.Unix(n, 0)
}
func stamp(t time.Time) []byte { return []byte(strconv.FormatInt(t.Unix(), 10)) }

// publish writes the lease and status. A nil lease publishes disabled.
func (b *Broker) publish(ctx context.Context, lease *proxy.GitHubLease, status string) error {
	if lease == nil {
		lease = &proxy.GitHubLease{Enabled: false}
	}
	raw, _ := json.Marshal(lease)
	return b.patch(ctx, b.Config.Lease, func(d map[string][]byte) {
		d[LeaseKey] = raw
		d[StatusKey] = []byte(status)
	})
}

// Tick renews the authorization if needed and publishes a lease.
func (b *Broker) Tick(ctx context.Context) error {
	if b.pending != nil {
		if err := b.store(ctx, *b.pending); err != nil {
			return err // Keep it; never refresh again with a spent token.
		}
		b.pending = nil
	}
	state, err := b.secret(ctx, b.Config.State)
	if err != nil {
		return err
	}
	d := state.Data
	if string(d[keyRevoked]) == "true" {
		return errors.Join(b.publish(ctx, nil, "revoked"), errors.New("GitHub access revoked; run authorize to resume"))
	}
	if len(d[keyRefresh]) == 0 || string(d[keyStage]) == stageReauthorize {
		return errors.Join(b.publish(ctx, nil, "authorization-required"), errors.New("GitHub authorization required"))
	}
	now := b.now()
	access, expires := string(d[keyAccess]), unix(d[keyAccessExpires])
	if access == "" || expires.Sub(now) < RefreshBefore {
		if !unix(d[keyRefreshExpires]).After(now) {
			return b.reauthorize(ctx, "refresh token expired")
		}
		if err := b.patch(ctx, b.Config.State, func(m map[string][]byte) { m[keyStage] = []byte(stageRefreshing) }); err != nil {
			return err
		}
		t, err := b.refresh(ctx, string(d[keyRefresh]))
		if errors.Is(err, errGrantInvalid) {
			return b.reauthorize(ctx, "GitHub refused the refresh token")
		}
		if err != nil {
			return b.publishCurrent(ctx, access, expires, string(d[keyLogin]), err)
		}
		b.pending = &t
		if err := b.store(ctx, t); err != nil {
			return err
		}
		b.pending = nil
		access, expires = t.Access, t.AccessExpires
	}
	return b.publishCurrent(ctx, access, expires, string(d[keyLogin]), nil)
}

// publishCurrent publishes the stored access token while it is still valid.
func (b *Broker) publishCurrent(ctx context.Context, access string, expires time.Time, login string, cause error) error {
	now := b.now()
	if access == "" || !expires.After(now.Add(time.Minute)) {
		return errors.Join(b.publish(ctx, nil, "unavailable"), cause)
	}
	until := math.Min(float64(expires.Unix()), float64(now.Add(LeaseLifetime).Unix()))
	return errors.Join(b.publish(ctx, &proxy.GitHubLease{Enabled: true, Token: access, ExpiresAt: int64(until), Login: login}, "live"), cause)
}

// store saves a refresh result and clears the stage in one update.
func (b *Broker) store(ctx context.Context, t tokens) error {
	return b.patch(ctx, b.Config.State, func(m map[string][]byte) {
		m[keyAccess], m[keyAccessExpires] = []byte(t.Access), stamp(t.AccessExpires)
		m[keyRefresh], m[keyRefreshExpires] = []byte(t.Refresh), stamp(t.RefreshExpires)
		m[keyStage] = nil
	})
}

// reauthorize forgets the tokens and stops publishing until authorize runs.
func (b *Broker) reauthorize(ctx context.Context, why string) error {
	slog.WarnContext(ctx, "GitHub broker needs a new authorization", "reason", why)
	err := b.patch(ctx, b.Config.State, func(m map[string][]byte) {
		m[keyStage] = []byte(stageReauthorize)
		delete(m, keyAccess)
		delete(m, keyRefresh)
	})
	return errors.Join(b.publish(ctx, nil, "authorization-required"), err, errors.New("GitHub authorization required: "+why))
}

var errGrantInvalid = errors.New("GitHub refused the grant")

type oauthResponse struct {
	AccessToken           string `json:"access_token"`
	ExpiresIn             int64  `json:"expires_in"`
	RefreshToken          string `json:"refresh_token"`
	RefreshTokenExpiresIn int64  `json:"refresh_token_expires_in"`
	Error                 string `json:"error"`
	DeviceCode            string `json:"device_code"`
	UserCode              string `json:"user_code"`
	VerificationURI       string `json:"verification_uri"`
	Interval              int64  `json:"interval"`
}

// oauth posts a form to github.com's OAuth endpoints. GitHub reports most
// errors with HTTP 200 and an "error" field.
func (b *Broker) oauth(ctx context.Context, path string, form url.Values) (oauthResponse, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", b.web()+path, strings.NewReader(form.Encode()))
	if err != nil {
		return oauthResponse{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	res, err := b.http().Do(req)
	if err != nil {
		return oauthResponse{}, errors.New("GitHub OAuth endpoint unavailable")
	}
	defer res.Body.Close()
	var out oauthResponse
	if json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&out) != nil {
		return oauthResponse{}, fmt.Errorf("GitHub OAuth endpoint returned HTTP %d", res.StatusCode)
	}
	return out, nil
}

func (r oauthResponse) tokens(now time.Time) (tokens, error) {
	if !validToken(r.AccessToken) || !validToken(r.RefreshToken) || r.ExpiresIn <= 60 || r.RefreshTokenExpiresIn <= 0 {
		return tokens{}, errors.New("GitHub returned no expiring user token; enable user token expiration on the App")
	}
	return tokens{Access: r.AccessToken, Refresh: r.RefreshToken, AccessExpires: now.Add(time.Duration(r.ExpiresIn) * time.Second), RefreshExpires: now.Add(time.Duration(r.RefreshTokenExpiresIn) * time.Second)}, nil
}

func validToken(t string) bool {
	if len(t) < 20 || len(t) > 255 {
		return false
	}
	for _, c := range t {
		if !(c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z') {
			return false
		}
	}
	return true
}

// refresh exchanges the refresh token. GitHub Apps need the client secret.
func (b *Broker) refresh(ctx context.Context, refresh string) (tokens, error) {
	out, err := b.oauth(ctx, "/login/oauth/access_token", url.Values{"client_id": {b.Config.ClientID}, "client_secret": {b.Config.ClientSecret}, "grant_type": {"refresh_token"}, "refresh_token": {refresh}})
	if err != nil {
		return tokens{}, err
	}
	switch out.Error {
	case "":
	case "bad_refresh_token", "invalid_grant", "unauthorized_client":
		return tokens{}, errGrantInvalid
	default:
		return tokens{}, errors.New("GitHub refresh failed: " + sanitize(out.Error))
	}
	return out.tokens(b.now())
}

func sanitize(s string) string {
	if len(s) > 64 {
		s = s[:64]
	}
	return strings.Map(func(r rune) rune {
		if r == '_' || r >= 'a' && r <= 'z' {
			return r
		}
		return -1
	}, s)
}

// user returns the account a token acts for.
func (b *Broker) user(ctx context.Context, token string) (string, string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", b.api()+"/user", nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	res, err := b.http().Do(req)
	if err != nil {
		return "", "", errors.New("GitHub user lookup failed")
	}
	defer res.Body.Close()
	var out struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
	}
	if res.StatusCode != 200 || json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&out) != nil || out.ID <= 0 || !githubpolicy.ValidOwner(out.Login) {
		return "", "", errors.New("GitHub user lookup failed")
	}
	return strconv.FormatInt(out.ID, 10), out.Login, nil
}

func (b *Broker) sleep(ctx context.Context, d time.Duration) error {
	if b.Sleep != nil {
		return b.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Authorize runs GitHub's device flow once. It prints only the verification
// address and the one-time user code; the tokens go straight into the state
// Secret. A different account than the one recorded is refused.
func (b *Broker) Authorize(ctx context.Context, out io.Writer) error {
	state, err := b.secret(ctx, b.Config.State)
	if err != nil {
		return err
	}
	if _, err := b.secret(ctx, b.Config.Lease); err != nil {
		return err
	}
	code, err := b.oauth(ctx, "/login/device/code", url.Values{"client_id": {b.Config.ClientID}})
	if err != nil || code.DeviceCode == "" || code.UserCode == "" || !strings.HasPrefix(code.VerificationURI, "https://github.com/") {
		return errors.New("GitHub device flow could not start; is device flow enabled on the App?")
	}
	fmt.Fprintf(out, "Open %s in a browser signed in to GitHub and enter the code %s\n", code.VerificationURI, code.UserCode)
	interval := time.Duration(max(code.Interval, 5)) * time.Second
	for {
		if err := b.sleep(ctx, interval); err != nil {
			return err
		}
		res, err := b.oauth(ctx, "/login/oauth/access_token", url.Values{"client_id": {b.Config.ClientID}, "device_code": {code.DeviceCode}, "grant_type": {"urn:ietf:params:oauth:grant-type:device_code"}})
		if err != nil {
			return err
		}
		switch res.Error {
		case "authorization_pending":
			continue
		case "slow_down":
			interval += 5 * time.Second
			continue
		case "":
		default:
			return errors.New("GitHub device flow ended: " + sanitize(res.Error))
		}
		t, err := res.tokens(b.now())
		if err != nil {
			return err
		}
		id, login, err := b.user(ctx, t.Access)
		if err != nil {
			return err
		}
		if pinned := string(state.Data[keyUserID]); pinned != "" && pinned != id {
			return errors.New("this broker is bound to another GitHub account; delete the user-id key of its state Secret to change accounts")
		}
		err = b.patch(ctx, b.Config.State, func(m map[string][]byte) {
			m[keyAccess], m[keyAccessExpires] = []byte(t.Access), stamp(t.AccessExpires)
			m[keyRefresh], m[keyRefreshExpires] = []byte(t.Refresh), stamp(t.RefreshExpires)
			m[keyLogin], m[keyUserID] = []byte(login), []byte(id)
			delete(m, keyStage)
			delete(m, keyRevoked)
		})
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "Authorized as %s. The tokens are stored in Secret %s/%s and are not shown.\n", login, b.Config.Namespace, b.Config.State)
		return nil
	}
}

// Revoke stops every project's GitHub access and revokes the authorization
// at GitHub (DELETE /applications/{client_id}/grant), which invalidates the
// access and refresh tokens. Local access ends first; the command fails, and
// keeps the tokens for a retry, unless GitHub confirms the revocation.
func (b *Broker) Revoke(ctx context.Context, out io.Writer) error {
	if err := b.patch(ctx, b.Config.State, func(m map[string][]byte) { m[keyRevoked] = []byte("true") }); err != nil {
		return err
	}
	if err := b.publish(ctx, nil, "revoked"); err != nil {
		return err
	}
	fmt.Fprintln(out, "Project access disabled: no lease is published.")
	state, err := b.secret(ctx, b.Config.State)
	if err != nil {
		return err
	}
	token, expires := string(state.Data[keyAccess]), unix(state.Data[keyAccessExpires])
	if token != "" && !expires.After(b.now().Add(time.Minute)) && len(state.Data[keyRefresh]) > 0 {
		// GitHub identifies the grant by a valid token.
		t, err := b.refresh(ctx, string(state.Data[keyRefresh]))
		if err == nil {
			if err = b.store(ctx, t); err == nil {
				token = t.Access
			}
		}
	}
	if token == "" {
		fmt.Fprintln(out, "No stored authorization to revoke at GitHub. Check https://github.com/settings/apps/authorizations.")
		return nil
	}
	body, _ := json.Marshal(map[string]string{"access_token": token})
	req, err := http.NewRequestWithContext(ctx, "DELETE", b.api()+"/applications/"+url.PathEscape(b.Config.ClientID)+"/grant", strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.SetBasicAuth(b.Config.ClientID, b.Config.ClientSecret)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	res, err := b.http().Do(req)
	if err != nil || res.StatusCode != http.StatusNoContent {
		if res != nil {
			res.Body.Close()
		}
		fmt.Fprintln(out, "GitHub did NOT confirm the revocation. Revoke the App at https://github.com/settings/apps/authorizations, then run revoke again.")
		return errors.New("GitHub-side revocation not confirmed")
	}
	res.Body.Close()
	if err := b.patch(ctx, b.Config.State, func(m map[string][]byte) {
		for _, k := range []string{keyAccess, keyAccessExpires, keyRefresh, keyRefreshExpires, keyStage} {
			delete(m, k)
		}
	}); err != nil {
		return err
	}
	fmt.Fprintln(out, "GitHub revoked the authorization. Run authorize to resume.")
	return nil
}

// ConfigFromEnv reads the broker configuration.
func ConfigFromEnv() (Config, error) {
	c := Config{Namespace: os.Getenv("POD_NAMESPACE"), State: os.Getenv("GITHUB_BROKER_STATE"), Lease: os.Getenv("GITHUB_BROKER_LEASE"), ClientID: os.Getenv("GITHUB_APP_CLIENT_ID")}
	// The client ID is not secret, but reading it from the same Secret as the
	// client secret lets an operator configure the App in one step.
	if path := os.Getenv("GITHUB_APP_CLIENT_ID_FILE"); path != "" && c.ClientID == "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			return Config{}, errors.New("GITHUB_APP_CLIENT_ID_FILE unreadable")
		}
		c.ClientID = strings.TrimSpace(string(raw))
	}
	if path := os.Getenv("GITHUB_APP_CLIENT_SECRET_FILE"); path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			return Config{}, errors.New("GITHUB_APP_CLIENT_SECRET_FILE unreadable")
		}
		c.ClientSecret = strings.TrimSpace(string(raw))
	}
	if c.Namespace == "" || c.State == "" || c.Lease == "" || c.State == c.Lease || c.ClientID == "" || c.ClientSecret == "" || strings.ContainsAny(c.ClientID, "/?#% ") {
		return Config{}, errors.New("POD_NAMESPACE, GITHUB_BROKER_STATE, GITHUB_BROKER_LEASE, GITHUB_APP_CLIENT_ID (or _FILE) and GITHUB_APP_CLIENT_SECRET_FILE are required")
	}
	return c, nil
}

// Run is the github-broker command: "authorize", "revoke", or the renewal
// loop (no argument, or "once").
func Run(ctx context.Context, c client.Client, args []string) error {
	cfg, err := ConfigFromEnv()
	if err != nil {
		return err
	}
	b := &Broker{Client: c, Config: cfg}
	mode := ""
	if len(args) > 0 {
		mode = args[0]
	}
	switch mode {
	case "authorize":
		return b.Authorize(ctx, os.Stdout)
	case "revoke":
		return b.Revoke(ctx, os.Stdout)
	case "once":
		return b.Tick(ctx)
	case "":
	default:
		return errors.New("usage: github-broker [authorize|revoke|once]")
	}
	for {
		if err := b.Tick(ctx); err != nil {
			slog.WarnContext(ctx, "GitHub broker tick", "status", err.Error())
		}
		if err := b.sleep(ctx, 30*time.Second); err != nil {
			return nil
		}
	}
}
