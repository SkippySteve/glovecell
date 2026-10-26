// Package download holds the pieces of "Download into sandbox" that the
// console backend, the project proxy and the worker helper share: URL and
// destination validation, the one-time grant shape and redaction for audit.
//
// The download never travels on the controller's own network path. The
// controller hands the project's inspecting proxy a one-time grant for one
// exact URL, then runs the helper (`agent-project-controller fetch`) inside the
// worker as the worker's user. The helper fetches through the proxy and writes
// the file itself.
package download

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"
)

const (
	// DefaultMaxBytes is the size cap when the operator configures none (2 GiB).
	DefaultMaxBytes int64 = 2 << 30
	// MaxMaxBytes bounds any configured cap (16 GiB).
	MaxMaxBytes int64 = 16 << 30
	// GrantTTL is how long a one-time grant may wait for its single request.
	GrantTTL = 10 * time.Minute
	// DefaultTimeout bounds one fetch, from request to the last byte.
	DefaultTimeout = 30 * time.Minute
	// MaxTimeout bounds any configured fetch timeout.
	MaxTimeout = 2 * time.Hour
	// MaxRedirects is how many redirects one download follows. Each hop gets
	// its own one-time grant for the exact Location the proxy saw.
	MaxRedirects = 5
	// TokenHeader carries the per-grant secret from the helper to the proxy.
	// The proxy strips it; it never reaches the destination.
	TokenHeader = "X-Apc-Download-Grant"
	// MaxURLLength bounds a download URL, query included.
	MaxURLLength = 4096
	// MaxDestinationLength bounds a destination path.
	MaxDestinationLength = 1024
	// UserAgent is the only client identification the proxy sends upstream.
	UserAgent = "agent-project-controller-download/1"
)

