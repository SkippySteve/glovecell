package hubadmin

import (
	"strings"
	"testing"
	"time"
)

func TestValidateEndpointRequiresExplicitWSSOrigin(t *testing.T) {
	if err := ValidateEndpoint("wss://hub.example.com"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "https://hub.example.com", "wss://user@hub.example.com", "wss://hub.example.com/path", "wss://hub.example.com?x=1"} {
		if ValidateEndpoint(bad) == nil {
			t.Fatal(bad)
		}
	}
}

func TestValidateOfferRejectsExpiredAndCrossEndpointOffers(t *testing.T) {
	now := time.UnixMilli(1_700_000_000_000)
	good := Offer{Available: true, Endpoint: "wss://hub.example.com", PairingURL: "orca://pair?code=abc", DeviceID: "phone-1", ExpiresAt: now.Add(2 * time.Minute).UnixMilli(), QRDataURL: "data:image/png;base64,abc"}
	if err := ValidateOffer(good, "wss://hub.example.com", now); err != nil {
		t.Fatal(err)
	}
	expired := good
	expired.ExpiresAt = now.Add(-time.Second).UnixMilli()
	if ValidateOffer(expired, "wss://hub.example.com", now) == nil {
		t.Fatal("accepted expired offer")
	}
	wrong := good
	wrong.Endpoint = "wss://other.example.com"
	if ValidateOffer(wrong, "wss://hub.example.com", now) == nil {
		t.Fatal("accepted mismatched endpoint")
	}
	unavailable := Offer{Available: false, PairingURL: "orca://pair?code=abc"}
	if ValidateOffer(unavailable, "wss://hub.example.com", now) == nil {
		t.Fatal("accepted unavailable offer with pairing URL")
	}
}

func TestValidateDevicesRejectsDuplicatesAndOversizedNames(t *testing.T) {
	if err := ValidateDevices(Devices{Devices: []Device{{DeviceID: "a", Name: "phone"}}}); err != nil {
		t.Fatal(err)
	}
	if ValidateDevices(Devices{Devices: []Device{{DeviceID: "a"}, {DeviceID: "a"}}}) == nil {
		t.Fatal("accepted duplicate device id")
	}
	if ValidateDevices(Devices{Devices: []Device{{DeviceID: "a", Name: strings.Repeat("n", 257)}}}) == nil {
		t.Fatal("accepted oversized name")
	}
}
