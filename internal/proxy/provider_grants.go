package proxy

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"os"
	"strings"
	"time"
)

func providerForHost(host string) string {
	switch host {
	case "chatgpt.com":
		return "codex"
	case "cli-chat-proxy.grok.com":
		return "grok"
	case "api2.cursor.sh", "agentn.global.api5.cursor.sh":
		return "cursor"
	case "api.meta.ai":
		return "muse"
	}
	return ""
}

type providerGrant struct {
	Enabled bool `json:"enabled"`
	Access  struct {
		Token   string  `json:"accessToken"`
		Expires float64 `json:"expiresAt"`
		Issuer  string  `json:"issuer,omitempty"`
		Account string  `json:"chatgptAccountId,omitempty"`
		Plan    string  `json:"chatgptPlanType,omitempty"`
		// CellToken, when set, is what this cell's worker holds instead of
		// Token. The proxy accepts only it and forwards Token in its place.
		CellToken string `json:"cellToken,omitempty"`
	} `json:"access"`
}

// live reports an enabled grant with a token that is valid for over a minute.
func (g providerGrant) live() bool {
	return g.Enabled && g.Access.Token != "" && !math.IsNaN(g.Access.Expires) && !math.IsInf(g.Access.Expires, 0) && g.Access.Expires > float64(time.Now().Unix()+60)
}

// currentGrants rereads the mounted grant document. Unknown providers and
// fields fail closed.
func (s *Server) currentGrants() (map[string]providerGrant, error) {
	if s.ProviderGrants == nil {
		return nil, errors.New("provider authorization unavailable")
	}
	raw, err := s.ProviderGrants()
	if err != nil || len(raw) > 128<<10 {
		return nil, errors.New("provider authorization unavailable")
	}
	var grants map[string]providerGrant
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&grants) != nil {
		return nil, errors.New("provider authorization unavailable")
	}
	if d.Decode(new(any)) != io.EOF {
		return nil, errors.New("invalid provider grant document")
	}
	for provider := range grants {
		if provider != "codex" && provider != "grok" && provider != "cursor" && provider != "muse" {
			return nil, errors.New("unreviewed provider grant")
		}
	}
	return grants, nil
}

func (s *Server) providerAuthorization(r *http.Request) error {
	host := r.URL.Hostname()
	name := providerForHost(host)
	// A provider host reached outside the fixed policy can only have come
	// through a brokered route, which always needs a live matching grant.
	if name == "" || !s.Config.BrokeredProviders && s.inRules(host) {
		return nil
	}
	// Cursor's official desktop contract can renew using an access JWT. Never
	// allow worker account/renewal routes, even under a broad operator path rule.
	if name == "cursor" && (r.URL.Path == "/auth" || strings.HasPrefix(r.URL.Path, "/auth/") || r.URL.Path == "/oauth" || strings.HasPrefix(r.URL.Path, "/oauth/")) {
		return errors.New("provider account management belongs to the trusted owner")
	}
	grants, err := s.currentGrants()
	if err != nil {
		return err
	}
	if name == "codex" {
		if _, ok := grants[name]; !ok && !s.Config.IndependentCodex {
			return nil
		}
	} // Legacy Codex grants retain controller expiry checks.
	grant, ok := grants[name]
	if !ok || !grant.live() {
		return errors.New("provider authorization expired or revoked")
	}
	// Account credentials must match the current broker projection. Renewal cannot
	// leave an older credential usable after the gateway consumes its replacement.
	if grant.Access.CellToken != "" {
		// Per-cell token: the worker never held the real one. Only the cell
		// token is accepted (the real token sent from inside means it leaked
		// from somewhere else), and the real one goes upstream.
		if name != "grok" && name != "cursor" {
			return errors.New("cell tokens are reviewed only for grok and cursor")
		}
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+grant.Access.CellToken)) != 1 {
			return errors.New("provider credential does not match its current grant")
		}
		r.Header.Set("Authorization", "Bearer "+grant.Access.Token)
		return nil
	}
	if r.Header.Get("Authorization") != "Bearer "+grant.Access.Token {
		return errors.New("provider credential does not match its current grant")
	}
	return nil
}

func gatewayProviderGrants() ([]byte, error) { return os.ReadFile("/tls/provider-access") }
