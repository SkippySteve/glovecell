package t3

// This client uses the stock T3 v0.0.45 environment-link API. Account OAuth
// credentials are consumed only in the trusted controller, never by workers.
import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const DefaultRelayURL = "https://relay.t3.codes"

type TokenSource interface {
	Token(context.Context) (string, error)
}
type Account struct {
	Binary, Directory string
	mu                sync.Mutex
}

func (a *Account) Token(ctx context.Context) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	filename := filepath.Join(a.Directory, "userdata", "secrets", "cloud-cli-oauth-token.bin")
	read := func() (string, int64, error) {
		stat, e := os.Lstat(filename)
		if e != nil {
			return "", 0, errors.New("T3 Connect account authorization required")
		}
		if !stat.Mode().IsRegular() || stat.Mode().Perm()&0077 != 0 {
			return "", 0, errors.New("T3 account credential must be a private regular file")
		}
		b, e := os.ReadFile(filename)
		if e != nil {
			return "", 0, errors.New("T3 account credential unavailable")
		}
		var token struct {
			AccessToken string `json:"accessToken"`
			Expires     int64  `json:"expiresAtEpochMs"`
		}
		if json.Unmarshal(b, &token) != nil || token.AccessToken == "" {
			return "", 0, errors.New("invalid T3 account credential")
		}
		return token.AccessToken, token.Expires, nil
	}
	token, expires, e := read()
	if e != nil {
		return "", e
	}
	if expires > time.Now().Add(time.Minute).UnixMilli() {
		return token, nil
	}
	// Delegate refresh and its cross-process credential lease to the stock CLI.
	// A revoked grant can start device authorization; bound that operation and
	// suppress its prompts here. Reauthorization is an explicit operator action.
	refreshCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(refreshCtx, a.Binary, "connect", "login", "--headless", "--base-dir", a.Directory)
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if cmd.Run() != nil {
		return "", errors.New("T3 account refresh failed; authorize the controller again")
	}
	token, expires, e = read()
	if e != nil {
		return "", e
	}
	if expires <= time.Now().UnixMilli() {
		return "", errors.New("T3 account authorization expired")
	}
	return token, nil
}

type Cloud struct {
	URL    string
	Tokens TokenSource
	HTTP   *http.Client
}
type Endpoint struct {
	HTTP string `json:"httpBaseUrl"`
	WS   string `json:"wsBaseUrl"`
	Kind string `json:"providerKind"`
}
type Tunnel struct {
	Kind  string `json:"providerKind"`
	Token string `json:"connectorToken"`
	ID    string `json:"tunnelId"`
	Name  string `json:"tunnelName"`
}
type Link struct {
	OK            bool     `json:"ok"`
	UserID        string   `json:"cloudUserId"`
	EnvironmentID string   `json:"environmentId"`
	Endpoint      Endpoint `json:"endpoint"`
	Runtime       *Tunnel  `json:"endpointRuntime"`
	Issuer        string   `json:"relayIssuer"`
	Credential    string   `json:"environmentCredential"`
	MintKey       string   `json:"cloudMintPublicKey"`
}
type Environment struct {
	ID       string   `json:"environmentId"`
	Label    string   `json:"label"`
	Endpoint Endpoint `json:"endpoint"`
}

