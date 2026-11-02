package proxy

// Reference rules: a deployment-level list of permanent, read-only allow
// rules that every project's proxy gets in addition to its own (README
// "Reference rules"). An operator uses it for the source repositories and
// official documentation of the open-source projects the deployment runs.
// The rules are ordinary HTTPS rules in the project's policy, each labelled
// with Source so the console can show where it came from; they are never
// written to the ProjectWorkspace and cannot be edited per project.
//
// The document is generic: projects with a source label, repositories
// (github OWNER/REPO, or another Git host and path) and documentation domains
// (exact, or *.domain plus the domain itself).

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	"golang.org/x/net/publicsuffix"
)

// MaxReferenceRules bounds the reference rules of one policy. Project rules
// keep their own bound of 64.
const MaxReferenceRules = 2048

// ReferenceDocument is the operator's reference list.
type ReferenceDocument struct {
	Version  int                `json:"version"`
	Digest   string             `json:"digest"`
	Note     string             `json:"note,omitempty"`
	Projects []ReferenceProject `json:"projects"`
}

// ReferenceProject is one upstream project: its label, repositories and docs.
type ReferenceProject struct {
	Name   string `json:"name"`
	Source string `json:"source"`
	Repos  []struct {
		GitHub string `json:"github,omitempty"`
		Host   string `json:"host,omitempty"`
		Path   string `json:"path,omitempty"`
	} `json:"repos"`
	Docs []struct {
		Domain   string `json:"domain"`
		Wildcard bool   `json:"wildcard"`
	} `json:"docs"`
}

var (
	sourceLabel  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9:._/#+-]{0,127}$`)
	digestRE     = regexp.MustCompile(`^[a-f0-9]{8,64}$`)
	gitPath      = regexp.MustCompile(`^[A-Za-z0-9_.-]+(/[A-Za-z0-9_.-]+)+$`)
	readMethods  = []string{"GET", "HEAD"}
	releaseHosts = []string{"release-assets.githubusercontent.com", "objects.githubusercontent.com", "github-releases.githubusercontent.com"}
)

// ReferenceRules parses the document and expands it into labelled rules:
//   - docs: GET and HEAD on every path of the domain, and with wildcard of
//     every name one label below it;
//   - GitHub repositories: what clone, fetch, API reads, raw files, archives,
//     Git LFS and release downloads use (GET and HEAD, and POST only to
//     git-upload-pack, which fetches);
//   - other Git hosts: GET and HEAD under the repository's path, and POST only
//     to its git-upload-pack.
//
// Nothing in the document can ask for another method; git-receive-pack (push)
// is never allowed. Any invalid entry refuses the whole document.
func ReferenceRules(raw []byte) ([]api.HTTPSRule, string, error) {
	var doc ReferenceDocument
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		return nil, "", errors.New("reference rules are not valid JSON: " + err.Error())
	}
	if doc.Version != 1 || !digestRE.MatchString(doc.Digest) {
		return nil, "", errors.New("reference rules need version 1 and a digest")
	}
	var rules []api.HTTPSRule
	names := map[string]bool{}
	for _, p := range doc.Projects {
		if p.Name == "" || names[p.Name] || !sourceLabel.MatchString(p.Source) {
			return nil, "", errors.New("reference project " + p.Name + " needs a unique name and a source label")
		}
		names[p.Name] = true
		add := func(host string, methods []string, paths ...string) {
			rules = append(rules, api.HTTPSRule{Host: host, Methods: methods, Paths: paths, Source: p.Source})
		}
		for _, d := range p.Docs {
			if d.Wildcard {
				add("*."+d.Domain, readMethods, "/")
			}
			add(d.Domain, readMethods, "/")
		}
		var github []string
		for _, r := range p.Repos {
			switch {
			case r.GitHub != "" && r.Host == "" && r.Path == "":
				if _, err := GitHubRules(r.GitHub); err != nil {
					return nil, "", errors.New("reference project " + p.Name + ": " + err.Error())
				}
				github = append(github, r.GitHub)
			case r.GitHub == "" && dnsHost.MatchString(r.Host) && gitPath.MatchString(r.Path) && !strings.HasSuffix(r.Path, ".git") && !strings.Contains(r.Path, ".."):
				add(r.Host, readMethods, "/"+r.Path, "/"+r.Path+".git")
				add(r.Host, []string{"POST"}, "/"+r.Path+"/git-upload-pack", "/"+r.Path+".git/git-upload-pack")
			default:
				return nil, "", errors.New("reference project " + p.Name + ": a repository is github OWNER/REPO or a host and path")
			}
		}
		if len(github) > 0 {
			var web, fetch, apiPaths, raws, media []string
			for _, repo := range github {
				for _, n := range []string{"/" + repo, "/" + repo + ".git"} {
					web = append(web, n+"/info/refs")
					fetch = append(fetch, n+"/git-upload-pack")
				}
				web = append(web, "/"+repo+"/releases", "/"+repo+"/archive", "/"+repo+"/raw")
				apiPaths = append(apiPaths, "/repos/"+repo)
				raws = append(raws, "/"+repo)
				media = append(media, "/media/"+repo)
			}
			add("github.com", readMethods, web...)
			add("github.com", []string{"POST"}, fetch...)
			add("api.github.com", readMethods, apiPaths...)
			add("raw.githubusercontent.com", readMethods, raws...)
			add("codeload.github.com", readMethods, raws...)
			add("media.githubusercontent.com", readMethods, media...)
			for _, host := range releaseHosts {
				add(host, []string{"GET"}, "/")
			}
		}
	}
	if len(rules) > MaxReferenceRules {
		return nil, "", errors.New("reference rules exceed 2048 rules")
	}
	if err := validateReference(rules); err != nil {
		return nil, "", err
	}
	return rules, doc.Digest, nil
}

