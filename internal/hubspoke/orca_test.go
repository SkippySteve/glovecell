package hubspoke

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestOrcaParseOffer(t *testing.T) {
	raw := []byte(`{"type":"orca_server_ready","pairing":{"available":true,"url":"orca://pair?token=secret","endpoint":"ws://spoke-a:6768","deviceId":"dev-1","scope":"runtime"}}`)
	offer, err := (OrcaRuntime{}).ParseOffer(raw)
	if err != nil {
		t.Fatal(err)
	}
	if offer.URL != "orca://pair?token=secret" || offer.Scope != "runtime" || offer.Endpoint != "ws://spoke-a:6768" {
		t.Fatalf("offer %#v", offer)
	}
}

func TestOrcaParseOfferRejectsMobile(t *testing.T) {
	raw := []byte(`{"type":"orca_server_ready","pairing":{"available":true,"url":"orca://pair?x=1","scope":"mobile"}}`)
	if _, err := (OrcaRuntime{}).ParseOffer(raw); err == nil {
		t.Fatal("accepted mobile offer")
	}
}

func TestFindReadyOfferInLogs(t *testing.T) {
	logs := []byte("noise\n{\"type\":\"orca_server_ready\",\"pairing\":{\"available\":true,\"url\":\"orca://pair?x=1\",\"scope\":\"runtime\"}}\nmore\n")
	offer, err := FindReadyOffer(OrcaRuntime{}, logs)
	if err != nil || !strings.HasPrefix(offer.URL, "orca://pair") {
		t.Fatal(err, offer)
	}
}

func TestOrcaEnrollerArgvAndNoOfferLeak(t *testing.T) {
	var saw [][]string
	e := &OrcaEnroller{
		Binary: "/bin/orca-ide",
		Run: func(_ context.Context, argv []string) (string, error) {
			saw = append(saw, append([]string{}, argv...))
			switch argv[1] {
			case "environment":
				if argv[2] == "add" {
					return `{"ok":true,"result":{"id":"env-1","name":"demo"}}`, nil
				}
				if argv[2] == "list" {
					return `{"ok":true,"result":{"environments":[{"id":"env-1","name":"demo"}]}}`, nil
				}
				return `{"ok":true,"result":{}}`, nil
			case "repo":
				return `{"ok":true,"result":{}}`, nil
			}
			return "", errors.New("unexpected")
		},
	}
	offer := Offer{URL: "orca://pair?token=do-not-leak", Scope: "runtime"}
	enrolled, err := e.Enroll(context.Background(), "demo", offer)
	if err != nil || enrolled.ID != "env-1" {
		t.Fatal(err, enrolled)
	}
	if err := e.Connect(context.Background(), enrolled.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.RegisterProject(context.Background(), enrolled.ID, "/workspace/repo"); err != nil {
		t.Fatal(err)
	}
	// Force a failure path and ensure the offer is not in the error.
	e.Run = func(context.Context, []string) (string, error) {
		return "", errors.New("boom orca://pair?token=do-not-leak")
	}
	_, err = e.Enroll(context.Background(), "demo", offer)
	if err == nil || strings.Contains(err.Error(), "do-not-leak") || strings.Contains(err.Error(), "orca://pair") {
		t.Fatalf("offer leaked in error: %v", err)
	}
	if len(saw) < 1 || saw[0][2] != "add" {
		t.Fatalf("argv %#v", saw)
	}
}

func TestT3Stub(t *testing.T) {
	if _, err := (T3Runtime{}).ParseOffer(nil); !errors.Is(err, ErrNotImplemented) {
		t.Fatal(err)
	}
	if _, err := (T3Enroller{}).Enroll(context.Background(), "x", Offer{}); !errors.Is(err, ErrNotImplemented) {
		t.Fatal(err)
	}
}
