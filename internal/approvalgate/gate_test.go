package approvalgate

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/voice/matrix"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/voice/store"
)

const (
	steve     = "@steve:example.com"
	approvals = "!approvals:example.com"
	repo      = "steve/agent-project-controller"
	sha1      = "1111111111111111111111111111111111111111"
	sha2      = "2222222222222222222222222222222222222222"
)

type posted struct{ room, thread, text, spoken string }

type fakeOut struct {
	mu    sync.Mutex
	posts []posted
	fail  error
}

func (o *fakeOut) Post(_ context.Context, room, thread, text, spoken string) (string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.fail != nil {
		return "", o.fail
	}
	o.posts = append(o.posts, posted{room, thread, text, spoken})
	return fmt.Sprintf("$post%d", len(o.posts)), nil
}

func (o *fakeOut) last() posted {
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.posts) == 0 {
		return posted{}
	}
	return o.posts[len(o.posts)-1]
}

type rig struct {
	gate     *Gate
	store    *store.Memory
	forge    *FakeForge
	out      *fakeOut
	feedback []string
	now      time.Time
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{store: store.NewMemory(), forge: NewFakeForge(), out: &fakeOut{}, now: time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)}
	g, err := New(Config{Steve: steve, Room: approvals, Repos: []string{"steve/*"}, MaxMergesPerHour: 3}, r.store, r.forge, r.out,
		func(_ context.Context, repo string, pr int, brief string) error {
			r.feedback = append(r.feedback, fmt.Sprintf("%s#%d: %s", repo, pr, brief))
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	g.Now = func() time.Time { return r.now }
	r.gate = g
	return r
}

var ctx = context.Background()

// card posts a card for a new PR and returns it.
func (r *rig) card(t *testing.T, n int, sha string) store.Card {
	t.Helper()
	r.forge.AddPR(repo, n, sha, fmt.Sprintf("Change %d", n))
	c, err := r.gate.RequestCard(ctx, CardRequest{Repo: repo, PR: n, RequestedBy: "orca", Note: "trust me, it is fine"})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

var eventN int

// steveSays is a verified, encrypted voice message from Steve.
func steveSays() matrix.Message {
	eventN++
	return matrix.Message{RoomID: approvals, EventID: fmt.Sprintf("$m%d", eventN), ThreadRoot: fmt.Sprintf("$m%d", eventN), Sender: steve, DeviceID: "PHONE",
		Kind: matrix.KindVoice, Encrypted: true, DeviceVerified: true, AudioSHA256: fmt.Sprintf("audio%d", eventN)}
}

func (r *rig) lastAudit(t *testing.T) store.AuditEntry {
	t.Helper()
	list, err := r.store.ListAudit(ctx, 1)
	if err != nil || len(list) == 0 {
		t.Fatalf("no audit: %v", err)
	}
	return list[0]
}

func TestHappyPathMerges(t *testing.T) {
	r := newRig(t)
	c := r.card(t, 459, sha1)
	if len(c.Code) != 4 || c.Code[0] == '0' {
		t.Fatalf("code %q", c.Code)
	}
	card := r.out.last()
	for _, want := range []string{"Approval card, code " + c.Code, "PR #459: Change 459", "internal/x.go", "Checks: 1 passed", "Risk: Low.", "Door: two-way", "Rollback: Revert the PR.", "not verified): trust me"} {
		if !strings.Contains(card.text, want) {
			t.Errorf("card lacks %q:\n%s", want, card.text)
		}
	}
	if !strings.Contains(card.spoken, "approve "+SpokenCode(c.Code)) || card.room != approvals {
		t.Fatalf("spoken card %q", card.spoken)
	}
	if r.forge.Status(sha1, "voice-approval") != "pending" {
		t.Fatal("pending voice-approval status not set")
	}
	msg := steveSays()
	d := r.gate.Handle(ctx, msg, "Approve PR 459 code "+c.Code+".")
	if d.Outcome != store.DecisionMerged {
		t.Fatalf("decision %+v", d)
	}
	if r.forge.MergedCount() != 1 || r.forge.Merged[0] != repo+"#459@"+sha1 {
		t.Fatalf("merged %v", r.forge.Merged)
	}
	if r.forge.Status(sha1, "voice-approval") != "success" {
		t.Fatal("voice-approval status not success")
	}
	if len(r.forge.Approvals) != 1 || r.forge.Approvals[0] != repo+"#459@"+sha1 {
		t.Fatalf("approving review %v", r.forge.Approvals)
	}
	if len(r.forge.Comments) != 1 || !strings.Contains(r.forge.Comments[0], "Approved by Steve by voice") || !strings.Contains(r.forge.Comments[0], msg.AudioSHA256) || !strings.Contains(r.forge.Comments[0], "code "+c.Code) {
		t.Fatalf("comment %v", r.forge.Comments)
	}
	a := r.lastAudit(t)
	if a.Decision != store.DecisionMerged || a.AudioSHA256 != msg.AudioSHA256 || a.DeviceID != "PHONE" || a.HeadSHA != sha1 || !a.Verified || !strings.Contains(a.Transcript, c.Code) {
		t.Fatalf("audit %+v", a)
	}
	if got, _ := r.store.GetCard(ctx, c.ID); got.State != store.CardMerged {
		t.Fatalf("card state %s", got.State)
	}
	if !strings.Contains(r.out.last().text, "Merged") || r.out.last().spoken == "" || r.out.last().thread != msg.ThreadRoot {
		t.Fatalf("confirmation %+v", r.out.last())
	}
}

func TestSpokenCodeForms(t *testing.T) {
	r := newRig(t)
	c := r.card(t, 1, sha1)
	words := map[rune]string{'0': "zero", '1': "one", '2': "two", '3': "three", '4': "four", '5': "five", '6': "six", '7': "seven", '8': "eight", '9': "nine"}
	var spoken []string
	for _, d := range c.Code {
		spoken = append(spoken, words[d])
	}
	if d := r.gate.Handle(ctx, steveSays(), "approve "+strings.Join(spoken, " ")); d.Outcome != store.DecisionMerged {
		t.Fatalf("spelled code: %+v", d)
	}
}

// refusal runs one message that must be refused and leaves nothing merged.
func refusal(t *testing.T, r *rig, msg matrix.Message, text, wantReason string) Decision {
	t.Helper()
	before := r.forge.MergedCount()
	d := r.gate.Handle(ctx, msg, text)
	if d.Outcome != store.DecisionRefused || !strings.Contains(d.Reason, wantReason) {
		t.Fatalf("%q: decision %+v, want refusal %q", text, d, wantReason)
	}
	if r.forge.MergedCount() != before {
		t.Fatal("merged despite refusal")
	}
	if a := r.lastAudit(t); a.Decision != store.DecisionRefused || !strings.Contains(a.Reason, wantReason) {
		t.Fatalf("refusal not audited: %+v", a)
	}
	return d
}

func TestRefusalWrongSender(t *testing.T) {
	r := newRig(t)
	c := r.card(t, 1, sha1)
	posts := len(r.out.posts)
	for _, sender := range []string{"@mallory:example.com", "@orca-agent:example.com", "@glovecell:example.com"} {
		m := steveSays()
		m.Sender = sender
		refusal(t, r, m, "approve "+c.Code, ReasonNotSteve)
	}
	if len(r.out.posts) != posts {
		t.Fatal("gate replied to a stranger")
	}
}

func TestRefusalUnverifiedDevice(t *testing.T) {
	r := newRig(t)
	c := r.card(t, 1, sha1)
	m := steveSays()
	m.DeviceVerified = false
	refusal(t, r, m, "approve "+c.Code, ReasonUnverified)
	m = steveSays()
	m.Encrypted = false
	refusal(t, r, m, "approve "+c.Code, ReasonUnencrypted)
	m = steveSays()
	m.RoomID = "!inbox:example.com"
	refusal(t, r, m, "approve "+c.Code, ReasonWrongRoom)
	// The card survives for a proper approval.
	if d := r.gate.Handle(ctx, steveSays(), "approve "+c.Code); d.Outcome != store.DecisionMerged {
		t.Fatalf("after refusals %+v", d)
	}
}

func TestRefusalEditedAndForwarded(t *testing.T) {
	r := newRig(t)
	c := r.card(t, 1, sha1)
	m := steveSays()
	m.Edited = true
	refusal(t, r, m, "approve "+c.Code, ReasonEdited)
	m = steveSays()
	m.Forwarded = true
	refusal(t, r, m, "approve "+c.Code, ReasonForwarded)
}

func TestRefusalReplayedAudio(t *testing.T) {
	r := newRig(t)
	c := r.card(t, 1, sha1)
	r.forge.SetChecks(sha1, Check{Context: "CI", State: "failure"})
	m := steveSays()
	refusal(t, r, m, "approve "+c.Code, ReasonCIRed)
	r.forge.SetChecks(sha1, Check{Context: "CI", State: "success"})
	m.EventID = "$again"
	refusal(t, r, m, "approve "+c.Code, ReasonReplayedAudio)
}

func TestRefusalWrongCode(t *testing.T) {
	r := newRig(t)
	c := r.card(t, 1, sha1)
	wrong := "1000"
	if c.Code == wrong {
		wrong = "1001"
	}
	refusal(t, r, steveSays(), "approve "+wrong, ReasonWrongCode)
	refusal(t, r, steveSays(), "approve it", ReasonNoCode)
	refusal(t, r, steveSays(), "approve PR 2 code "+c.Code, ReasonPRMismatch)
	refusal(t, r, steveSays(), "approve 1234 and 5678", ReasonUnclear)
}

func TestRefusalExpiredCode(t *testing.T) {
	r := newRig(t)
	c := r.card(t, 1, sha1)
	r.now = r.now.Add(24*time.Hour + time.Second)
	refusal(t, r, steveSays(), "approve "+c.Code, ReasonExpiredCode)
}

func TestRefusalReusedCode(t *testing.T) {
	r := newRig(t)
	c := r.card(t, 1, sha1)
	if d := r.gate.Handle(ctx, steveSays(), "approve "+c.Code); d.Outcome != store.DecisionMerged {
		t.Fatal(d)
	}
	r.forge.Update(repo, 1, func(p *PR) { p.Merged, p.State = false, "open" })
	refusal(t, r, steveSays(), "approve "+c.Code, ReasonUsedCode)
	if r.forge.MergedCount() != 1 {
		t.Fatal("merged twice")
	}
}

func TestRefusalSHAChangedIssuesNewCard(t *testing.T) {
	r := newRig(t)
	c := r.card(t, 1, sha1)
	r.forge.Push(repo, 1, sha2)
	refusal(t, r, steveSays(), "approve "+c.Code, ReasonSHAChanged)
	old, _ := r.store.GetCard(ctx, c.ID)
	if old.State != store.CardSuperseded {
		t.Fatalf("old card %s", old.State)
	}
	fresh, err := r.store.OpenCardFor(ctx, repo, 1)
	if err != nil || fresh.HeadSHA != sha2 || fresh.Code == c.Code {
		t.Fatalf("new card %+v %v", fresh, err)
	}
	refusal(t, r, steveSays(), "approve "+c.Code, "newer card")
	if d := r.gate.Handle(ctx, steveSays(), "approve "+fresh.Code); d.Outcome != store.DecisionMerged || r.forge.Merged[0] != repo+"#1@"+sha2 {
		t.Fatalf("new card approval %+v %v", d, r.forge.Merged)
	}
}

func TestRefusalCIRedWithoutOverride(t *testing.T) {
	r := newRig(t)
	c := r.card(t, 1, sha1)
	r.forge.SetChecks(sha1, Check{Context: "CI / checks", State: "failure"}, Check{Context: "security", State: "success"})
	refusal(t, r, steveSays(), "approve "+c.Code, ReasonCIRed)
	r.forge.SetChecks(sha1, Check{Context: "CI / checks", State: "pending"})
	refusal(t, r, steveSays(), "approve "+c.Code, ReasonCIRed)
	r.forge.SetChecks(sha1)
	refusal(t, r, steveSays(), "approve "+c.Code, ReasonCIRed)
	// Only the explicit phrase overrides.
	refusal(t, r, steveSays(), "approve "+c.Code+" anyway", ReasonCIRed)
	r.forge.SetChecks(sha1, Check{Context: "CI / checks", State: "failure"})
	if d := r.gate.Handle(ctx, steveSays(), "approve "+c.Code+" despite failing checks"); d.Outcome != store.DecisionMerged {
		t.Fatalf("override %+v", d)
	}
}

func TestRefusalNotMergeableOrClosed(t *testing.T) {
	r := newRig(t)
	c := r.card(t, 1, sha1)
	r.forge.Update(repo, 1, func(p *PR) { p.Mergeable = false })
	refusal(t, r, steveSays(), "approve "+c.Code, ReasonNotMergeable)
	r.forge.Update(repo, 1, func(p *PR) { p.Mergeable, p.State = true, "closed" })
	refusal(t, r, steveSays(), "approve "+c.Code, ReasonClosed)
}

func TestRefusalRateCap(t *testing.T) {
	r := newRig(t)
	var codes []string
	for i := 1; i <= 4; i++ {
		codes = append(codes, r.card(t, i, fmt.Sprintf("%040d", i)).Code)
	}
	for i := 0; i < 3; i++ {
		if d := r.gate.Handle(ctx, steveSays(), "approve "+codes[i]); d.Outcome != store.DecisionMerged {
			t.Fatalf("merge %d: %+v", i, d)
		}
	}
	refusal(t, r, steveSays(), "approve "+codes[3], ReasonRateCap)
	r.now = r.now.Add(61 * time.Minute)
	if d := r.gate.Handle(ctx, steveSays(), "approve "+codes[3]); d.Outcome != store.DecisionMerged {
		t.Fatalf("after an hour %+v", d)
	}
}

func TestPauseAndResume(t *testing.T) {
	r := newRig(t)
	c := r.card(t, 1, sha1)
	// Pause works from Steve's unverified device: it only stops things.
	m := steveSays()
	m.DeviceVerified = false
	if d := r.gate.Handle(ctx, m, "Pause all merges!"); d.Outcome != store.DecisionPaused {
		t.Fatalf("pause %+v", d)
	}
	refusal(t, r, steveSays(), "approve "+c.Code, ReasonPaused)
	// Resume needs the verified device.
	m = steveSays()
	m.DeviceVerified = false
	refusal(t, r, m, "resume merges", ReasonUnverified)
	// A stranger cannot pause either.
	m = steveSays()
	m.Sender = "@mallory:example.com"
	if d := r.gate.Handle(ctx, m, "pause all merges"); d.Outcome != store.DecisionRefused {
		t.Fatalf("stranger paused: %+v", d)
	}
	if d := r.gate.Handle(ctx, steveSays(), "resume merges"); d.Outcome != store.DecisionResumed {
		t.Fatalf("resume %+v", d)
	}
	if d := r.gate.Handle(ctx, steveSays(), "approve "+c.Code); d.Outcome != store.DecisionMerged {
		t.Fatalf("after resume %+v", d)
	}
}

func TestStoreDownFailsClosed(t *testing.T) {
	r := newRig(t)
	c := r.card(t, 1, sha1)
	r.store.Down = true
	d := r.gate.Handle(ctx, steveSays(), "approve "+c.Code)
	if d.Outcome != store.DecisionRefused || r.forge.MergedCount() != 0 {
		t.Fatalf("merged without the database: %+v", d)
	}
}

func TestRejectHoldAndMore(t *testing.T) {
	r := newRig(t)
	c := r.card(t, 7, sha1)
	if d := r.gate.Handle(ctx, steveSays(), "tell me more about "+c.Code); d.Outcome != store.DecisionInfo || !strings.Contains(r.out.last().text, "internal/x.go") {
		t.Fatalf("more %+v %q", d, r.out.last().text)
	}
	// A hold in the card's thread needs no code.
	m := steveSays()
	m.InThread, m.ThreadRoot = true, c.EventID
	if d := r.gate.Handle(ctx, m, "hold"); d.Outcome != store.DecisionHeld || len(r.feedback) != 1 {
		t.Fatalf("hold %+v %v", d, r.feedback)
	}
	if d := r.gate.Handle(ctx, steveSays(), "don't approve "+c.Code+" yet"); d.Outcome != store.DecisionHeld {
		t.Fatalf("negated approval %+v", d)
	}
	if d := r.gate.Handle(ctx, steveSays(), "Reject "+c.Code+" because the migration has no down step"); d.Outcome != store.DecisionRejected {
		t.Fatalf("reject %+v", d)
	}
	if r.forge.Status(sha1, "voice-approval") != "failure" {
		t.Fatal("rejection status not set")
	}
	if fb := r.feedback[len(r.feedback)-1]; !strings.Contains(fb, "the migration has no down step") || !strings.HasPrefix(fb, repo+"#7") {
		t.Fatalf("feedback %q", fb)
	}
	refusal(t, r, steveSays(), "approve "+c.Code, ReasonUsedCode)
}

func TestRevertAsksAnAgentNeverReverts(t *testing.T) {
	r := newRig(t)
	if d := r.gate.Handle(ctx, steveSays(), "Revert PR 459"); d.Outcome != store.DecisionRevert {
		t.Fatalf("revert %+v", d)
	}
	if len(r.feedback) != 1 || !strings.Contains(r.feedback[0], "revert pull request") || r.forge.MergedCount() != 0 {
		t.Fatalf("feedback %v", r.feedback)
	}
}

func TestListApproval(t *testing.T) {
	r := newRig(t)
	r.card(t, 1, sha1)
	r.card(t, 2, sha2)
	if d := r.gate.Handle(ctx, steveSays(), "What's pending?"); d.Outcome != store.DecisionCard {
		t.Fatalf("list %+v", d)
	}
	text := r.out.last().text
	code := strings.Fields(strings.SplitN(text, "code ", 2)[1])[0]
	// A list code does not approve when one item moved.
	r.forge.Push(repo, 2, "3333333333333333333333333333333333333333")
	refusal(t, r, steveSays(), "approve all on today's list code "+code, "PR #2")
	if r.forge.MergedCount() != 0 {
		t.Fatal("partial list merged")
	}
	// A fresh list covers the new card.
	r.gate.Handle(ctx, steveSays(), "list pending approvals")
	text = r.out.last().text
	code = strings.Fields(strings.SplitN(text, "code ", 2)[1])[0]
	if d := r.gate.Handle(ctx, steveSays(), "approve all on today's list code "+code); d.Outcome != store.DecisionMerged || r.forge.MergedCount() != 2 {
		t.Fatalf("list approval %+v merged %d", d, r.forge.MergedCount())
	}
	refusal(t, r, steveSays(), "approve all on today's list code "+code, ReasonUsedCode)
}

func TestListRespectsRateCap(t *testing.T) {
	r := newRig(t)
	for i := 1; i <= 4; i++ {
		r.card(t, i, fmt.Sprintf("%040d", i))
	}
	r.gate.Handle(ctx, steveSays(), "list")
	code := strings.Fields(strings.SplitN(r.out.last().text, "code ", 2)[1])[0]
	refusal(t, r, steveSays(), "approve all code "+code, ReasonRateCap)
}

func TestMergeFailureIsReportedAndAudited(t *testing.T) {
	r := newRig(t)
	c := r.card(t, 1, sha1)
	r.forge.MergeErr = errors.New("Forgejo returned HTTP 500")
	d := r.gate.Handle(ctx, steveSays(), "approve "+c.Code)
	if d.Outcome != store.DecisionMergeFailed || r.lastAudit(t).Decision != store.DecisionMergeFailed {
		t.Fatalf("decision %+v", d)
	}
	if r.forge.Status(sha1, "voice-approval") != "failure" {
		t.Fatal("status left at success after a failed merge")
	}
	if got, _ := r.store.GetCard(ctx, c.ID); got.State != store.CardFailed {
		t.Fatalf("card %s", got.State)
	}
}

func TestReviewFailureStopsTheMerge(t *testing.T) {
	r := newRig(t)
	c := r.card(t, 1, sha1)
	r.forge.ApproveErr = errors.New("Forgejo returned HTTP 403")
	d := r.gate.Handle(ctx, steveSays(), "approve "+c.Code)
	if d.Outcome != store.DecisionMergeFailed || r.forge.MergedCount() != 0 || !strings.Contains(d.Reason, "approving review") {
		t.Fatalf("decision %+v", d)
	}
	if r.forge.Status(sha1, "voice-approval") == "success" {
		t.Fatal("voice-approval success without a review")
	}
}

func TestRecoverAfterInterruptedMerge(t *testing.T) {
	r := newRig(t)
	a := r.card(t, 1, sha1)
	b := r.card(t, 2, sha2)
	// Crash after consuming both codes: #1 merged before the crash, #2 not.
	for _, c := range []store.Card{a, b} {
		if _, err := r.store.UseCard(ctx, c.ID, r.now); err != nil {
			t.Fatal(err)
		}
	}
	r.forge.Update(repo, 1, func(p *PR) { p.Merged, p.State = true, "closed" })
	if err := r.gate.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.store.GetCard(ctx, a.ID); got.State != store.CardMerged {
		t.Fatalf("merged card %s", got.State)
	}
	if got, _ := r.store.GetCard(ctx, b.ID); got.State != store.CardInterrupted {
		t.Fatalf("interrupted card %s", got.State)
	}
	if r.forge.MergedCount() != 0 {
		t.Fatal("recovery merged")
	}
	fresh, err := r.store.OpenCardFor(ctx, repo, 2)
	if err != nil || fresh.ID == b.ID {
		t.Fatalf("no new card after interruption: %+v %v", fresh, err)
	}
	refusal(t, r, steveSays(), "approve "+b.Code, "newer card")
	if d := r.gate.Handle(ctx, steveSays(), "approve "+fresh.Code); d.Outcome != store.DecisionMerged {
		t.Fatalf("approve new card %+v", d)
	}
}

func TestRequestCardLimits(t *testing.T) {
	r := newRig(t)
	if _, err := r.gate.RequestCard(ctx, CardRequest{Repo: "mallory/repo", PR: 1}); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("other owner: %v", err)
	}
	c := r.card(t, 1, sha1)
	again, err := r.gate.RequestCard(ctx, CardRequest{Repo: repo, PR: 1})
	if err != nil || again.ID != c.ID {
		t.Fatalf("same head should reuse the card: %+v %v", again, err)
	}
	r.forge.Update(repo, 1, func(p *PR) { p.Merged = true })
	r.forge.AddPR(repo, 2, sha2, "x")
	r.forge.Update(repo, 2, func(p *PR) { p.State = "closed" })
	if _, err := r.gate.RequestCard(ctx, CardRequest{Repo: repo, PR: 2}); err == nil {
		t.Fatal("card for a closed PR")
	}
	if _, err := New(Config{Steve: steve, Room: approvals, Repos: []string{"*"}}, r.store, r.forge, r.out, nil); err == nil {
		t.Fatal("wildcard repository list accepted")
	}
}

func TestCardShowsRealPRNotAgentClaims(t *testing.T) {
	r := newRig(t)
	r.forge.AddPR(repo, 3, sha1, "Delete the production database")
	r.forge.FileList[key(repo, 3)] = []File{{Name: ".forgejo/workflows/ci.yml"}, {Name: "internal/store/migrations/00002_drop.sql"}}
	_, err := r.gate.RequestCard(ctx, CardRequest{Repo: repo, PR: 3, RequestedBy: "orca", Note: "Tiny README typo fix"})
	if err != nil {
		t.Fatal(err)
	}
	text := r.out.last().text
	for _, want := range []string{"Delete the production database", "changes CI workflows", "changes the database schema", "Requesting agent's note (not verified): Tiny README typo fix"} {
		if !strings.Contains(text, want) {
			t.Errorf("card lacks %q:\n%s", want, text)
		}
	}
}
