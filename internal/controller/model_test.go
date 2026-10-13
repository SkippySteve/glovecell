package controller

import (
	"strconv"
	"testing"
	"time"
)

func validModelAccess() map[string][]byte {
	return map[string][]byte{"access-token": []byte("fixture-access"), "chatgpt-account-id": []byte("fixture-account"), "expires-at": []byte(strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10))}
}

func TestRejectRefreshMaterial(t *testing.T) {
	if err := RejectRefreshMaterial(validModelAccess()); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"refresh-token", "refresh_token", "refreshToken", "auth.json", "tokens", "id-token"} {
		data := validModelAccess()
		data[key] = []byte(`{"refresh_token":"do-not-deliver"}`)
		if err := RejectRefreshMaterial(data); err == nil {
			t.Fatal("accepted credential container", key)
		}
	}
	for _, expiry := range []string{"", "1", "NaN", "+Inf", "-Inf", "1e9999"} {
		data := validModelAccess()
		data["expires-at"] = []byte(expiry)
		if err := RejectRefreshMaterial(data); err == nil {
			t.Fatal("accepted invalid expiry", expiry)
		}
	}
	for _, token := range []string{"", "x\ny", `{"refresh_token":"nested"}`} {
		data := validModelAccess()
		data["access-token"] = []byte(token)
		if err := RejectRefreshMaterial(data); err == nil {
			t.Fatal("accepted malformed access material")
		}
	}
}

func TestProviderAccessOnlyValidation(t *testing.T) {
	valid := `{"grok":{"enabled":true,"access":{"accessToken":"fixture-access","expiresAt":1,"issuer":"https://auth.x.ai"}},"cursor":{"enabled":false},"muse":{"enabled":true,"access":{"accessToken":"apc-muse-subscription-gateway","expiresAt":1}}}`
	// Expiry is a per-provider launcher decision, not a whole-workbench pause.
	if err := validateProviderAccess([]byte(valid)); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		`{"cursor":{"enabled":true,"access":{"accessToken":"x","expiresAt":100,"refreshToken":"never-deliver"}}}`,
		`{"cursor":{"enabled":true,"access":{"accessToken":"x","expiresAt":0}}}`,
		`{"muse":{"enabled":true,"access":{"accessToken":"long-lived-subscription-key","expiresAt":100}}}`,
		`{"grok":{"enabled":true,"access":{"accessToken":"x","expiresAt":100,"issuer":"https://other.example"}}}`,
		`{"unreviewed":{"enabled":false}}`, valid + `{}`,
	} {
		if err := validateProviderAccess([]byte(raw)); err == nil {
			t.Fatal("accepted unsafe provider material")
		}
	}
}