var dnsHost = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$`)
var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ParseURL accepts only a plain public https URL: a lowercase DNS name with at
// least one dot (no IP literal, no userinfo), port 443, no fragment and no dot
// segments. The query is kept exactly; it is part of the one-time grant.
func ParseURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > MaxURLLength {
		return nil, errors.New("enter an https URL (at most 4096 characters)")
	}
	for _, r := range raw {
		if r < 0x21 || r == 0x7f || r == '\\' {
			return nil, errors.New("URL contains spaces, control characters or backslashes; percent-encode them")
		}
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, errors.New("URL does not parse")
	}
	if u.Scheme != "https" {
		return nil, errors.New("only https URLs are downloaded; http is refused")
	}
	if u.Opaque != "" || u.User != nil {
		return nil, errors.New("URLs with credentials or opaque parts are refused")
	}
	if u.Fragment != "" || strings.Contains(raw, "#") {
		return nil, errors.New("remove the #fragment; it is never sent to the server")
	}
	if p := u.Port(); p != "" && p != "443" {
		return nil, errors.New("only the standard https port 443 is supported")
	}
	host := u.Hostname()
	if host != strings.ToLower(host) {
		return nil, errors.New("write the host name in lowercase")
	}
	if net.ParseIP(host) != nil || len(host) > 253 || !dnsHost.MatchString(host) {
		return nil, errors.New("the host must be a public DNS name, not an IP address or a single label")
	}
	if strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") || strings.HasSuffix(host, ".svc") || strings.Contains(host, ".svc.") || strings.HasSuffix(host, ".cluster.local") || host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return nil, errors.New("cluster and local host names are refused")
	}
	escaped := u.EscapedPath()
	if escaped == "" {
		escaped = "/"
	}
	if !strings.HasPrefix(escaped, "/") || strings.Contains(escaped, "//") {
		return nil, errors.New("URL path must be absolute without empty segments")
	}
	for _, seg := range strings.Split(u.Path, "/") {
		if seg == "." || seg == ".." {
			return nil, errors.New("URL path must not contain . or .. segments")
		}
	}
	lower := strings.ToLower(escaped)
	if strings.Contains(lower, "%2e%2e") || strings.Contains(lower, "%2f") || strings.Contains(lower, "%5c") || strings.Contains(lower, "%00") {
		return nil, errors.New("URL path must not encode slashes, dot segments or NUL")
	}
	u.Host = host // drop an explicit :443
	return u, nil
}

// RequestURI is what the helper sends and what the grant must match exactly.
func RequestURI(u *url.URL) string { return u.RequestURI() }

// Exact is the grant key: scheme, host, escaped path and the raw query.
func Exact(u *url.URL) string { return "https://" + u.Hostname() + RequestURI(u) }

// QueryHash is a short SHA-256 of the raw query, or "" without one. Queries
// can carry tokens (signed CDN links), so records keep only this hash.
func QueryHash(u *url.URL) string {
	if u.RawQuery == "" && !u.ForceQuery {
		return ""
	}
	sum := sha256.Sum256([]byte(u.RawQuery))
	return hex.EncodeToString(sum[:])[:16]
}

// Redact is the URL as audit records and the console show it: the query is
// replaced by its hash.
func Redact(u *url.URL) string {
	out := "https://" + u.Hostname() + u.EscapedPath()
	if h := QueryHash(u); h != "" {
		out += "?<query sha256:" + h + ">"
	}
	return out
}

// RedactString redacts a raw URL; unparseable input becomes a fixed marker.
func RedactString(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return "<invalid URL>"
	}
	out := u.Scheme + "://" + u.Host + u.EscapedPath()
	if u.RawQuery != "" || u.ForceQuery {
		sum := sha256.Sum256([]byte(u.RawQuery))
		out += "?<query sha256:" + hex.EncodeToString(sum[:])[:16] + ">"
	}
	return out
}

var safeName = regexp.MustCompile(`[^A-Za-z0-9._+-]+`)

// FileName is a safe default file name taken from the URL's last path segment.
func FileName(u *url.URL) string {
	base := path.Base(u.Path)
	base = safeName.ReplaceAllString(base, "_")
	base = strings.Trim(base, "._")
	if base == "" || base == "/" {
		return "download"
	}
	if len(base) > 128 {
		base = base[len(base)-128:]
	}
	return base
}

// DefaultDestination is ~/downloads/<file name from the URL>.
func DefaultDestination(u *url.URL) string { return "~/downloads/" + FileName(u) }

// CheckDestination is the controller's syntactic check. The helper repeats it
// and then resolves the path inside the worker, where symlinks are visible.
// A destination is ~/... or /workspace/..., names a file, and has no . or ..
// segments, empty segments or control characters.
func CheckDestination(raw string) error {
	if raw == "" || len(raw) > MaxDestinationLength {
		return errors.New("enter a destination path (at most 1024 characters)")
	}
	for _, r := range raw {
		if r < 0x20 || r == 0x7f || r == '\\' {
			return errors.New("destination contains control characters or backslashes")
		}
	}
	var rest string
	switch {
	case strings.HasPrefix(raw, "~/"):
		rest = raw[2:]
	case strings.HasPrefix(raw, "/workspace/"):
		rest = raw[len("/workspace/"):]
	default:
		return errors.New("destination must be under ~/ (the sandbox home) or /workspace/")
	}
	if rest == "" || strings.HasSuffix(rest, "/") {
		return errors.New("destination must name a file, not a directory")
	}
	for _, seg := range strings.Split(rest, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return errors.New("destination must not contain empty, . or .. segments")
		}
		if len(seg) > 255 {
			return errors.New("a destination path segment is longer than 255 bytes")
		}
	}
	return nil
}

// CheckSHA256 accepts an empty value (no expectation) or 64 hex digits, which
// it returns in lowercase.
func CheckSHA256(raw string) (string, error) {
	v := strings.ToLower(strings.TrimSpace(raw))
	v = strings.TrimPrefix(v, "sha256:")
	if v == "" || sha256Hex.MatchString(v) {
		return v, nil
	}
	return "", errors.New("expected SHA-256 must be 64 hexadecimal digits")
}

// ValidToken reports whether s looks like a grant token (32 random bytes, hex).
func ValidToken(s string) bool { return len(s) == 64 && sha256Hex.MatchString(s) }

// TokenHash is what the proxy stores; the token itself only crosses the
// controller-to-worker exec stream and the worker-to-proxy request.
func TokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// ResolveRedirect turns a Location header into the next exact URL. It returns
// an error for anything ParseURL refuses (http, IP literals, other ports...).
func ResolveRedirect(from *url.URL, location string) (*url.URL, error) {
	if location == "" || len(location) > MaxURLLength {
		return nil, errors.New("redirect without a usable Location")
	}
	ref, err := url.Parse(location)
	if err != nil {
		return nil, errors.New("redirect Location does not parse")
	}
	next := from.ResolveReference(ref)
	next.Fragment = ""
	next.RawFragment = ""
	if next.Scheme == "http" {
		return nil, errors.New("redirect downgrades https to http; refused")
	}
	return ParseURL(next.String())
}

// MaxHops bounds a checked redirect chain: the input URL plus at most
// MaxHops-1 redirects.
const MaxHops = 10

// Hop is one URL of a checked redirect chain. URL is the full exact URL and is
// shown only to the signed-in operator (masked by default); records keep
// Redacted.
type Hop struct {
	URL           string `json:"url"`
	Redacted      string `json:"redacted"`
	Host          string `json:"host"`
	Status        int    `json:"status,omitempty"`
	CrossHost     bool   `json:"crossHost,omitempty"`
	QueryHash     string `json:"queryHash,omitempty"`
	ContentLength int64  `json:"contentLength"`
	ContentType   string `json:"contentType,omitempty"`
	// Probe is the request the proxy made: HEAD, or GET when the server
	// refused HEAD (the GET is closed after its headers; no body is read).
	Probe string `json:"probe,omitempty"`
	// Refused explains why this hop's redirect was not followed.
	Refused string `json:"refused,omitempty"`
}

// NewHop describes u before it is probed; prev is the previous hop's host.
func NewHop(u *url.URL, prev string) Hop {
	return Hop{URL: Exact(u), Redacted: Redact(u), Host: u.Hostname(), QueryHash: QueryHash(u), ContentLength: -1, CrossHost: prev != "" && prev != u.Hostname()}
}

// ChainDigest identifies an approved chain: the exact URLs in order.
func ChainDigest(hops []Hop) string {
	h := sha256.New()
	for _, hop := range hops {
		h.Write([]byte(hop.URL))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// SameExceptQuery reports whether two URLs differ at most in their query.
func SameExceptQuery(a, b *url.URL) bool {
	return a.Hostname() == b.Hostname() && a.EscapedPath() == b.EscapedPath()
}

// Change describes where a download's redirects left the approved chain.
type Change struct {
	Hop       int    `json:"hop"`
	Expected  string `json:"expected"`
	Observed  string `json:"observed"`
	QueryOnly bool   `json:"queryOnly"`
	Reason    string `json:"reason"`
}
