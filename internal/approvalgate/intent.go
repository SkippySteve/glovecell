package approvalgate

import (
	"regexp"
	"strconv"
	"strings"
)

// Kind is what Steve asked the gate to do.
type Kind string

const (
	KindNone    Kind = ""
	KindApprove Kind = "approve"
	KindReject  Kind = "reject"
	KindHold    Kind = "hold"
	KindMore    Kind = "more"
	KindPause   Kind = "pause"
	KindResume  Kind = "resume"
	KindList    Kind = "list"
	KindRevert  Kind = "revert"
)

// Intent is a parsed reply. Parsing is deliberately strict: anything
// ambiguous (two verbs, two codes, a negated approval) is KindNone with a
// Problem, and the gate refuses it.
type Intent struct {
	Kind Kind
	// Code is the 4-digit card or list code ("" if none was said).
	Code string
	// PR is a spoken PR number (0 if none).
	PR int
	// All marks "approve all …" (a list code).
	All bool
	// Despite marks the explicit CI override phrase.
	Despite bool
	// Reason is the text after "because".
	Reason string
	// Problem explains a KindNone result.
	Problem string
}

var (
	unitWords = map[string]string{"zero": "0", "oh": "0", "o": "0", "nought": "0", "one": "1", "two": "2", "three": "3", "four": "4", "for": "", "five": "5", "six": "6", "seven": "7", "eight": "8", "nine": "9"}
	teenWords = map[string]string{"ten": "10", "eleven": "11", "twelve": "12", "thirteen": "13", "fourteen": "14", "fifteen": "15", "sixteen": "16", "seventeen": "17", "eighteen": "18", "nineteen": "19"}
	tensWords = map[string]string{"twenty": "2", "thirty": "3", "forty": "4", "fifty": "5", "sixty": "6", "seventy": "7", "eighty": "8", "ninety": "9"}

	nonWord    = regexp.MustCompile(`[^a-z0-9#' ]+`)
	prRef      = regexp.MustCompile(`\b(?:pr|pull request|pull|merge request|number)\s*#?\s*(\d{1,6})\b|#(\d{1,6})\b`)
	codeRef    = regexp.MustCompile(`\b(?:code|approval code|list code|pin)\s*(?:is\s*)?(\d{4})\b`)
	fourDigits = regexp.MustCompile(`\b\d{4}\b`)
	despite    = regexp.MustCompile(`\bdespite\s+(?:the\s+)?(?:failing|failed|red|broken)\s+(?:checks?|ci|tests?|builds?)\b`)
	negApprove = regexp.MustCompile(`\b(?:don't|dont|do not|not|never)\s+(?:yet\s+)?approve\b|\bdisapprove\b|\bunapprove\b`)
)

// Normalize lowercases the transcript, strips punctuation and turns spoken
// numbers into digits: "four seven two one", "forty-seven twenty-one",
// "47-21" and "4,721" all become "4721".
func Normalize(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, "’", "'")
	s = strings.ReplaceAll(s, "-", " ")
	s = nonWord.ReplaceAllString(s, " ")
	words := strings.Fields(s)
	var out []string
	for i := 0; i < len(words); i++ {
		w := words[i]
		if t, ok := tensWords[w]; ok {
			if i+1 < len(words) {
				if u, ok := unitWords[words[i+1]]; ok && u != "" && u != "0" {
					out = append(out, t+u)
					i++
					continue
				}
			}
			out = append(out, t+"0")
			continue
		}
		if t, ok := teenWords[w]; ok {
			out = append(out, t)
			continue
		}
		// "o" and "oh" count as zero only next to other digits; "for" is
		// never a digit here.
		if u, ok := unitWords[w]; ok && u != "" {
			if (w == "o" || w == "oh") && !nearDigit(words, i) {
				out = append(out, w)
				continue
			}
			out = append(out, u)
			continue
		}
		out = append(out, w)
	}
	s = strings.Join(out, " ")
	// Join digit groups that belong together ("4 7 2 1", "47 21").
	for {
		next := joinDigits(s)
		if next == s {
			break
		}
		s = next
	}
	return s
}

