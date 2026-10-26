package download

import (
	"strings"
	"testing"
)

func TestParseURLAcceptsOnlyPlainPublicHTTPS(t *testing.T) {
	ok := []string{
		"https://files.example.com/a.tar.gz",
		"https://files.example.com:443/a.tar.gz?X-Amz-Signature=abc&b=1",
		"https://cdn.example.org/path%20with%20space.bin",
		"https://example.com",
	}
	for _, raw := range ok {
		if _, err := ParseURL(raw); err != nil {
			t.Errorf("%s refused: %v", raw, err)
		}
	}
	bad := map[string]string{
		"http://files.example.com/a":                                     "http",
		"ftp://files.example.com/a":                                      "https",
		"https://user:pw@files.example.com/a":                            "credentials",
		"https://files.example.com/a#frag":                               "fragment",
		"https://files.example.com:8443/a":                               "443",
		"https://93.184.216.34/a":                                        "IP",
		"https://[2001:db8::1]/a":                                        "IP",
		"https://Files.Example.com/a":                                    "lowercase",
		"https://localhost/a":                                            "single label",
		"https://intranet/a":                                             "single label",
		"https://api.default.svc.cluster.local":                          "cluster",
		"https://printer.local/a":                                        "cluster",
		"https://files.example.com/a/../etc":                             ". or ..",
		"https://files.example.com/a/%2e%2e/b":                           ". or ..",
		"https://files.example.com/a%2e%2e":                              "encode",
		"https://files.example.com/a%2fb":                                "encode",
		"https://files.example.com/a b":                                  "spaces",
		"https://files.example.com/a\\b":                                 "backslash",
		"https://files.example.com//a":                                   "empty segments",
		"":                                                               "enter",
		"https://files.example.com/" + strings.Repeat("a", MaxURLLength): "4096",
	}
	for raw, want := range bad {
		_, err := ParseURL(raw)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: got %v, want error mentioning %q", raw, err, want)
		}
	}
}

func TestExactKeepsQueryAndRedactHashesIt(t *testing.T) {
	u, err := ParseURL("https://files.example.com:443/dl/a.bin?token=secret123")
	if err != nil {
		t.Fatal(err)
	}
	if Exact(u) != "https://files.example.com/dl/a.bin?token=secret123" {
		t.Fatal(Exact(u))
	}
	r := Redact(u)
	if strings.Contains(r, "secret123") || !strings.HasPrefix(r, "https://files.example.com/dl/a.bin?<query sha256:") {
		t.Fatal(r)
	}
	if strings.Contains(RedactString("https://x.example.com/p?k=secret123"), "secret123") {
		t.Fatal("RedactString leaked the query")
	}
	plain, _ := ParseURL("https://files.example.com/a.bin")
	if Redact(plain) != "https://files.example.com/a.bin" || QueryHash(plain) != "" {
		t.Fatal(Redact(plain))
	}
}

func TestDestinationSyntax(t *testing.T) {
	for _, ok := range []string{"~/downloads/a.bin", "/workspace/repo/vendor/a.tgz", "~/a"} {
		if err := CheckDestination(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "/etc/passwd", "/workspace", "/workspace/", "~/", "~/a/", "~/../etc/x", "/workspace/a/../../etc/x", "~/a//b", "~/./a", "relative/a", "/dev/null", "/proc/self/mem", "/workspaceX/a", "~/a\x00b", "~/a\\b", "~/" + strings.Repeat("a", 256)} {
		if err := CheckDestination(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestDefaultDestinationIsASafeName(t *testing.T) {
	for raw, want := range map[string]string{
		"https://files.example.com/releases/tool-1.2.tar.gz?sig=x": "~/downloads/tool-1.2.tar.gz",
		"https://files.example.com/":                               "~/downloads/download",
		"https://files.example.com/a%20b%3Bc.zip":                  "~/downloads/a_b_c.zip",
		"https://files.example.com/..hidden":                       "~/downloads/hidden",
	} {
		u, err := ParseURL(raw)
		if err != nil {
			t.Fatal(raw, err)
		}
		if got := DefaultDestination(u); got != want {
			t.Errorf("%s: %s, want %s", raw, got, want)
		}
		if err := CheckDestination(DefaultDestination(u)); err != nil {
			t.Errorf("default destination for %s is invalid: %v", raw, err)
		}
	}
}

func TestSHA256Input(t *testing.T) {
	h := strings.Repeat("A", 64)
	if v, err := CheckSHA256("sha256:" + h); err != nil || v != strings.ToLower(h) {
		t.Fatal(v, err)
	}
	if v, err := CheckSHA256(""); err != nil || v != "" {
		t.Fatal(v, err)
	}
	for _, bad := range []string{"abc", strings.Repeat("g", 64), strings.Repeat("a", 63)} {
		if _, err := CheckSHA256(bad); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}

func TestResolveRedirect(t *testing.T) {
	from, _ := ParseURL("https://github.com/o/r/releases/download/v1/a.tgz")
	next, err := ResolveRedirect(from, "https://objects.example.com/x/y?sig=1")
	if err != nil || Exact(next) != "https://objects.example.com/x/y?sig=1" {
		t.Fatal(next, err)
	}
	next, err = ResolveRedirect(from, "/o/r/other.tgz")
	if err != nil || Exact(next) != "https://github.com/o/r/other.tgz" {
		t.Fatal(next, err)
	}
	for loc, want := range map[string]string{
		"http://objects.example.com/x":   "downgrade",
		"https://10.0.0.1/x":             "IP",
		"https://objects.example.com:81": "443",
		"":                               "Location",
		"https://intranet/x":             "single label",
	} {
		if _, err := ResolveRedirect(from, loc); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %q", loc, err, want)
		}
	}
}

func TestChainDigestAndQueryOnlyChanges(t *testing.T) {
	a, _ := ParseURL("https://objects.example.com/x/y?sig=1")
	b, _ := ParseURL("https://objects.example.com/x/y?sig=2")
	c, _ := ParseURL("https://other.example.com/x/y?sig=1")
	if !SameExceptQuery(a, b) || SameExceptQuery(a, c) {
		t.Fatal("query-only comparison")
	}
	one := []Hop{NewHop(a, "")}
	two := []Hop{NewHop(b, "")}
	if ChainDigest(one) == ChainDigest(two) {
		t.Fatal("digest ignores the query")
	}
	if !NewHop(c, "objects.example.com").CrossHost || NewHop(b, "objects.example.com").CrossHost {
		t.Fatal("cross-host flag")
	}
}
