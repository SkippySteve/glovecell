package proxy

import (
	"errors"
	"net/http"
	"strings"
)

// Claude Code with a Claude subscription. The worker only ever holds its
// cell's stand-in token; the proxy swaps in the subscription's OAuth token on
// these exact routes (the same set the sandbox-control gateway reviewed), and
// refuses everything else on api.anthropic.com, including API-key requests.
var claudeRoutes = map[string]string{
	"/v1/messages":                   "POST",
	"/v1/messages/count_tokens":      "POST",
	"/api/hello":                     "GET",
	"/api/oauth/profile":             "GET",
	"/api/oauth/usage":               "GET",
	"/api/oauth/account/settings":    "GET",
	"/api/claude_code/settings":      "GET",
	"/api/claude_code/policy_limits": "GET",
}

const claudeOAuthBeta = "oauth-2025-04-20"

func claudeRequest(r *http.Request) error {
	if claudeRoutes[r.URL.Path] != r.Method || r.URL.RawQuery != "" && r.URL.RawQuery != "beta=true" {
		return errors.New("unreviewed Claude subscription route")
	}
	if r.Header.Get("X-Api-Key") != "" {
		return errors.New("Claude API keys are not brokered")
	}
	return nil
}

// claudeOAuthHeaders marks a swapped request as a subscription (OAuth) call.
func claudeOAuthHeaders(r *http.Request) {
	r.Header.Del("Cookie")
	r.Header.Del("X-Api-Key")
	beta := r.Header.Get("Anthropic-Beta")
	if !strings.Contains(beta, claudeOAuthBeta) {
		if beta != "" {
			beta += ","
		}
		r.Header.Set("Anthropic-Beta", beta+claudeOAuthBeta)
	}
}