func isDigitWord(w string) bool {
	if w == "" {
		return false
	}
	for _, r := range w {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func nearDigit(words []string, i int) bool {
	for _, j := range []int{i - 1, i + 1} {
		if j >= 0 && j < len(words) {
			w := words[j]
			if isDigitWord(w) {
				return true
			}
			if u, ok := unitWords[w]; ok && u != "" && w != "o" && w != "oh" {
				return true
			}
			if _, ok := tensWords[w]; ok {
				return true
			}
		}
	}
	return false
}

// joinDigits merges adjacent digit words while the result has at most four
// digits, so a PR number followed by a code stays apart only when a word
// separates them.
func joinDigits(s string) string {
	words := strings.Fields(s)
	var out []string
	for _, w := range words {
		if n := len(out); n > 0 && isDigitWord(w) && isDigitWord(out[n-1]) && len(out[n-1])+len(w) <= 4 {
			out[n-1] += w
			continue
		}
		out = append(out, w)
	}
	return strings.Join(out, " ")
}

func hasAny(s string, words ...string) bool {
	for _, w := range words {
		if regexp.MustCompile(`\b` + regexp.QuoteMeta(w) + `\b`).MatchString(s) {
			return true
		}
	}
	return false
}

// Parse reads one reply.
func Parse(transcript string) Intent {
	raw := strings.ToLower(transcript)
	s := Normalize(transcript)
	in := Intent{}
	if m := prRef.FindStringSubmatch(s); m != nil {
		v := m[1]
		if v == "" {
			v = m[2]
		}
		in.PR, _ = strconv.Atoi(v)
	}
	if i := strings.Index(s, "because "); i >= 0 {
		in.Reason = strings.TrimSpace(s[i+len("because "):])
		s = s[:i]
	} else if i := strings.Index(raw, " reason "); i >= 0 {
		in.Reason = strings.TrimSpace(raw[i+len(" reason "):])
	}
	// The code: after "code", else the only four-digit number that is not
	// the PR number.
	if m := codeRef.FindStringSubmatch(s); m != nil {
		in.Code = m[1]
	} else {
		var codes []string
		for _, c := range fourDigits.FindAllString(s, -1) {
			if n, _ := strconv.Atoi(c); n == in.PR && in.PR != 0 {
				continue
			}
			if !contains(codes, c) {
				codes = append(codes, c)
			}
		}
		switch len(codes) {
		case 1:
			in.Code = codes[0]
		case 0:
		default:
			in.Problem = "more than one code"
		}
	}
	in.Despite = despite.MatchString(s)

	verbs := map[Kind]bool{}
	switch {
	case hasAny(s, "pause all merges", "pause merges", "pause merging", "stop all merges", "stop merging", "freeze merges", "emergency stop"):
		verbs[KindPause] = true
	case hasAny(s, "resume merges", "resume merging", "unpause merges", "unpause", "restart merges"):
		verbs[KindResume] = true
	}
	if hasAny(s, "approve", "approved", "approving") {
		verbs[KindApprove] = true
	}
	if hasAny(s, "reject", "rejected", "decline") {
		verbs[KindReject] = true
	}
	if hasAny(s, "hold", "on hold", "wait", "not yet") {
		verbs[KindHold] = true
	}
	if hasAny(s, "tell me more", "more about", "more on", "details", "explain", "what does") {
		verbs[KindMore] = true
	}
	if hasAny(s, "revert") {
		verbs[KindRevert] = true
	}
	if hasAny(s, "list", "what's pending", "whats pending", "pending approvals", "what is pending") && !verbs[KindApprove] {
		verbs[KindList] = true
	}
	in.All = hasAny(s, "approve all", "all on", "everything on", "the whole list", "approve the list")

	if negApprove.MatchString(s) {
		// "don't approve 4721" is never an approval; treat it as a hold.
		delete(verbs, KindApprove)
		if !verbs[KindReject] {
			verbs[KindHold] = true
		}
	}
	// Pause and resume win over everything: they need no code.
	for _, k := range []Kind{KindPause, KindResume} {
		if verbs[k] {
			in.Kind = k
			return in
		}
	}
	// "not yet" inside "approve" sentences is a hold; "wait" alone too.
	if verbs[KindApprove] && verbs[KindHold] && !verbs[KindReject] {
		in.Kind, in.Problem = KindNone, "both approve and hold"
		return in
	}
	var picked []Kind
	for _, k := range []Kind{KindApprove, KindReject, KindRevert} {
		if verbs[k] {
			picked = append(picked, k)
		}
	}
	if len(picked) > 1 {
		in.Problem = "more than one decision"
		return in
	}
	switch {
	case len(picked) == 1:
		in.Kind = picked[0]
	case verbs[KindHold]:
		in.Kind = KindHold
	case verbs[KindMore]:
		in.Kind = KindMore
	case verbs[KindList]:
		in.Kind = KindList
	}
	if in.Kind == KindApprove && in.Problem != "" {
		in.Kind = KindNone
	}
	return in
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if x == y {
			return true
		}
	}
	return false
}

// SpokenCode spells a code digit by digit for speech ("4 7 2 1").
func SpokenCode(code string) string { return strings.Join(strings.Split(code, ""), " ") }
