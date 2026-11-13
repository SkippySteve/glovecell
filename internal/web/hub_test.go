package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/hubadmin"
)

type fakeHub struct {
	offer   hubadmin.Offer
	devices hubadmin.Devices
}

func (f *fakeHub) Pair(context.Context) (hubadmin.Offer, error) { return f.offer, nil }
func (f *fakeHub) Devices(context.Context) (hubadmin.Devices, error) {
	return f.devices, nil
}
func (f *fakeHub) Revoke(context.Context, string) (bool, error) { return true, nil }

func TestHubPairingRequiresOIDCAndSetsNoStore(t *testing.T) {
	now := time.Now()
	backend := &fakeHub{offer: hubadmin.Offer{
		Available:  true,
		Endpoint:   "wss://hub.example.com",
		PairingURL: "orca://pair?code=abc",
		DeviceID:   "phone-1",
		ExpiresAt:  now.Add(2 * time.Minute).UnixMilli(),
		QRDataURL:  "data:image/png;base64,abc",
	}}
	s := &Server{Hub: &Hub{Backend: backend, Endpoint: "wss://hub.example.com"}, OIDC: &OIDC{}}

	req := httptest.NewRequest("POST", "/api/v1/hub/pairing", nil)
	rec := httptest.NewRecorder()
	s.mintHubPairing(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("got %d", rec.Code)
	}

	req = httptest.NewRequest("POST", "/api/v1/hub/pairing", nil)
	req = req.WithContext(context.WithValue(req.Context(), actorKey{}, "https://auth.example.com#steve"))
	rec = httptest.NewRecorder()
	s.mintHubPairing(rec, req)
	if rec.Code != 200 {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(rec.Header().Get("Cache-Control"))
	}
	var offer hubadmin.Offer
	if json.Unmarshal(rec.Body.Bytes(), &offer) != nil || offer.PairingURL == "" {
		t.Fatal(rec.Body.String())
	}
}

func TestHubDisabledIsNotFound(t *testing.T) {
	s := &Server{OIDC: &OIDC{}}
	req := httptest.NewRequest("GET", "/api/v1/hub/devices", nil)
	req = req.WithContext(context.WithValue(req.Context(), actorKey{}, "https://auth.example.com#steve"))
	rec := httptest.NewRecorder()
	s.listHubDevices(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("got %d", rec.Code)
	}
}
