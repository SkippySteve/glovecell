// Package credentials resolves the secrets the project proxy adds to the
// requests it forwards, so workers only ever hold placeholders.
//
// The interface follows Agent Substrate's credential-provider plugin API
// (substrate.credprovider.v1.CredentialProvider.FetchSecret, Apache-2.0):
// a credential is named by a URI, ate-secret://<provider class>/<tail>, the
// provider class picks the backend, the backend interprets the tail and
// decides whether the calling cell may have it, and every failure denies the
// request. Keeping the same URI syntax and error classes means a Glovecell
// policy can name the same credentials as a Substrate EgressPolicy, and a
// backend written for one (an OpenBao provider, say) fits the other.
package credentials

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// Scheme is the credential URI scheme, the same as Agent Substrate's.
const Scheme = "ate-secret"

// The error classes a provider returns. They map to HTTP statuses as
// Substrate's egress gateway maps the matching gRPC codes (Status).
var (
	ErrNotFound         = errors.New("credential not found")
	ErrPermissionDenied = errors.New("credential not granted to this cell")
	ErrUnavailable      = errors.New("credential backend unavailable")
)

// Request asks a provider for one credential.
type Request struct {
	// URI names the credential (ParseURI).
	URI string
	// Cell identifies the cell the request is made for. Only the proxy sets
	// it, never the worker's request. Empty for a provider that serves one
	// cell's own mount (Files); a shared backend must refuse an empty Cell.
	Cell string
}

// Provider resolves a credential URI to its secret.
type Provider interface {
	FetchSecret(ctx context.Context, req Request) ([]byte, error)
}

// URI is a parsed credential URI.
type URI struct {
	// Class is the provider class: the URI host, a DNS-1123 subdomain.
	Class string
	// Tail is the provider-interpreted path, at least two non-empty segments.
	Tail []string
}

var (
	subdomain = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*$`)
	segment   = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
)

// ParseURI checks a credential URI with Substrate's rules: scheme ate-secret,
// a DNS-1123 subdomain host, and a path of at least two non-empty segments;
// no port, userinfo, query, fragment, percent-encoding or trailing slash.
// Segments are also limited to letters, digits, '.', '_' and '-', and never
// '.' or '..', so a tail cannot climb out of where a provider looks.
func ParseURI(raw string) (URI, error) {
	invalid := errors.New("invalid credential URI")
	rest, ok := strings.CutPrefix(raw, Scheme+"://")
	if !ok || len(raw) > 1024 || strings.ContainsAny(rest, "%?#@:") {
		return URI{}, invalid
	}
	host, path, ok := strings.Cut(rest, "/")
	if !ok || len(host) > 253 || !subdomain.MatchString(host) {
		return URI{}, invalid
	}
	tail := strings.Split(path, "/")
	if len(tail) < 2 {
		return URI{}, invalid
	}
	for _, s := range tail {
		if !segment.MatchString(s) || s == "." || s == ".." {
			return URI{}, invalid
		}
	}
	return URI{Class: host, Tail: tail}, nil
}

// Status is the HTTP status the proxy answers when a credential cannot be
// fetched: 503 (retryable) when the backend is unavailable or slow, 403 for
// everything else. Either way the request never leaves without its
// credential.
func Status(err error) int {
	if errors.Is(err, ErrUnavailable) || errors.Is(err, context.DeadlineExceeded) {
		return http.StatusServiceUnavailable
	}
	return http.StatusForbidden
}

// Usable checks a fetched secret before it goes into a header: not empty,
// bounded, and printable ASCII only, so it cannot split or add headers.
// Substrate's gateway refuses the same values with 503.
func Usable(secret []byte) error {
	if len(secret) == 0 || len(secret) > 32768 {
		return ErrUnavailable
	}
	for _, c := range secret {
		if c < 33 || c > 126 {
			return ErrUnavailable
		}
	}
	return nil
}

// Files serves credentials from files projected into the proxy, such as a
// Secret volume, as ate-secret://<Class>/<volume>/<key>. Only the listed
// volume/key pairs resolve, so a URI cannot reach other files in a volume.
// The proxy is per cell and so is its volume: Files does not need Cell.
type Files struct {
	Class string
	// Volumes maps a volume name to its mount directory.
	Volumes map[string]string
	// Keys lists the "<volume>/<key>" pairs that may be read.
	Keys []string
}

func (f Files) FetchSecret(ctx context.Context, req Request) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	u, err := ParseURI(req.URI)
	if err != nil || u.Class != f.Class {
		return nil, ErrPermissionDenied
	}
	name := strings.Join(u.Tail, "/")
	dir, ok := f.Volumes[u.Tail[0]]
	if len(u.Tail) != 2 || !ok || !slices.Contains(f.Keys, name) {
		return nil, ErrPermissionDenied
	}
	// Read on every request so a replaced or revoked Secret applies at once.
	raw, err := os.ReadFile(filepath.Join(dir, u.Tail[1]))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, ErrUnavailable
	}
	secret := []byte(strings.TrimSpace(string(raw)))
	if err := Usable(secret); err != nil {
		return nil, err
	}
	return secret, nil
}

// ProviderFunc adapts a function to Provider.
type ProviderFunc func(ctx context.Context, req Request) ([]byte, error)

func (f ProviderFunc) FetchSecret(ctx context.Context, req Request) ([]byte, error) {
	return f(ctx, req)
}