func (c *Cloud) Validate() error {
	u, e := url.Parse(c.URL)
	if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || c.Tokens == nil {
		return errors.New("T3 Connect requires an HTTPS relay origin and controller authorization")
	}
	return nil
}
func (c *Cloud) request(ctx context.Context, method, path string, payload, result any) error {
	if e := c.Validate(); e != nil {
		return e
	}
	token, e := c.Tokens.Token(ctx)
	if e != nil {
		return e
	}
	var body io.Reader
	if payload != nil {
		b, e := json.Marshal(payload)
		if e != nil {
			return e
		}
		body = bytes.NewReader(b)
	}
	req, e := http.NewRequestWithContext(ctx, method, c.URL+path, body)
	if e != nil {
		return e
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("User-Agent", "agent-project-controller/t3-connect")
	h := c.HTTP
	if h == nil {
		h = &http.Client{Timeout: 120 * time.Second}
	}
	// Never follow a redirect with account credentials or a signed link proof.
	copyClient := *h
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, e := copyClient.Do(req)
	if e != nil {
		return errors.New("T3 Connect relay unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("T3 Connect relay rejected %s: HTTP %d", method, resp.StatusCode)
	}
	if e = json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(result); e != nil {
		return errors.New("invalid T3 Connect relay response")
	}
	return nil
}
func (c *Cloud) Challenge(ctx context.Context) (string, error) {
	var out struct {
		Challenge string `json:"challenge"`
	}
	e := c.request(ctx, http.MethodPost, "/v1/client/environment-link-challenges", cloudFlags(""), &out)
	if e == nil && out.Challenge == "" {
		e = errors.New("empty T3 link challenge")
	}
	return out.Challenge, e
}
func cloudFlags(proof string) map[string]any {
	m := map[string]any{"notificationsEnabled": false, "liveActivitiesEnabled": false, "managedTunnelsEnabled": true}
	if proof != "" {
		m["proof"] = proof
	}
	return m
}
func (c *Cloud) Link(ctx context.Context, proof, id string) (Link, error) {
	var out Link
	e := c.request(ctx, http.MethodPost, "/v1/client/environment-links", cloudFlags(proof), &out)
	if e != nil {
		return out, e
	}
	if !out.OK || out.EnvironmentID != id || out.UserID == "" || out.Credential == "" || out.MintKey == "" || out.Issuer != c.URL || out.Runtime == nil || out.Runtime.Kind != "cloudflare_tunnel" || out.Runtime.Token == "" || out.Runtime.ID == "" || out.Endpoint.Kind != "cloudflare_tunnel" {
		return Link{}, errors.New("T3 relay returned a mismatched or unsupported environment link")
	}
	if e = validateEndpoint(out.Endpoint); e != nil {
		return Link{}, e
	}
	return out, nil
}
func validateEndpoint(endpoint Endpoint) error {
	h, e := url.Parse(endpoint.HTTP)
	if e != nil {
		return errors.New("invalid T3 endpoint")
	}
	w, e := url.Parse(endpoint.WS)
	if e != nil {
		return errors.New("invalid T3 endpoint")
	}
	if h.Scheme != "https" || !strings.HasSuffix(h.Hostname(), ".t3coderelay.com") || h.Port() != "" || h.User != nil || h.RawQuery != "" || h.Fragment != "" || (h.Path != "" && h.Path != "/") || w.Scheme != "wss" || w.Host != h.Host || w.User != nil || w.RawQuery != "" || w.Fragment != "" || w.Path != "/ws" {
		return errors.New("unsupported T3 managed endpoint")
	}
	return nil
}
func (c *Cloud) List(ctx context.Context) ([]Environment, error) {
	var out struct {
		Environments []Environment `json:"environments"`
	}
	e := c.request(ctx, http.MethodGet, "/v1/environments", nil, &out)
	return out.Environments, e
}
func (c *Cloud) Revoke(ctx context.Context, id string) error {
	if id == "" || strings.ContainsAny(id, "/?#") {
		return errors.New("invalid T3 environment identity")
	}
	var out struct {
		OK bool `json:"ok"`
	}
	deleteErr := c.request(ctx, http.MethodDelete, "/v1/client/environment-links/"+url.PathEscape(id), nil, &out)
	// The relay can return an error after applying DELETE. The authoritative
	// account catalog, queried with the same account credential, is the proof
	// of revocation. A failed catalog request never establishes completion.
	list, e := c.List(ctx)
	if e != nil {
		return errors.Join(deleteErr, e)
	}
	for _, env := range list {
		if env.ID == id {
			return errors.Join(deleteErr, errors.New("T3 environment revocation not verified"))
		}
	}
	return nil
}

// Validate the worker-signed JWT before spending the account's environment
// grant. The first public key is pinned in the durable project ledger.
func verifyProof(proof, id, challenge, issuer, origin, pinnedKey string) (string, error) {
	parts := strings.Split(proof, ".")
	if len(parts) != 3 {
		return "", errors.New("invalid T3 link proof")
	}
	header, e := base64.RawURLEncoding.DecodeString(parts[0])
	if e != nil {
		return "", errors.New("invalid T3 link proof")
	}
	var alg struct {
		Algorithm string `json:"alg"`
		Type      string `json:"typ"`
	}
	if json.Unmarshal(header, &alg) != nil || (alg.Algorithm != "EdDSA" || alg.Type != "t3-env-link+jwt") {
		return "", errors.New("unsupported T3 signing algorithm")
	}
	payload, e := base64.RawURLEncoding.DecodeString(parts[1])
	if e != nil {
		return "", errors.New("invalid T3 link proof")
	}
	var p struct {
		EnvironmentID string   `json:"environmentId"`
		Subject       string   `json:"sub"`
		Issuer        string   `json:"iss"`
		Audience      string   `json:"aud"`
		Challenge     string   `json:"challenge"`
		PublicKey     string   `json:"environmentPublicKey"`
		Endpoint      Endpoint `json:"endpoint"`
		Origin        struct {
			Host string `json:"localHttpHost"`
			Port int    `json:"localHttpPort"`
		} `json:"origin"`
		Descriptor struct {
			ID string `json:"environmentId"`
		} `json:"descriptor"`
		Issued  int64    `json:"iat"`
		Expires int64    `json:"exp"`
		Scopes  []string `json:"scopes"`
	}
	if json.Unmarshal(payload, &p) != nil {
		return "", errors.New("invalid T3 link proof")
	}
	u, e := url.Parse(origin)
	if e != nil {
		return "", e
	}
	now := time.Now().Unix()
	expectedWS := "ws://" + u.Host
	if p.EnvironmentID != id || p.Subject != id || p.Descriptor.ID != id || p.Issuer != "t3-env:"+id || p.Audience != issuer || p.Challenge != challenge || p.Endpoint.HTTP != origin || p.Endpoint.WS != expectedWS || p.Endpoint.Kind != "cloudflare_tunnel" || p.Origin.Host != "127.0.0.1" || fmt.Sprint(p.Origin.Port) != u.Port() || p.Expires <= now || p.Issued > now+30 || p.Expires-p.Issued > 300 || p.Expires <= p.Issued || len(p.Scopes) != 2 || p.Scopes[0] != "agent_activity_notifications" || p.Scopes[1] != "managed_tunnels" {
		return "", errors.New("T3 link proof does not match the requested project")
	}
	key := strings.TrimSpace(p.PublicKey)
	if pinnedKey != "" && strings.TrimSpace(pinnedKey) != key {
		return "", errors.New("T3 environment signing key changed")
	}
	block, _ := pem.Decode([]byte(key))
	if block == nil {
		return "", errors.New("invalid T3 environment signing key")
	}
	public, e := x509.ParsePKIXPublicKey(block.Bytes)
	if e != nil {
		return "", errors.New("invalid T3 environment signing key")
	}
	ed, ok := public.(ed25519.PublicKey)
	if !ok {
		return "", errors.New("unsupported T3 environment signing key")
	}
	signature, e := base64.RawURLEncoding.DecodeString(parts[2])
	if e != nil || !ed25519.Verify(ed, []byte(parts[0]+"."+parts[1]), signature) {
		return "", errors.New("T3 link proof signature rejected")
	}
	return key, nil
}

// Snapshot one account credential for the whole link or revoke transaction.
// An operator changing accounts mid-operation cannot make another account's
// empty catalog look like successful revocation of the original registration.
type accountToken string

func (t accountToken) Token(context.Context) (string, error) { return string(t), nil }
func (c *Cloud) ForAccount(ctx context.Context, expected string) (*Cloud, string, error) {
	token, e := c.Tokens.Token(ctx)
	if e != nil {
		return nil, "", e
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, "", errors.New("T3 OAuth account identity unavailable")
	}
	payload, e := base64.RawURLEncoding.DecodeString(parts[1])
	if e != nil {
		return nil, "", errors.New("T3 OAuth account identity unavailable")
	}
	var claims struct {
		Subject string `json:"sub"`
	}
	if json.Unmarshal(payload, &claims) != nil || claims.Subject == "" {
		return nil, "", errors.New("T3 OAuth account identity unavailable")
	}
	if expected != "" && expected != claims.Subject {
		return nil, "", errors.New("T3 account changed; original account authorization required for this project")
	}
	snapshot := *c
	snapshot.Tokens = accountToken(token)
	return &snapshot, claims.Subject, nil
}
