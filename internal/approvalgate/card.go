package approvalgate

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// CI is the checks summary for a head SHA.
type CI struct {
	Passed, Failed, Pending int
	FailedNames             []string
	PendingNames            []string
}

// Green reports every check passed and at least one ran.
func (c CI) Green() bool { return c.Failed == 0 && c.Pending == 0 && c.Passed > 0 }

func (c CI) String() string {
	if c.Passed+c.Failed+c.Pending == 0 {
		return "no checks reported"
	}
	s := fmt.Sprintf("%d passed", c.Passed)
	if c.Failed > 0 {
		s += fmt.Sprintf(", %d failed (%s)", c.Failed, strings.Join(c.FailedNames, ", "))
	}
	if c.Pending > 0 {
		s += fmt.Sprintf(", %d pending (%s)", c.Pending, strings.Join(c.PendingNames, ", "))
	}
	return s
}

// Spoken is the CI state in a few words.
func (c CI) Spoken() string {
	switch {
	case c.Passed+c.Failed+c.Pending == 0:
		return "No checks have reported"
	case c.Green():
		return "Checks are green"
	case c.Failed > 0:
		return fmt.Sprintf("%d check%s failing", c.Failed, plural(c.Failed))
	}
	return fmt.Sprintf("%d check%s still running", c.Pending, plural(c.Pending))
}

func plural(n int) string {
	if n == 1 {
		return " is"
	}
	return "s are"
}

// Summarize evaluates checks, ignoring the gate's own status context.
func Summarize(checks []Check, own string) CI {
	var ci CI
	for _, c := range checks {
		if c.Context == own {
			continue
		}
		switch c.State {
		case "success":
			ci.Passed++
		case "pending", "":
			ci.Pending++
			ci.PendingNames = append(ci.PendingNames, c.Context)
		default: // failure, error, warning
			ci.Failed++
			ci.FailedNames = append(ci.FailedNames, c.Context)
		}
	}
	sort.Strings(ci.FailedNames)
	sort.Strings(ci.PendingNames)
	return ci
}

// Facts is what the gate itself establishes about a PR; agents cannot
// influence it except by changing the PR.
type Facts struct {
	PR    PR
	Files []File
	CI    CI
	Flags []string
}

var sensitive = []struct {
	pattern *regexp.Regexp
	flag    string
}{
	{regexp.MustCompile(`^\.(forgejo|github)/workflows/`), "changes CI workflows"},
	{regexp.MustCompile(`(^|/)Dockerfile[^/]*$|(^|/)Containerfile$`), "changes container images"},
	{regexp.MustCompile(`(^|/)migrations?/`), "changes the database schema"},
	{regexp.MustCompile(`(^|/)crds?/|_types\.go$`), "changes API types or CRDs"},
	{regexp.MustCompile(`(?i)rbac|clusterrole|networkpolic|admission`), "changes access control"},
	{regexp.MustCompile(`(^|/)go\.(mod|sum)$|package(-lock)?\.json$|(^|/)requirements[^/]*\.txt$`), "changes dependencies"},
	{regexp.MustCompile(`(?i)(^|/)(secret|token|credential|auth|oidc|approvalgate)`), "touches credentials or approval code"},
	{regexp.MustCompile(`^(infra|clusters|charts|deploy|config)/`), "changes deployment manifests"},
}

// RiskFlags lists the sensitive areas a change touches.
func RiskFlags(files []File) []string {
	seen := map[string]bool{}
	var out []string
	for _, f := range files {
		for _, s := range sensitive {
			if !seen[s.flag] && s.pattern.MatchString(f.Name) {
				seen[s.flag] = true
				out = append(out, s.flag)
			}
		}
	}
	return out
}

// AuthorClaims are the PR description's own risk, door, rollback and hand
// steps. They are untrusted author text and always labelled as such.
type AuthorClaims struct {
	Risk, Door, Rollback, HandSteps string
}

var heading = regexp.MustCompile(`(?m)^\s{0,3}(?:#{1,6}\s*|\*\*)?([A-Za-z][A-Za-z /'-]{1,40}?)(?:\*\*)?\s*:?\s*$`)

// Claims extracts the author's claims from a PR body.
func Claims(body string) AuthorClaims {
	sections := map[string]string{}
	locs := heading.FindAllStringSubmatchIndex(body, -1)
	for i, l := range locs {
		name := strings.ToLower(strings.TrimSpace(body[l[2]:l[3]]))
		end := len(body)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		sections[name] = strings.TrimSpace(body[l[1]:end])
	}
	find := func(keys ...string) string {
		for name, text := range sections {
			for _, k := range keys {
				if strings.Contains(name, k) && text != "" {
					return oneLine(text, 240)
				}
			}
		}
		// Inline "Risk: low" lines.
		for _, line := range strings.Split(body, "\n") {
			l := strings.TrimLeft(strings.TrimSpace(line), "-*# ")
			for _, k := range keys {
				if strings.HasPrefix(strings.ToLower(l), k) {
					if _, v, ok := strings.Cut(l, ":"); ok && strings.TrimSpace(v) != "" {
						return oneLine(v, 240)
					}
				}
			}
		}
		return ""
	}
	c := AuthorClaims{Risk: find("risk"), Rollback: find("rollback", "roll back"), HandSteps: find("hand step", "manual step", "steve's step", "steps for steve")}
	lower := strings.ToLower(body)
	switch {
	case strings.Contains(lower, "one-way door") || strings.Contains(lower, "one way door"):
		c.Door = "one-way"
	case strings.Contains(lower, "two-way door") || strings.Contains(lower, "two way door"):
		c.Door = "two-way"
	}
	return c
}

