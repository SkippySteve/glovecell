package credentials

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestParseURIFollowsSubstrateSyntax(t *testing.T) {
	u, err := ParseURI("ate-secret://k8s.io/default/ns1/example-api/token")
	if err != nil || u.Class != "k8s.io" || len(u.Tail) != 4 || u.Tail[3] != "token" {
		t.Fatal("valid URI refused", u, err)
	}
	for _, raw := range []string{
		"",
		"https://k8s.io/a/b",               // another scheme
		"ate-secret://k8s.io/only-one",     // one segment
		"ate-secret://k8s.io/a/b/",         // trailing slash
		"ate-secret://k8s.io/a//b",         // empty segment
		"ate-secret://k8s.io:443/a/b",      // port
		"ate-secret://user@k8s.io/a/b",     // userinfo
		"ate-secret://k8s.io/a/b?x=1",      // query
		"ate-secret://k8s.io/a/b#x",        // fragment
		"ate-secret://k8s.io/a/%2e%2e",     // percent-encoding
		"ate-secret://k8s.io/a/../b",       // climbing
		"ate-secret://k8s.io/./b",          // dot segment
		"ate-secret://K8S.io/a/b",          // host not lowercase
		"ate-secret://-k8s.io/a/b",         // host not DNS-1123
		"ate-secret://k8s.io/a/b c",        // space
		"ate-secret://k8s.io/a/b\nInject:", // control character
	} {
		if _, err := ParseURI(raw); err == nil {
			t.Fatal("invalid URI accepted:", raw)
		}
	}
}

func TestStatusFailsClosedLikeSubstrate(t *testing.T) {
	for err, want := range map[error]int{
		ErrNotFound:              403,
		ErrPermissionDenied:      403,
		errors.New("other"):      403,
		ErrUnavailable:           503,
		context.DeadlineExceeded: 503,
	} {
		if got := Status(err); got != want {
			t.Fatal(err, got, want)
		}
	}
}

func TestFilesServesOnlyListedKeysAndRereads(t *testing.T) {
	dir := t.TempDir()
	write := func(name, value string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("token", "first-token\n")
	write("ca.key", "not-a-credential")
	f := Files{Class: "cell.example", Volumes: map[string]string{"tls": dir}, Keys: []string{"tls/token", "tls/absent"}}
	fetch := func(uri string) ([]byte, error) {
		return f.FetchSecret(context.Background(), Request{URI: uri})
	}
	if got, err := fetch("ate-secret://cell.example/tls/token"); err != nil || string(got) != "first-token" {
		t.Fatal("listed key not served", string(got), err)
	}
	// A replaced Secret applies to the next request.
	write("token", "second-token")
	if got, err := fetch("ate-secret://cell.example/tls/token"); err != nil || string(got) != "second-token" {
		t.Fatal("replacement not read", string(got), err)
	}
	for uri, want := range map[string]error{
		"ate-secret://cell.example/tls/ca.key":       ErrPermissionDenied, // in the volume, not listed
		"ate-secret://other.example/tls/token":       ErrPermissionDenied, // another provider class
		"ate-secret://cell.example/other/token":      ErrPermissionDenied, // unknown volume
		"ate-secret://cell.example/tls/token/extra":  ErrPermissionDenied, // deeper tail
		"ate-secret://cell.example/tls/../tls/token": ErrPermissionDenied, // invalid URI
		"ate-secret://cell.example/tls/absent":       ErrNotFound,         // listed, missing
	} {
		if got, err := fetch(uri); !errors.Is(err, want) || got != nil {
			t.Fatal(uri, err, want)
		}
	}
	// Revocation: the file is removed, so the credential is gone at once.
	if err := os.Remove(filepath.Join(dir, "token")); err != nil {
		t.Fatal(err)
	}
	if _, err := fetch("ate-secret://cell.example/tls/token"); !errors.Is(err, ErrNotFound) || Status(err) != http.StatusForbidden {
		t.Fatal("revoked credential still served", err)
	}
	// Unusable material never reaches a header.
	for _, value := range []string{"", "two\nlines", "tab\tinside", string(make([]byte, 32769))} {
		write("token", value)
		if got, err := fetch("ate-secret://cell.example/tls/token"); !errors.Is(err, ErrUnavailable) || got != nil {
			t.Fatalf("unusable credential accepted: %q %v", value, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.FetchSecret(ctx, Request{URI: "ate-secret://cell.example/tls/token"}); err == nil {
		t.Fatal("cancelled fetch succeeded")
	}
}
