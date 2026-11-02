package proxy

import (
	"strings"
	"testing"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
)

const referenceFixture = `{"version":1,"digest":"0123456789ab","note":"fixture","projects":[
{"name":"cilium","source":"git:reference.yaml#cilium","repos":[{"github":"cilium/cilium"},{"github":"cilium/hubble-ui"}],"docs":[{"domain":"cilium.io","wildcard":true}]},
{"name":"forgejo","source":"git:reference.yaml#forgejo","repos":[{"host":"codeberg.org","path":"forgejo/forgejo"}],"docs":[]},
{"name":"restic","source":"git:reference.yaml#restic","repos":[],"docs":[{"domain":"restic.readthedocs.io","wildcard":false}]}]}`

func referenceConfig(t *testing.T) Config {
	t.Helper()
	rules, digest, err := ReferenceRules([]byte(referenceFixture))
	if err != nil || digest != "0123456789ab" {
		t.Fatal(err, digest)
	}
	own := api.HTTPSRule{Host: "git.example.com", Methods: []string{"GET", "POST"}, Paths: []string{"/steve/project.git"}}
	c := Config{Rules: append([]api.HTTPSRule{own}, rules...)}
	if err = Validate(c); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestReferenceRulesAreReadOnlyAndLabelled(t *testing.T) {
	c := referenceConfig(t)
	for _, r := range c.Rules[1:] {
		if !strings.HasPrefix(r.Source, "git:reference.yaml#") {
			t.Fatal("reference rule without its label", r)
		}
	}
	for request, want := range map[string]bool{
		"GET docs.cilium.io /en/stable/":                                 true,
		"HEAD cilium.io /":                                               true,
		"GET a.b.cilium.io /":                                            false, // one label, as Cilium's *.cilium.io
		"GET cilium.io.evil.example /":                                   false,
		"POST docs.cilium.io /search":                                    false,
		"GET api.github.com /repos/cilium/cilium/contents/README.md":     true,
		"GET api.github.com /repos/cilium/ciliumx":                       false,
		"DELETE api.github.com /repos/cilium/cilium/issues/1":            false,
		"GET github.com /cilium/hubble-ui.git/info/refs":                 true,
		"POST github.com /cilium/cilium.git/git-upload-pack":             true,
		"POST github.com /cilium/cilium.git/git-receive-pack":            false,
		"GET github.com /cilium/cilium/releases/download/v1/x.tar.gz":    true,
		"GET github.com /cilium/cilium/settings":                         false,
		"GET raw.githubusercontent.com /cilium/cilium/main/README.md":    true,
		"GET release-assets.githubusercontent.com /x":                    true,
		"POST release-assets.githubusercontent.com /x":                   false,
		"GET codeberg.org /forgejo/forgejo.git/info/refs":                true,
		"POST codeberg.org /forgejo/forgejo.git/git-upload-pack":         true,
		"POST codeberg.org /forgejo/forgejo.git/git-receive-pack":        false,
		"GET codeberg.org /forgejo/other":                                false,
		"GET restic.readthedocs.io /en/latest/":                          true,
		"GET other.readthedocs.io /":                                     false,
		"GET api.github.com /repos/cilium/cilium/../../other/x/contents": false,
	} {
		parts := strings.SplitN(request, " ", 3)
		if got := Allowed(c, parts[1], parts[0], parts[2]); got != want {
			t.Errorf("%s: allowed=%v, want %v", request, got, want)
		}
	}
}

func TestReferenceRulesRefuseWritesAndSharedWildcards(t *testing.T) {
	for name, doc := range map[string]string{
		"unknown field": `{"version":1,"digest":"0123456789ab","projects":[],"methods":["PUT"]}`,
		"no digest":     `{"version":1,"projects":[]}`,
		"push path":     `{"version":1,"digest":"0123456789ab","projects":[{"name":"a","source":"git:x#a","repos":[{"host":"codeberg.org","path":"a/b.git"}],"docs":[]}]}`,
		"tenant zone":   `{"version":1,"digest":"0123456789ab","projects":[{"name":"a","source":"git:x#a","repos":[],"docs":[{"domain":"a.github.io","wildcard":true}]}]}`,
		"suffix":        `{"version":1,"digest":"0123456789ab","projects":[{"name":"a","source":"git:x#a","repos":[],"docs":[{"domain":"co.uk","wildcard":true}]}]}`,
		"no label":      `{"version":1,"digest":"0123456789ab","projects":[{"name":"a","source":"","repos":[],"docs":[{"domain":"a.example.com","wildcard":false}]}]}`,
		"duplicate":     `{"version":1,"digest":"0123456789ab","projects":[{"name":"a","source":"git:x#a","repos":[],"docs":[]},{"name":"a","source":"git:x#a","repos":[],"docs":[]}]}`,
	} {
		if _, _, err := ReferenceRules([]byte(doc)); err == nil {
			t.Error(name, "accepted")
		}
	}
	for _, rule := range []api.HTTPSRule{
		{Host: "docs.example.com", Methods: []string{"PUT"}, Paths: []string{"/"}, Source: "git:x#a"},
		{Host: "docs.example.com", Methods: []string{"POST"}, Paths: []string{"/upload"}, Source: "git:x#a"},
		{Host: "*.github.io", Methods: []string{"GET"}, Paths: []string{"/"}, Source: "git:x#a"},
		{Host: "codeberg.org", Methods: []string{"GET"}, Paths: []string{"/a/b.git/git-receive-pack"}, Source: "git:x#a"},
		// A project's own rule never gets a wildcard.
		{Host: "*.example.com", Methods: []string{"GET"}, Paths: []string{"/"}},
	} {
		if Validate(Config{Rules: []api.HTTPSRule{rule}}) == nil {
			t.Error("accepted", rule)
		}
	}
}

func TestReferenceRulesDoNotCountTowardProjectRules(t *testing.T) {
	c := referenceConfig(t)
	for i := 0; i < 63; i++ {
		c.Rules = append(c.Rules, api.HTTPSRule{Host: "h" + strings.Repeat("x", i%5) + ".example.com", Methods: []string{"GET"}, Paths: []string{"/" + string(rune('a'+i%26))}})
	}
	if err := Validate(c); err != nil {
		t.Fatal("64 project rules plus the reference list refused", err)
	}
	c.Rules = append(c.Rules, api.HTTPSRule{Host: "one-more.example.com", Methods: []string{"GET"}, Paths: []string{"/"}})
	if Validate(c) == nil {
		t.Fatal("65 project rules accepted")
	}
}

func TestWildcardHostMatchesOneLabel(t *testing.T) {
	for host, want := range map[string]bool{"docs.example.com": true, "example.com": false, "a.b.example.com": false, "xexample.com": false, ".example.com": false} {
		if hostMatches("*.example.com", host) != want {
			t.Error(host)
		}
	}
	if !WildcardAllowed("example.com") || WildcardAllowed("com") || WildcardAllowed("github.io") || WildcardAllowed("a.github.io") || WildcardAllowed("readthedocs.io") {
		t.Fatal("wildcard guard")
	}
}