func oneLine(s string, n int) string {
	return truncate(strings.Join(strings.Fields(s), " "), n)
}

func orDefault(s, d string) string {
	if strings.TrimSpace(s) == "" {
		return d
	}
	return s
}

// CardText is the text card posted to the Approvals room.
func CardText(f Facts, code string, expires time.Time, note string) string {
	pr := f.PR
	var b strings.Builder
	fmt.Fprintf(&b, "Approval card, code %s\n", code)
	fmt.Fprintf(&b, "%s PR #%d: %s\n", pr.Repo, pr.Number, pr.Title)
	fmt.Fprintf(&b, "%s\n", pr.URL)
	fmt.Fprintf(&b, "Author %s, %s into %s at %s\n", pr.Author, pr.HeadRef, pr.BaseRef, short(pr.HeadSHA))
	fmt.Fprintf(&b, "Changes: %d files, +%d -%d\n", len(f.Files), pr.Additions, pr.Deletions)
	for i, file := range f.Files {
		if i == 12 {
			fmt.Fprintf(&b, "  ... and %d more\n", len(f.Files)-12)
			break
		}
		fmt.Fprintf(&b, "  %s (+%d -%d)\n", file.Name, file.Additions, file.Deletions)
	}
	fmt.Fprintf(&b, "Checks: %s\n", f.CI)
	fmt.Fprintf(&b, "Mergeable: %s\n", yesNo(pr.Mergeable))
	if len(f.Flags) > 0 {
		fmt.Fprintf(&b, "Gate notes: %s\n", strings.Join(f.Flags, "; "))
	}
	c := Claims(pr.Body)
	b.WriteString("From the PR description (author's words, not verified):\n")
	fmt.Fprintf(&b, "  Risk: %s\n", orDefault(c.Risk, "not stated"))
	fmt.Fprintf(&b, "  Door: %s\n", orDefault(c.Door, "not stated"))
	fmt.Fprintf(&b, "  Rollback: %s\n", orDefault(c.Rollback, "not stated"))
	fmt.Fprintf(&b, "  Hand steps: %s\n", orDefault(c.HandSteps, "none stated"))
	if note = oneLine(note, 280); note != "" {
		fmt.Fprintf(&b, "Requesting agent's note (not verified): %s\n", note)
	}
	fmt.Fprintf(&b, "Reply \"approve %s\", \"reject %s because ...\", \"hold\" or \"tell me more about %s\". The code is single use and expires %s.",
		code, code, code, expires.UTC().Format("Mon 2 Jan 15:04 MST"))
	return b.String()
}

// CardSpeech is the spoken version of a card.
func CardSpeech(f Facts, code string) string {
	pr := f.PR
	c := Claims(pr.Body)
	var b strings.Builder
	fmt.Fprintf(&b, "Approval request for %s pull request %d: %s. ", spokenRepo(pr.Repo), pr.Number, pr.Title)
	fmt.Fprintf(&b, "%d files changed. %s. ", len(f.Files), f.CI.Spoken())
	if !pr.Mergeable {
		b.WriteString("It can't be merged as it stands. ")
	}
	if len(f.Flags) > 0 {
		fmt.Fprintf(&b, "It %s. ", strings.Join(f.Flags, ", and "))
	}
	if c.Door != "" {
		fmt.Fprintf(&b, "The description calls it a %s door. ", c.Door)
	}
	if c.Risk != "" {
		fmt.Fprintf(&b, "The description says the risk is: %s. ", truncate(c.Risk, 120))
	}
	fmt.Fprintf(&b, "To approve, say approve %s.", SpokenCode(code))
	return b.String()
}

// DetailText answers "tell me more".
func DetailText(f Facts) string {
	pr := f.PR
	var b strings.Builder
	fmt.Fprintf(&b, "%s PR #%d at %s: %s\n", pr.Repo, pr.Number, short(pr.HeadSHA), pr.Title)
	fmt.Fprintf(&b, "Files (%d):\n", len(f.Files))
	for i, file := range f.Files {
		if i == 50 {
			fmt.Fprintf(&b, "  ... and %d more\n", len(f.Files)-50)
			break
		}
		fmt.Fprintf(&b, "  %s %s (+%d -%d)\n", file.Status, file.Name, file.Additions, file.Deletions)
	}
	fmt.Fprintf(&b, "Checks: %s\n", f.CI)
	b.WriteString("Description (author's words, not verified):\n")
	b.WriteString(truncate(strings.TrimSpace(pr.Body), 1500))
	return b.String()
}

// DetailSpeech is the spoken version of DetailText.
func DetailSpeech(f Facts) string {
	pr := f.PR
	var names []string
	for i, file := range f.Files {
		if i == 5 {
			break
		}
		names = append(names, file.Name)
	}
	s := fmt.Sprintf("Pull request %d, %s. It adds %d lines and removes %d across %d files", pr.Number, pr.Title, pr.Additions, pr.Deletions, len(f.Files))
	if len(names) > 0 {
		s += ", including " + strings.Join(names, ", ")
	}
	s += ". " + f.CI.Spoken() + "."
	if body := oneLine(stripMarkdown(pr.Body), 600); body != "" {
		s += " The description says: " + body
	}
	return s
}

var mdNoise = regexp.MustCompile("[`*_#>|]+")

func stripMarkdown(s string) string { return mdNoise.ReplaceAllString(s, " ") }

func spokenRepo(repo string) string {
	_, name, _ := strings.Cut(repo, "/")
	return strings.NewReplacer("-", " ", "_", " ").Replace(name)
}

func short(sha string) string {
	if len(sha) > 10 {
		return sha[:10]
	}
	return sha
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
