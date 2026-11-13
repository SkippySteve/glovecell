package proxy

import (
	"errors"
	"net/http/httptest"
	"testing"

	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/credentials"
)

func TestMuseSubscriptionScopeRenewalAndRevocation(t *testing.T) {
	current := "subscription-only-fixture"
	var asked []string
	s := &Server{Config: Config{MuseSubscriptionGateway: true}, Credentials: loaderProvider(func() (string, error) { return current, nil }, &asked)}
	for _, route := range []struct{ method, path string }{{"GET", "/muse-code/models"}, {"GET", "/muse-code/config/v1"}, {"GET", "/v1/models"}, {"POST", "/v1/responses"}, {"POST", "/v1/chat/completions"}} {
		r := httptest.NewRequest(route.method, "https://api.meta.ai"+route.path, nil)
		r.Header.Set("Authorization", "Bearer non-secret-placeholder")
		if err := s.museCredentials(r); err != nil || r.Header.Get("Authorization") != "Bearer "+current {
			t.Fatal("subscription injection failed", err)
		}
	}
	for _, uri := range asked {
		if uri != MuseSubscriptionKeyURI {
			t.Fatal("Muse key fetched by the wrong URI", uri)
		}
	}
	for _, target := range []string{"https://api.meta.ai/v1/keys", "https://api.meta.ai/v1/models?redirect=https://other.example", "https://api.meta.ai/v1/models/../keys"} {
		r := httptest.NewRequest("GET", target, nil)
		if err := s.museCredentials(r); err == nil || credentials.Status(err) != 403 {
			t.Fatal("accepted unreviewed subscription route", target)
		}
	}
	r := httptest.NewRequest("POST", "https://other.example/v1/responses", nil)
	if err := s.museCredentials(r); err != nil || r.Header.Get("Authorization") != "" {
		t.Fatal("credential crossed origin")
	}
	current = "renewed-subscription-fixture"
	r = httptest.NewRequest("GET", "https://api.meta.ai/v1/models", nil)
	if err := s.museCredentials(r); err != nil || r.Header.Get("Authorization") != "Bearer "+current {
		t.Fatal("renewal was not consumed")
	}
	for _, loader := range []func() (string, error){nil, func() (string, error) { return "", nil }, func() (string, error) { return "", errors.New("broker unavailable") }, func() (string, error) { return "unsafe\nkey", nil }} {
		s.Credentials = loaderProvider(loader, nil)
		r = httptest.NewRequest("GET", "https://api.meta.ai/v1/models", nil)
		if err := s.museCredentials(r); err == nil || r.Header.Get("Authorization") != "" {
			t.Fatal("unavailable/revoked grant did not fail closed")
		}
	}
	// A restarted gateway can recover from replacement material without worker restart.
	s.Credentials = loaderProvider(func() (string, error) { return current, nil }, nil)
	if err := s.museCredentials(httptest.NewRequest("GET", "https://api.meta.ai/v1/models", nil)); err != nil {
		t.Fatal(err)
	}
}
