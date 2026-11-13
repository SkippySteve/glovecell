package approvalgate

import "testing"

func TestParseTranscripts(t *testing.T) {
	cases := []struct {
		in   string
		want Intent
	}{
		{"Approve 4721.", Intent{Kind: KindApprove, Code: "4721"}},
		{"approve PR 459 code 4721", Intent{Kind: KindApprove, Code: "4721", PR: 459}},
		{"Approve pull request #459, code 4721.", Intent{Kind: KindApprove, Code: "4721", PR: 459}},
		{"Approve four seven two one", Intent{Kind: KindApprove, Code: "4721"}},
		{"Approve forty-seven twenty-one.", Intent{Kind: KindApprove, Code: "4721"}},
		{"approve 47-21", Intent{Kind: KindApprove, Code: "4721"}},
		{"Approve 4,721.", Intent{Kind: KindApprove, Code: "4721"}},
		{"Approve one oh two three", Intent{Kind: KindApprove, Code: "1023"}},
		{"Approve 4721 despite failing checks", Intent{Kind: KindApprove, Code: "4721", Despite: true}},
		{"approve 4721 despite the failed tests", Intent{Kind: KindApprove, Code: "4721", Despite: true}},
		{"approve 4721 even though checks fail", Intent{Kind: KindApprove, Code: "4721"}},
		{"Approve all on today's list, code 8830.", Intent{Kind: KindApprove, Code: "8830", All: true}},
		{"Reject 4721 because the migration has no down step.", Intent{Kind: KindReject, Code: "4721", Reason: "the migration has no down step"}},
		{"Hold.", Intent{Kind: KindHold}},
		{"Hold 4721, I want to read it first", Intent{Kind: KindHold, Code: "4721"}},
		{"Don't approve 4721 yet", Intent{Kind: KindHold, Code: "4721"}},
		{"Tell me more about 4721", Intent{Kind: KindMore, Code: "4721"}},
		{"Pause all merges!", Intent{Kind: KindPause}},
		{"Emergency stop, pause all merges, approve 4721", Intent{Kind: KindPause, Code: "4721"}},
		{"Resume merges.", Intent{Kind: KindResume}},
		{"What's pending?", Intent{Kind: KindList}},
		{"Revert PR 459", Intent{Kind: KindRevert, PR: 459}},
		{"Approve 4721 and reject 1234", Intent{Kind: KindNone}},
		{"Approve 4721 or 1234", Intent{Kind: KindNone, Problem: "more than one code"}},
		{"approve 4721, no wait, reject it", Intent{Kind: KindNone, Code: "4721", Problem: "more than one decision"}},
		{"Approve 4721, actually hold", Intent{Kind: KindNone, Code: "4721", Problem: "both approve and hold"}},
		{"Sounds good", Intent{}},
		{"I disapprove of 4721", Intent{Kind: KindHold, Code: "4721"}},
	}
	for _, c := range cases {
		got := Parse(c.in)
		if got.Kind != c.want.Kind || got.Code != c.want.Code || got.PR != c.want.PR || got.Despite != c.want.Despite || got.All != c.want.All ||
			(c.want.Reason != "" && got.Reason != c.want.Reason) || (c.want.Problem != "" && got.Problem != c.want.Problem) {
			t.Errorf("%q:\n got %+v\nwant %+v", c.in, got, c.want)
		}
	}
}

func TestNormalize(t *testing.T) {
	for in, want := range map[string]string{
		"Four seven two one":       "4721",
		"forty seven twenty one":   "4721",
		"Approve PR 12, code 3456": "approve pr 12 code 3456",
		"oh well, approve it":      "oh well approve it",
		"nineteen eighty four":     "1984",
	} {
		if got := Normalize(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}
