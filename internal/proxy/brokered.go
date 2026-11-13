package proxy

import (
	"bytes"
	"errors"
	"os"
	"slices"
	"strings"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
)

// BrokeredProviders lists the subscription providers a project can opt into
// with spec.providers, and the exact upstream hosts each one needs. The
// controller opens these hosts for the project's proxy only (never the worker)
// and only while the project opts in. The proxy then admits a request to them
// only while that project's own grant for the provider is enabled, unexpired
// and matches the request's bearer token.
var BrokeredProviders = map[string][]string{
	"grok":   {"cli-chat-proxy.grok.com"},
	"cursor": {"api2.cursor.sh", "agentn.global.api5.cursor.sh"},
	"codex":  {"chatgpt.com"},
	"claude": {"api.anthropic.com"},
}

// brokeredRoutes are the methods and literal path prefixes a brokered
// provider's CLI uses. Grok's CLI API lives under /v1. Cursor uses Connect RPC
// service paths, so its whole host is listed; providerAuthorization still
// refuses Cursor's account and renewal routes (/auth, /oauth) on every request.
// Codex uses the ChatGPT backend's Codex and account (wham) APIs. Claude's
// prefixes are narrowed further to exact routes by claudeRoutes.
var brokeredRoutes = map[string]api.HTTPSRule{
	"chatgpt.com":                  {Host: "chatgpt.com", Methods: []string{"GET", "POST"}, Paths: []string{"/backend-api/codex", "/backend-api/wham"}},
	"api.anthropic.com":            {Host: "api.anthropic.com", Methods: []string{"GET", "POST"}, Paths: []string{"/v1/messages", "/api/hello", "/api/oauth", "/api/claude_code"}},
	"cli-chat-proxy.grok.com":      {Host: "cli-chat-proxy.grok.com", Methods: []string{"GET", "POST"}, Paths: []string{"/v1"}},
	"api2.cursor.sh":               {Host: "api2.cursor.sh", Methods: []string{"GET", "POST"}, Paths: []string{"/"}},
	"agentn.global.api5.cursor.sh": {Host: "agentn.global.api5.cursor.sh", Methods: []string{"GET", "POST"}, Paths: []string{"/"}},
}

// BrokeredProviderKey is the proxy-ca Secret key (mounted at /tls) that lists
// the providers this project opted into, comma separated. The controller
// writes it; it is absent for projects without spec.providers.
const BrokeredProviderKey = "brokered-providers"

// ParseBrokeredProviders reads that list. Unknown or repeated names fail
// closed: the proxy then admits no brokered route at all.
func ParseBrokeredProviders(raw []byte) ([]string, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil, nil
	}
	if len(raw) > 256 {
		return nil, errors.New("brokered provider list too large")
	}
	var names []string
	for _, name := range strings.Split(string(raw), ",") {
		if _, ok := BrokeredProviders[name]; !ok || slices.Contains(names, name) {
			return nil, errors.New("unreviewed brokered provider")
		}
		names = append(names, name)
	}
	return names, nil
}

// brokeredProvider returns the provider whose reviewed host this is, if the
// project opted into it and its current grant is live. Each call rereads the
// mounted files, so a revocation applies to the next request.
func (s *Server) brokeredProvider(host string) string {
	name := providerForHost(host)
	if _, ok := BrokeredProviders[name]; !ok || s.BrokeredList == nil || s.ProviderGrants == nil {
		return ""
	}
	raw, err := s.BrokeredList()
	if err != nil {
		return ""
	}
	names, err := ParseBrokeredProviders(raw)
	if err != nil || !slices.Contains(names, name) || !slices.Contains(BrokeredProviders[name], host) {
		return ""
	}
	grants, err := s.currentGrants()
	if err != nil {
		return ""
	}
	if grant, ok := grants[name]; !ok || !grant.live() {
		return ""
	}
	return name
}

// brokeredAllowed admits a request on a brokered route. providerAuthorization
// then checks its bearer token against the same grant.
func (s *Server) brokeredAllowed(host, method, path string) bool {
	rule, ok := brokeredRoutes[host]
	if !ok || s.brokeredProvider(host) == "" {
		return false
	}
	return Allowed(Config{Rules: []api.HTTPSRule{rule}}, host, method, path)
}

// inRules reports whether the project's fixed policy names host.
func (s *Server) inRules(host string) bool {
	if s.Config.VendorTelemetry && slices.Contains(VendorTelemetryHosts(), host) {
		return true
	}
	for _, rule := range s.Config.Rules {
		if hostMatches(rule.Host, host) {
			return true
		}
	}
	return false
}

func gatewayBrokeredProviders() ([]byte, error) {
	raw, err := os.ReadFile("/tls/" + BrokeredProviderKey)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return raw, err
}
