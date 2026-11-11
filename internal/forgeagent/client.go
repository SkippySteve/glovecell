package forgeagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/forgejo"
)

// Client is the controller's forgejo.Host backed by the forge agent. It holds
// no Forgejo credential, only its own projected ServiceAccount token.
type Client struct {
	URL string
	// TokenFile is the projected token (audience Audience). It is reread on
	// every call because the kubelet rotates it.
	TokenFile string
	// Origin and Owner are the Forgejo settings the controller reports and
	// validates against; the agent enforces Owner itself too.
	Origin, Owner string
	HTTP          *http.Client
}

var _ forgejo.Host = (*Client)(nil)

// NewClient checks the settings. agentURL must be an http(s) URL with no
// credentials, query or path beyond "/".
func NewClient(agentURL, tokenFile, origin, owner string) (*Client, error) {
	u, err := url.Parse(agentURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("FORGE_AGENT_URL must be an http(s) origin")
	}
	if strings.TrimSpace(tokenFile) == "" {
		return nil, errors.New("FORGE_AGENT_TOKEN_FILE is required with FORGE_AGENT_URL")
	}
	if _, err := forgejo.New(origin, owner, "unused"); err != nil {
		return nil, err
	}
	return &Client{URL: strings.TrimSuffix(agentURL, "/"), TokenFile: tokenFile, Origin: strings.TrimSuffix(origin, "/"), Owner: owner,
		HTTP: &http.Client{Timeout: 60 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (c *Client) call(ctx context.Context, path string, body, out any) error {
	token, err := os.ReadFile(c.TokenFile)
	if err != nil || len(bytes.TrimSpace(token)) == 0 {
		return errors.New("forge agent token unavailable")
	}
	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+string(bytes.TrimSpace(token)))
	req.Header.Set("Content-Type", "application/json")
	res, err := c.HTTP.Do(req)
	if err != nil {
		return errors.New("forge agent unreachable")
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return errors.New("forge agent response unreadable")
	}
	if res.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &e) == nil && e.Error != "" {
			return fmt.Errorf("forge agent: %s", e.Error)
		}
		return fmt.Errorf("forge agent returned HTTP %d", res.StatusCode)
	}
	if json.Unmarshal(data, out) != nil {
		return errors.New("forge agent response invalid")
	}
	return nil
}

func (c *Client) Discover(ctx context.Context) ([]forgejo.Repository, error) {
	var out struct {
		Repositories []forgejo.Repository `json:"repositories"`
	}
	if err := c.call(ctx, "/v1/discover", struct{}{}, &out); err != nil {
		return nil, err
	}
	return out.Repositories, nil
}

func (c *Client) Grant(ctx context.Context, repo string, cred forgejo.Credentials) (string, error) {
	if err := forgejo.ValidateRepository(repo, c.Owner); err != nil {
		return "", err
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := c.call(ctx, "/v1/grant", grantRequest{Repository: repo, Username: cred.Username, Password: cred.Password, Token: cred.Token}, &out); err != nil {
		return "", err
	}
	if out.Token == "" {
		return "", errors.New("forge agent did not return token material")
	}
	return out.Token, nil
}

func (c *Client) Revoke(ctx context.Context, repo string, cred forgejo.Credentials) error {
	if err := forgejo.ValidateRepository(repo, c.Owner); err != nil {
		return err
	}
	var out struct{}
	return c.call(ctx, "/v1/revoke", grantRequest{Repository: repo, Username: cred.Username, Password: cred.Password, Token: cred.Token}, &out)
}
