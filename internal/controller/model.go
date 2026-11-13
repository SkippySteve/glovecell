package controller

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// RejectRefreshMaterial refuses an operator model Secret that carries a refresh token.
// Access material may be mounted at /run/model. The controller never copies an owner refresh token into it.
func RejectRefreshMaterial(data map[string][]byte) error {
	for key := range data {
		folded := strings.ToLower(strings.NewReplacer("_", "", "-", "").Replace(key))
		if folded == "refreshtoken" {
			return errors.New("model access must not include a refresh token")
		}
		switch key {
		case "access-token", "chatgpt-account-id", "chatgpt-plan-type", "expires-at":
		case "provider-access":
			if err := validateProviderAccess(data[key]); err != nil {
				return err
			}
		default:
			return errors.New("model access contains an unreviewed field")
		}
	}
	if brokeredOnly(data) {
		return nil
	}
	token, account := string(data["access-token"]), string(data["chatgpt-account-id"])
	if token == "" || len(token) > 32768 || !bearerToken.MatchString(token) || account == "" || len(account) > 256 || strings.ContainsAny(account, "\r\n\t {}[]\"") {
		return errors.New("model access requires bounded access and account fields")
	}
	plan := string(data["chatgpt-plan-type"])
	if len(plan) > 256 || strings.ContainsAny(plan, "\r\n\x00") {
		return errors.New("invalid model access plan")
	}
	expires, err := strconv.ParseFloat(string(data["expires-at"]), 64)
	if err != nil || math.IsNaN(expires) || math.IsInf(expires, 0) || expires <= 0 || (!independentCodex(data) && expires <= float64(time.Now().Unix()+60)) {
		return errors.New("model access is expired or lacks a valid expiry")
	}
	return nil
}

var bearerToken = regexp.MustCompile(`^[A-Za-z0-9._~+/=-]+$`)

func validateProviderAccess(raw []byte) error {
	if len(raw) > 128<<10 {
		return errors.New("provider grants too large")
	}
	var values map[string]struct {
		Enabled bool `json:"enabled"`
		Access  *struct {
			Token   string  `json:"accessToken"`
			Expires float64 `json:"expiresAt"`
			Issuer  string  `json:"issuer,omitempty"`
			Account string  `json:"chatgptAccountId,omitempty"`
			Plan    string  `json:"chatgptPlanType,omitempty"`
		} `json:"access,omitempty"`
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&values) != nil || values == nil {
		return errors.New("access-only provider grants required")
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("invalid provider grant document")
	}
	for provider, value := range values {
		if provider != "codex" && provider != "grok" && provider != "cursor" && provider != "muse" && provider != "claude" {
			return errors.New("unreviewed provider grant")
		}
		if !value.Enabled {
			if value.Access != nil {
				return errors.New("revoked provider retained credentials")
			}
			continue
		}
		if value.Access == nil || value.Access.Token == "" || len(value.Access.Token) > 32768 || math.IsNaN(value.Access.Expires) || math.IsInf(value.Access.Expires, 0) || value.Access.Expires <= 0 {
			return errors.New("invalid provider grant")
		}
		for _, c := range value.Access.Token {
			if c < 33 || c > 126 {
				return errors.New("invalid provider access token")
			}
		}
		if provider == "codex" && (value.Access.Account == "" || len(value.Access.Account) > 256 || strings.ContainsAny(value.Access.Account, "\r\n\t {}[]\"")) {
			return errors.New("invalid Codex account")
		}
		if provider != "codex" && (value.Access.Account != "" || value.Access.Plan != "") {
			return errors.New("unreviewed provider account fields")
		}
		if provider == "grok" && value.Access.Issuer != "https://auth.x.ai" || provider != "grok" && value.Access.Issuer != "" {
			return errors.New("invalid provider issuer")
		}
		if provider == "claude" && !strings.HasPrefix(value.Access.Token, "sk-ant-oat01-") {
			return errors.New("invalid Claude subscription token")
		}
		if provider == "muse" && value.Access.Token != "apc-muse-subscription-gateway" {
			return errors.New("Muse subscription credential must remain at its trusted gateway")
		}
	}
	return nil
}

// Presence of a reviewed Codex entry selects independent provider lifecycle.
// Expiry and explicit revocation deny Codex at its adapter and gateway, preserving
// other providers and project storage. Legacy operator grants keep their behavior.
func independentCodex(data map[string][]byte) bool {
	var grants map[string]json.RawMessage
	if json.Unmarshal(data["provider-access"], &grants) != nil {
		return false
	}
	_, ok := grants["codex"]
	return ok
}

// brokeredOnly reports a Secret that carries only access-only provider grants
// (the controller's brokered leases, Codex included). It has no whole-worker
// expiry: each provider entry expires on its own at the launcher and the
// gateway, and its removal never stops the project. Operator Codex grants
// always carry the access-token, account and expiry keys as well, so they
// never look brokered.
func brokeredOnly(data map[string][]byte) bool {
	if len(data) != 1 || len(data["provider-access"]) == 0 {
		return false
	}
	var grants map[string]json.RawMessage
	if json.Unmarshal(data["provider-access"], &grants) != nil {
		return false
	}
	return grants != nil
}
