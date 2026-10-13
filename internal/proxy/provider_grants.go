package proxy

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"os"
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

func (s *Server) providerAuthorization(r *http.Request) error {
	name := providerForHost(r.URL.Hostname())
	if !s.Config.BrokeredProviders || name == "" {
		return nil
	}
	if s.ProviderGrants == nil {
		return errors.New("provider authorization unavailable")
	}
	raw, err := s.ProviderGrants()
	if err != nil || len(raw) > 128<<10 {
		return errors.New("provider authorization unavailable")
	}
	var grants map[string]struct {
		Enabled bool `json:"enabled"`
		Access  struct {
			Token   string  `json:"accessToken"`
			Expires float64 `json:"expiresAt"`
			Issuer  string  `json:"issuer,omitempty"`
			Account string  `json:"chatgptAccountId,omitempty"`
			Plan    string  `json:"chatgptPlanType,omitempty"`
		} `json:"access"`
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&grants) != nil {
		return errors.New("provider authorization unavailable")
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("invalid provider grant document")
	}
	for provider := range grants {
		if provider != "codex" && provider != "grok" && provider != "cursor" && provider != "muse" {
			return errors.New("unreviewed provider grant")
		}
	}
	if name == "codex" {
		if _, ok := grants[name]; !ok && !s.Config.IndependentCodex {
			return nil
		}
	} // Legacy Codex grants retain controller expiry checks.
	grant, ok := grants[name]
	if !ok || !grant.Enabled || grant.Access.Token == "" || math.IsNaN(grant.Access.Expires) || math.IsInf(grant.Access.Expires, 0) || grant.Access.Expires <= float64(time.Now().Unix()+60) {
		return errors.New("provider authorization expired or revoked")
	}
	// Account credentials must match the current broker projection. Renewal cannot
	// leave an older credential usable after the gateway consumes its replacement.
	if r.Header.Get("Authorization") != "Bearer "+grant.Access.Token {
		return errors.New("provider credential does not match its current grant")
	}
	return nil
}

func gatewayProviderGrants() ([]byte, error) { return os.ReadFile("/tls/provider-access") }
