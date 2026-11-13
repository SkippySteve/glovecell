package proxy

import (
	"errors"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
)

// RegistryRules returns the reviewed host allowlist for a package ecosystem.
// Paths are the whole host. This is not per-package version enforcement.
func RegistryRules(ids []string) ([]api.HTTPSRule, error) {
	catalog := map[string][]string{
		"npm":    {"registry.npmjs.org"},
		"go":     {"proxy.golang.org", "sum.golang.org"},
		"python": {"pypi.org", "files.pythonhosted.org"},
		"rust":   {"index.crates.io", "static.crates.io"},
	}
	if len(ids) > len(catalog) {
		return nil, errors.New("unknown registry")
	}
	seen := map[string]bool{}
	rules := []api.HTTPSRule{}
	for _, id := range ids {
		hosts, ok := catalog[id]
		if !ok || seen[id] {
			return nil, errors.New("unknown or repeated registry")
		}
		seen[id] = true
		for _, host := range hosts {
			rules = append(rules, api.HTTPSRule{Host: host, Methods: []string{"GET", "HEAD"}, Paths: []string{"/"}})
		}
	}
	return rules, nil
}

// StandardDownloadHosts are the GitHub hosts of the "standard" access level:
// web pages, raw files, archives and release assets, read only.
var StandardDownloadHosts = []string{"github.com", "codeload.github.com", "raw.githubusercontent.com", "objects.githubusercontent.com", "release-assets.githubusercontent.com"}

// StandardRules is the "standard" access level for a project's optional
// HTTPS rules: every reviewed package registry plus read-only GitHub
// downloads. GET and HEAD only, so nothing can be pushed, posted or uploaded
// through these rules; Git clone over HTTPS needs POST and is not included.
// Callers drop hosts that are reserved for the project's own repository.
func StandardRules() []api.HTTPSRule {
	rules, _ := RegistryRules([]string{"go", "npm", "python", "rust"})
	for _, host := range StandardDownloadHosts {
		rules = append(rules, api.HTTPSRule{Host: host, Methods: []string{"GET", "HEAD"}, Paths: []string{"/"}})
	}
	return rules
}