// WildcardAllowed reports whether "*.base" may be a reference rule: base must
// be a name its owner controls, never a public suffix or a tenant zone on
// shared hosting (the Public Suffix List, private section included), where
// anyone can create names.
func WildcardAllowed(base string) bool {
	if len(base) > 249 || !dnsHost.MatchString(base) || !strings.Contains(base, ".") {
		return false
	}
	suffix, icann := publicsuffix.PublicSuffix(base)
	return icann && suffix != base
}

// hostMatches compares a request host with an exact or "*.base" rule host.
// A wildcard matches exactly one label below base, as Cilium's toFQDNs
// matchPattern "*.base" does at the network layer.
func hostMatches(rule, host string) bool {
	base, wildcard := strings.CutPrefix(rule, "*.")
	if !wildcard {
		return rule == host
	}
	label, rest, ok := strings.Cut(host, ".")
	return ok && rest == base && label != "" && label != "*" && dnsHost.MatchString(label)
}

// validateReference checks labelled rules: read only (GET and HEAD, POST
// only to git-upload-pack), literal prefixes, exact or allowed wildcard hosts.
func validateReference(rules []api.HTTPSRule) error {
	for _, r := range rules {
		if !sourceLabel.MatchString(r.Source) {
			return errors.New("reference rule needs a source label")
		}
		host := strings.TrimPrefix(r.Host, "*.")
		if len(r.Host) > 253 || !dnsHost.MatchString(host) || !strings.Contains(host, ".") || host != r.Host && !WildcardAllowed(host) {
			return errors.New("reference rule host " + r.Host + " is not an exact host or an allowed *.domain")
		}
		if len(r.Methods) == 0 || len(r.Paths) == 0 {
			return errors.New("reference rule requires methods and paths")
		}
		for _, m := range r.Methods {
			switch m {
			case "GET", "HEAD":
			case "POST":
				for _, p := range r.Paths {
					if !strings.HasSuffix(p, "/git-upload-pack") {
						return errors.New("reference rules allow POST only to git-upload-pack")
					}
				}
			default:
				return errors.New("reference rules are read only")
			}
		}
		for _, p := range r.Paths {
			if !strings.HasPrefix(p, "/") || strings.ContainsAny(p, "%?*\\#") || strings.Contains(p, "..") || strings.Contains(p, "//") || strings.HasSuffix(p, "git-receive-pack") {
				return errors.New("invalid reference rule path")
			}
		}
	}
	return nil
}
