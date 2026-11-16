package agentorchestrator

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestParseStatusRequiresLockedDownListener(t *testing.T) {
	ok := `{"enabled":true,"loopbackOnly":false,"hostId":"host-1","password":"never-decoded","requireDeviceCredentials":true}`
	s, err := ParseStatus([]byte(ok))
	if err != nil || s.HostID != "host-1" {
		t.Fatal(s, err)
	}
	for name, raw := range map[string]string{
		"off":           `{"enabled":false,"hostId":"h","requireDeviceCredentials":true}`,
		"loopback":      `{"enabled":true,"loopbackOnly":true,"hostId":"h","requireDeviceCredentials":true}`,
		"password ok":   `{"enabled":true,"hostId":"h","requireDeviceCredentials":false}`,
		"no host":       `{"enabled":true,"hostId":"","requireDeviceCredentials":true}`,
		"host with gap": `{"enabled":true,"hostId":"a b","requireDeviceCredentials":true}`,
		"garbage":       `not json`,
	} {
		if _, err := ParseStatus([]byte(raw)); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

func TestParsePairingCode(t *testing.T) {
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	good := `{"code":"ABCD-1234","expiresAt":"2026-11-15T12:10:00Z"}`
	if p, err := ParsePairingCode([]byte(good), now); err != nil || p.Code != "ABCD-1234" {
		t.Fatal(p, err)
	}
	for name, raw := range map[string]string{
		"empty":   `{"code":"","expiresAt":"2026-11-15T12:10:00Z"}`,
		"expired": `{"code":"x","expiresAt":"2026-11-15T11:59:00Z"}`,
		"too far": `{"code":"x","expiresAt":"2026-11-16T12:00:00Z"}`,
		"spaces":  `{"code":"a b","expiresAt":"2026-11-15T12:10:00Z"}`,
	} {
		if _, err := ParsePairingCode([]byte(raw), now); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

// The offer must decode the way AO's phone app parses it
// (packages/mobile/lib/pairingCode.ts): unpadded base64url JSON, v 2, one
// secure endpoint, link payload in the fragment.
func TestOfferMatchesAOPairingCodeFormat(t *testing.T) {
	o, err := NewOffer("scrapecast", "ao.example", "host-1", "CODE")
	if err != nil {
		t.Fatal(err)
	}
	code, link, err := o.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(code, "+/=") || link != "aomobile://pair#"+code {
		t.Fatal(code, link)
	}
	raw, err := base64.RawURLEncoding.DecodeString(code)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	endpoints := got["endpoints"].([]any)
	e := endpoints[0].(map[string]any)
	if got["v"] != float64(2) || got["hostId"] != "host-1" || got["token"] != "CODE" || len(endpoints) != 1 || e["host"] != "scrapecast.ao.example" || e["port"] != float64(443) || e["secure"] != true || e["kind"] != "lan" {
		t.Fatalf("%v", got)
	}
	if _, err := NewOffer("scrapecast", "", "host-1", "CODE"); err == nil {
		t.Fatal("offer without a domain")
	}
}

func TestLoopbackCommandsStayOffTheProxy(t *testing.T) {
	for _, cmd := range [][]string{StatusCommand(), PairingCodeCommand()} {
		joined := strings.Join(cmd, " ")
		if !strings.Contains(joined, "--noproxy *") || !strings.Contains(joined, "http://127.0.0.1:3001/api/v1/mobile/") {
			t.Fatal(joined)
		}
	}
}
