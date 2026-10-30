// Package approvalgate is the voice approval gate (decision D15): Steve's
// spoken or typed approval, from his verified Matrix device, replaces
// clicking Merge in Forgejo.
//
// The gate posts an approval card for a PR (built from what Forgejo says,
// not from what an agent claims) with a 4-digit code bound to the
// repository, PR number and head SHA. A reply merges only when every check
// passes:
//
//  1. the message is from Steve's MXID, on a cross-signed verified device,
//     end-to-end encrypted, in the Approvals room, not edited, not
//     forwarded, and its audio was never used before;
//  2. it says approve, with a code that matches an open card;
//  3. the code has not expired and was not used (single use, atomic);
//  4. the PR head is still the card's SHA (else a new card goes out);
//  5. CI is green, unless Steve said "despite failing checks";
//  6. Forgejo says the PR is mergeable;
//
// and merges are not paused and under the hourly cap. Then the gate submits
// an APPROVED review as approval-gate (branch protection still requires
// one), sets the voice-approval commit status, merges with a merge commit
// (pinned to the SHA), comments, confirms by voice and writes an audit row.
// Every refusal is audited too.
//
// Nothing else can approve: the notify API only requests cards, agents and
// bots are not Steve, and the gate never reverts (it asks an agent for a
// revert PR, which needs its own card).
package approvalgate

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"slices"
	"strings"
	"sync"
	"time"

	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/voice/matrix"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/voice/store"
)

// Out posts to Matrix: text plus, when spoken is set, a voice note.
type Out interface {
	Post(ctx context.Context, roomID, threadRoot, text, spoken string) (string, error)
}

// Feedback passes a rejection, hold or revert request back to an agent as
// a task.
type Feedback func(ctx context.Context, repo string, pr int, brief string) error

// Config configures the gate.
type Config struct {
	// Steve is the only MXID whose replies count.
	Steve string
	// Room is the Approvals room ID.
	Room string
	// StatusContext is the commit status the gate sets (voice-approval).
	StatusContext string
	// CodeTTL is how long a card's code is valid (24h).
	CodeTTL time.Duration
	// MaxMergesPerHour caps merges (10).
	MaxMergesPerHour int
	// Repos limits the gate to these OWNER/NAME repositories or OWNER/*
	// owners. Empty refuses every repository.
	Repos []string
}

// Gate is the approval gate.
type Gate struct {
	cfg      Config
	store    store.Store
	forge    Forge
	out      Out
	feedback Feedback
	Now      func() time.Time
	mu       sync.Mutex // serialises decisions, so the hourly cap holds
}

// New returns a gate.
func New(cfg Config, s store.Store, f Forge, out Out, fb Feedback) (*Gate, error) {
	if cfg.Steve == "" || cfg.Room == "" {
		return nil, errors.New("approval gate needs Steve's MXID and the Approvals room")
	}
	if cfg.StatusContext == "" {
		cfg.StatusContext = "voice-approval"
	}
	if cfg.CodeTTL <= 0 {
		cfg.CodeTTL = 24 * time.Hour
	}
	if cfg.MaxMergesPerHour <= 0 {
		cfg.MaxMergesPerHour = 10
	}
	for _, r := range cfg.Repos {
		owner, name, ok := strings.Cut(r, "/")
		if !ok || owner == "" || (name != "*" && !ValidRepo(r)) {
			return nil, fmt.Errorf("approval gate repository %q must be OWNER/NAME or OWNER/*", r)
		}
	}
	if fb == nil {
		fb = func(context.Context, string, int, string) error { return nil }
	}
	return &Gate{cfg: cfg, store: s, forge: f, out: out, feedback: fb, Now: time.Now}, nil
}

// Room returns the Approvals room.
func (g *Gate) Room() string { return g.cfg.Room }

func (g *Gate) allowed(repo string) bool {
	owner, _, _ := strings.Cut(repo, "/")
	return ValidRepo(repo) && (slices.Contains(g.cfg.Repos, repo) || slices.Contains(g.cfg.Repos, owner+"/*"))
}

// Refusal reasons (stable strings; the audit and tests use them).
const (
	ReasonNotSteve      = "sender is not Steve"
	ReasonWrongRoom     = "not the Approvals room"
	ReasonUnencrypted   = "message was not end-to-end encrypted"
	ReasonUnverified    = "sending device is not cross-signed and verified"
	ReasonEdited        = "edited messages are not accepted"
	ReasonForwarded     = "forwarded messages are not accepted"
	ReasonReplayedAudio = "this recording was already used"
	ReasonUnclear       = "no clear decision"
	ReasonNoCode        = "no approval code"
	ReasonWrongCode     = "no open card has that code"
	ReasonUsedCode      = "that code was already used"
	ReasonExpiredCode   = "that code has expired"
	ReasonPRMismatch    = "the code belongs to another PR"
	ReasonSHAChanged    = "the PR changed since the card"
	ReasonCIRed         = "checks are not green"
	ReasonNotMergeable  = "the PR is not mergeable"
	ReasonClosed        = "the PR is closed or already merged"
	ReasonPaused        = "merges are paused"
	ReasonRateCap       = "the hourly merge limit is reached"
	ReasonRepo          = "repository not covered by the gate"
	ReasonStore         = "approval records are unavailable"
)

// Decision is what the gate did with one message.
type Decision struct {
	Kind    Kind
	Outcome string // a store.Decision* value
	Reason  string
	Cards   []int64
}

// Handle processes one message in the Approvals room. transcript is the
// text (typed, or transcribed from msg.Audio).
func (g *Gate) Handle(ctx context.Context, msg matrix.Message, transcript string) Decision {
	g.mu.Lock()
	defer g.mu.Unlock()
	in := Parse(transcript)
	base := store.AuditEntry{Sender: msg.Sender, DeviceID: msg.DeviceID, Verified: msg.DeviceVerified, RoomID: msg.RoomID, EventID: msg.EventID,
		Transcript: transcript, AudioSHA256: msg.AudioSHA256, Code: in.Code, PR: in.PR}
	refuse := func(reason string, card *store.Card) Decision {
		a := base
		a.Decision, a.Reason = store.DecisionRefused, reason
		if card != nil {
			a.CardID, a.Repo, a.PR, a.HeadSHA = card.ID, card.Repo, card.PR, card.HeadSHA
		}
		g.audit(ctx, a)
		g.say(ctx, msg, "Not done: "+reason+".", "Not done. "+capitalize(reason)+".")
		return Decision{Kind: in.Kind, Outcome: store.DecisionRefused, Reason: reason}
	}

	// Check 1, identity. Bots and other users never reach the gate (the
	// Matrix filter drops them); this is the second line.
	if msg.Sender != g.cfg.Steve {
		a := base
		a.Decision, a.Reason = store.DecisionRefused, ReasonNotSteve
		g.audit(ctx, a)
		return Decision{Kind: in.Kind, Outcome: store.DecisionRefused, Reason: ReasonNotSteve}
	}
	if msg.RoomID != g.cfg.Room {
		return refuse(ReasonWrongRoom, nil)
	}
	if !msg.Encrypted {
		return refuse(ReasonUnencrypted, nil)
	}
	// Pause is the emergency brake: Steve's account in the encrypted room
	// is enough, even on an unverified device, because it only stops.
	if in.Kind == KindPause && !msg.Edited && !msg.Forwarded {
		return g.setPaused(ctx, msg, base, true)
	}
	if msg.Edited {
		return refuse(ReasonEdited, nil)
	}
	if msg.Forwarded {
		return refuse(ReasonForwarded, nil)
	}
	if !msg.DeviceVerified {
		return refuse(ReasonUnverified, nil)
	}
	if msg.AudioSHA256 != "" {
		seen, err := g.store.AudioSeen(ctx, msg.AudioSHA256)
		if err != nil {
			return refuse(ReasonStore, nil)
		}
		if seen {
			return refuse(ReasonReplayedAudio, nil)
		}
	}

	switch in.Kind {
	case KindResume:
		return g.setPaused(ctx, msg, base, false)
	case KindList:
		return g.postList(ctx, msg, base)
	case KindRevert:
		return g.revert(ctx, msg, base, in)
	case KindApprove:
		// handled below
	case KindReject, KindHold, KindMore:
		card, reason := g.cardFor(ctx, in, msg)
		if reason != "" {
			return refuse(reason, card)
		}
		switch in.Kind {
		case KindReject:
			return g.reject(ctx, msg, base, in, card)
		case KindHold:
			return g.hold(ctx, msg, base, in, card)
		default:
			return g.more(ctx, msg, base, card)
		}
	default:
		reason := ReasonUnclear
		if in.Problem != "" {
			reason += " (" + in.Problem + ")"
		}
		return refuse(reason, nil)
	}

	// Approve. Check 2: a code for an open card.
	if in.Code == "" {
		return refuse(ReasonNoCode, nil)
	}
	card, err := g.store.CardByCode(ctx, in.Code)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return refuse(ReasonWrongCode, nil)
	case err != nil:
		return refuse(ReasonStore, nil)
	}
	// Check 3: single use and expiry.
	if card.State != store.CardOpen {
		if card.State == store.CardSuperseded || card.State == store.CardInterrupted {
			return refuse(ReasonSHAChanged+"; use the newer card", &card)
		}
		return refuse(ReasonUsedCode, &card)
	}
	if !g.Now().Before(card.ExpiresAt) {
		return refuse(ReasonExpiredCode, &card)
	}
	if card.Kind == store.CardList {
		return g.approveList(ctx, msg, base, in, card, refuse)
	}
	if !in.All && in.PR != 0 && in.PR != card.PR {
		return refuse(ReasonPRMismatch, &card)
	}
	if in.All {
		return refuse(ReasonPRMismatch+" (that is a PR code, not a list code)", &card)
	}
	gs, err := g.store.GetGateState(ctx)
	if err != nil {
		return refuse(ReasonStore, &card)
	}
	if gs.Paused {
		return refuse(ReasonPaused, &card)
	}
	if n, err := g.store.CountDecisions(ctx, store.DecisionMerged, g.Now().Add(-time.Hour)); err != nil {
		return refuse(ReasonStore, &card)
	} else if n >= g.cfg.MaxMergesPerHour {
		return refuse(ReasonRateCap, &card)
	}
	if reason := g.precheck(ctx, msg, &card, in.Despite); reason != "" {
		return refuse(reason, &card)
	}
	outcome, reason := g.merge(ctx, msg, base, card)
	return Decision{Kind: KindApprove, Outcome: outcome, Reason: reason, Cards: []int64{card.ID}}
}

// precheck runs checks 4 to 6 against Forgejo now.
func (g *Gate) precheck(ctx context.Context, msg matrix.Message, card *store.Card, despite bool) string {
	if !g.allowed(card.Repo) {
		return ReasonRepo
	}
	pr, err := g.forge.PullRequest(ctx, card.Repo, card.PR)
	if err != nil {
		return "Forgejo is unavailable"
	}
	if pr.Merged || pr.State != "open" {
		return ReasonClosed
	}
	if pr.HeadSHA != card.HeadSHA {
		// Check 4: the PR moved. Retire this card and send a fresh one.
		if err := g.store.CloseCard(ctx, card.ID, store.CardOpen, store.CardSuperseded, "head "+short(pr.HeadSHA)); err == nil {
			if _, err := g.requestCard(ctx, CardRequest{Repo: card.Repo, PR: card.PR, RequestedBy: "gate", Note: "The PR changed after its previous card."}); err != nil {
				slog.Warn("approval card not reissued", "repo", card.Repo, "pr", card.PR, "error", err.Error())
			}
		}
		return ReasonSHAChanged + "; a new card is on its way"
	}
	checks, err := g.forge.Checks(ctx, card.Repo, card.HeadSHA)
	if err != nil {
		return "Forgejo is unavailable"
	}
	if ci := Summarize(checks, g.cfg.StatusContext); !ci.Green() && !despite {
		return ReasonCIRed + " (" + ci.String() + "); say \"approve " + card.Code + " despite failing checks\" to override"
	}
	if !pr.Mergeable {
		return ReasonNotMergeable
	}
	return ""
}

// merge consumes the code and merges. The code is consumed first, so a
// crash leaves an approved card that Recover resolves; it never merges
// twice and never merges without a consumed code.
func (g *Gate) merge(ctx context.Context, msg matrix.Message, base store.AuditEntry, card store.Card) (string, string) {
	now := g.Now()
	if _, err := g.store.UseCard(ctx, card.ID, now); err != nil {
		reason := ReasonUsedCode
		if errors.Is(err, store.ErrUnavailable) {
			reason = ReasonStore
		}
		a := base
		a.Decision, a.Reason, a.CardID, a.Repo, a.PR, a.HeadSHA = store.DecisionRefused, reason, card.ID, card.Repo, card.PR, card.HeadSHA
		g.audit(ctx, a)
		g.say(ctx, msg, "Not done: "+reason+".", "Not done. "+capitalize(reason)+".")
		return store.DecisionRefused, reason
	}
	stamp := now.UTC().Format(time.RFC3339)
	desc := fmt.Sprintf("Approved by Steve by voice at %s (code %s)", stamp, card.Code)
	review := fmt.Sprintf("%s.\n\nApproved by voice from Steve's verified device `%s`; head `%s`. Transcript: %q", desc, msg.DeviceID, card.HeadSHA, base.Transcript)
	if err := g.forge.Approve(ctx, card.Repo, card.PR, card.HeadSHA, review); err != nil {
		return g.mergeFailed(ctx, msg, base, card, "could not submit the approving review: "+err.Error())
	}
	if err := g.forge.SetStatus(ctx, card.Repo, card.HeadSHA, Check{Context: g.cfg.StatusContext, State: "success", Description: desc}); err != nil {
		return g.mergeFailed(ctx, msg, base, card, "could not set the voice-approval status: "+err.Error())
	}
	title := fmt.Sprintf("Merge PR #%d: %s", card.PR, card.Title)
	message := fmt.Sprintf("Approved-by-voice: %s, device %s, code %s, head %s, %s", msg.Sender, msg.DeviceID, card.Code, card.HeadSHA, stamp)
	if err := g.forge.Merge(ctx, card.Repo, card.PR, card.HeadSHA, title, message); err != nil {
		_ = g.forge.SetStatus(ctx, card.Repo, card.HeadSHA, Check{Context: g.cfg.StatusContext, State: "failure", Description: "Merge after voice approval failed; a new card is needed"})
		return g.mergeFailed(ctx, msg, base, card, "merge failed: "+err.Error())
	}
	if err := g.store.CloseCard(ctx, card.ID, store.CardApproved, store.CardMerged, "merged"); err != nil {
		slog.Error("approval card not closed after merge; recovery will close it", "card", card.ID, "error", err.Error())
	}
	a := base
	a.Decision, a.CardID, a.Repo, a.PR, a.HeadSHA, a.Code = store.DecisionMerged, card.ID, card.Repo, card.PR, card.HeadSHA, card.Code
	g.audit(ctx, a)
	comment := fmt.Sprintf("Approved by Steve by voice at %s, code %s, head %s.\n\nTranscript: %q\n\nAudio SHA-256: `%s` (device `%s`). Merged by the approval gate.",
		stamp, card.Code, card.HeadSHA, base.Transcript, orDefault(msg.AudioSHA256, "none (typed)"), msg.DeviceID)
	if err := g.forge.Comment(ctx, card.Repo, card.PR, comment); err != nil {
		slog.Warn("approval comment failed", "repo", card.Repo, "pr", card.PR, "error", err.Error())
	}
	g.say(ctx, msg, fmt.Sprintf("Merged %s PR #%d (%s) at %s.", card.Repo, card.PR, card.Title, short(card.HeadSHA)),
		fmt.Sprintf("Merged pull request %d, %s.", card.PR, card.Title))
	return store.DecisionMerged, ""
}

func (g *Gate) mergeFailed(ctx context.Context, msg matrix.Message, base store.AuditEntry, card store.Card, reason string) (string, string) {
	_ = g.store.CloseCard(ctx, card.ID, store.CardApproved, store.CardFailed, truncate(reason, 300))
	a := base
	a.Decision, a.Reason, a.CardID, a.Repo, a.PR, a.HeadSHA = store.DecisionMergeFailed, truncate(reason, 500), card.ID, card.Repo, card.PR, card.HeadSHA
	g.audit(ctx, a)
	g.say(ctx, msg, fmt.Sprintf("Approved, but %s. Ask for a new card when it is fixed.", reason),
		fmt.Sprintf("Approved, but the merge of pull request %d failed.", card.PR))
	return store.DecisionMergeFailed, reason
}

// approveList merges every PR on a list card, all or nothing for the
// checks: if any item fails a check, nothing merges.
func (g *Gate) approveList(ctx context.Context, msg matrix.Message, base store.AuditEntry, in Intent, list store.Card, refuse func(string, *store.Card) Decision) Decision {
	gs, err := g.store.GetGateState(ctx)
	if err != nil {
		return refuse(ReasonStore, &list)
	}
	if gs.Paused {
		return refuse(ReasonPaused, &list)
	}
	n, err := g.store.CountDecisions(ctx, store.DecisionMerged, g.Now().Add(-time.Hour))
	if err != nil {
		return refuse(ReasonStore, &list)
	}
	if n+len(list.Items) > g.cfg.MaxMergesPerHour {
		return refuse(ReasonRateCap, &list)
	}
	var cards []store.Card
	for _, it := range list.Items {
		c, err := g.store.GetCard(ctx, it.CardID)
		if err != nil {
			return refuse(ReasonStore, &list)
		}
		if c.State != store.CardOpen || c.HeadSHA != it.HeadSHA || c.Repo != it.Repo || c.PR != it.PR {
			return refuse(fmt.Sprintf("%s PR #%d on the list is no longer open as listed", it.Repo, it.PR), &list)
		}
		if !g.Now().Before(c.ExpiresAt) {
			return refuse(fmt.Sprintf("%s for %s PR #%d", ReasonExpiredCode, it.Repo, it.PR), &list)
		}
		if reason := g.precheck(ctx, msg, &c, in.Despite); reason != "" {
			return refuse(fmt.Sprintf("%s PR #%d: %s", it.Repo, it.PR, reason), &list)
		}
		cards = append(cards, c)
	}
	if _, err := g.store.UseCard(ctx, list.ID, g.Now()); err != nil {
		return refuse(ReasonUsedCode, &list)
	}
	d := Decision{Kind: KindApprove, Outcome: store.DecisionMerged, Cards: []int64{list.ID}}
	failed := 0
	for _, c := range cards {
		outcome, _ := g.merge(ctx, msg, base, c)
		d.Cards = append(d.Cards, c.ID)
		if outcome != store.DecisionMerged {
			failed++
		}
	}
	final := store.CardMerged
	if failed > 0 {
		final, d.Outcome, d.Reason = store.CardFailed, store.DecisionMergeFailed, fmt.Sprintf("%d of %d merges failed", failed, len(cards))
	}
	_ = g.store.CloseCard(ctx, list.ID, store.CardApproved, final, d.Reason)
	return d
}

// cardFor finds the card a reject, hold or "tell me more" refers to: by
// code, else the card whose thread the reply is in.
func (g *Gate) cardFor(ctx context.Context, in Intent, msg matrix.Message) (*store.Card, string) {
	if in.Code != "" {
		c, err := g.store.CardByCode(ctx, in.Code)
		if errors.Is(err, store.ErrNotFound) {
			return nil, ReasonWrongCode
		}
		if err != nil {
			return nil, ReasonStore
		}
		if c.State != store.CardOpen {
			return &c, ReasonUsedCode
		}
		return &c, ""
	}
	if msg.InThread {
		open, err := g.store.OpenCards(ctx, g.Now())
		if err != nil {
			return nil, ReasonStore
		}
		for i := range open {
			if open[i].EventID != "" && open[i].EventID == msg.ThreadRoot {
				return &open[i], ""
			}
		}
	}
	if in.PR != 0 {
		open, err := g.store.OpenCards(ctx, g.Now())
		if err != nil {
			return nil, ReasonStore
		}
		var match []store.Card
		for _, c := range open {
			if c.PR == in.PR {
				match = append(match, c)
			}
		}
		if len(match) == 1 {
			return &match[0], ""
		}
	}
	return nil, ReasonNoCode
}

func (g *Gate) reject(ctx context.Context, msg matrix.Message, base store.AuditEntry, in Intent, card *store.Card) Decision {
	if card.Kind == store.CardList {
		_ = g.store.CloseCard(ctx, card.ID, store.CardOpen, store.CardRejected, in.Reason)
		g.say(ctx, msg, "List rejected; the PR cards on it stay open.", "List rejected.")
		return Decision{Kind: KindReject, Outcome: store.DecisionRejected, Cards: []int64{card.ID}}
	}
	if err := g.store.CloseCard(ctx, card.ID, store.CardOpen, store.CardRejected, in.Reason); err != nil {
		g.say(ctx, msg, "Not done: "+ReasonUsedCode+".", "Not done. That code was already used.")
		return Decision{Kind: KindReject, Outcome: store.DecisionRefused, Reason: ReasonUsedCode}
	}
	a := base
	a.Decision, a.Reason, a.CardID, a.Repo, a.PR, a.HeadSHA, a.Code = store.DecisionRejected, in.Reason, card.ID, card.Repo, card.PR, card.HeadSHA, card.Code
	g.audit(ctx, a)
	_ = g.forge.SetStatus(ctx, card.Repo, card.HeadSHA, Check{Context: g.cfg.StatusContext, State: "failure", Description: "Rejected by Steve by voice"})
	_ = g.forge.Comment(ctx, card.Repo, card.PR, fmt.Sprintf("Rejected by Steve by voice at %s.\n\nReason: %s", g.Now().UTC().Format(time.RFC3339), orDefault(in.Reason, "none given")))
	brief := fmt.Sprintf("Steve rejected %s PR #%d (%s) by voice. Reason: %s. Address the feedback with new commits on the PR, or close it if it should not land, then request a new approval card.",
		card.Repo, card.PR, card.Title, orDefault(in.Reason, "none given"))
	if err := g.feedback(ctx, card.Repo, card.PR, brief); err != nil {
		slog.Warn("rejection feedback not sent", "error", err.Error())
	}
	g.say(ctx, msg, fmt.Sprintf("Rejected %s PR #%d. I've passed your reason to the agent.", card.Repo, card.PR), fmt.Sprintf("Rejected pull request %d.", card.PR))
	return Decision{Kind: KindReject, Outcome: store.DecisionRejected, Cards: []int64{card.ID}}
}

func (g *Gate) hold(ctx context.Context, msg matrix.Message, base store.AuditEntry, in Intent, card *store.Card) Decision {
	a := base
	a.Decision, a.Reason, a.CardID, a.Repo, a.PR, a.HeadSHA, a.Code = store.DecisionHeld, in.Reason, card.ID, card.Repo, card.PR, card.HeadSHA, card.Code
	g.audit(ctx, a)
	if card.Kind == store.CardPR {
		_ = g.forge.Comment(ctx, card.Repo, card.PR, fmt.Sprintf("Put on hold by Steve by voice at %s. %s", g.Now().UTC().Format(time.RFC3339), orDefault(in.Reason, "")))
		brief := fmt.Sprintf("Steve put %s PR #%d (%s) on hold by voice. Wait for his instructions before asking for approval again.", card.Repo, card.PR, card.Title)
		if in.Reason != "" {
			brief = fmt.Sprintf("Steve put %s PR #%d (%s) on hold by voice. Reason: %s. Address it before asking for approval again.", card.Repo, card.PR, card.Title, in.Reason)
		}
		if err := g.feedback(ctx, card.Repo, card.PR, brief); err != nil {
			slog.Warn("hold feedback not sent", "error", err.Error())
		}
	}
	g.say(ctx, msg, "On hold. The card stays open until it expires.", "On hold.")
	return Decision{Kind: KindHold, Outcome: store.DecisionHeld, Cards: []int64{card.ID}}
}

func (g *Gate) more(ctx context.Context, msg matrix.Message, base store.AuditEntry, card *store.Card) Decision {
	a := base
	a.Decision, a.CardID, a.Repo, a.PR, a.HeadSHA = store.DecisionInfo, card.ID, card.Repo, card.PR, card.HeadSHA
	g.audit(ctx, a)
	if card.Kind == store.CardList {
		var lines []string
		for _, it := range card.Items {
			lines = append(lines, fmt.Sprintf("%s PR #%d at %s", it.Repo, it.PR, short(it.HeadSHA)))
		}
		g.say(ctx, msg, "List "+card.Code+":\n"+strings.Join(lines, "\n"), fmt.Sprintf("The list has %d pull requests.", len(card.Items)))
		return Decision{Kind: KindMore, Outcome: store.DecisionInfo}
	}
	f, err := g.facts(ctx, card.Repo, card.PR)
	if err != nil {
		g.say(ctx, msg, "I couldn't read the PR from Forgejo.", "I couldn't read that pull request.")
		return Decision{Kind: KindMore, Outcome: store.DecisionInfo, Reason: err.Error()}
	}
	text := DetailText(f)
	if f.PR.HeadSHA != card.HeadSHA {
		text = "Note: the PR changed since the card; approving needs a new card.\n" + text
	}
	g.say(ctx, msg, text, DetailSpeech(f))
	return Decision{Kind: KindMore, Outcome: store.DecisionInfo, Cards: []int64{card.ID}}
}

func (g *Gate) revert(ctx context.Context, msg matrix.Message, base store.AuditEntry, in Intent) Decision {
	if in.PR == 0 {
		g.say(ctx, msg, "Which PR? Say \"revert PR\" and its number.", "Which pull request should be reverted?")
		return Decision{Kind: KindRevert, Outcome: store.DecisionRefused, Reason: "no PR number"}
	}
	a := base
	a.Decision = store.DecisionRevert
	g.audit(ctx, a)
	brief := fmt.Sprintf("Steve asked by voice to revert PR #%d. Open a revert pull request (git revert of its merge commit) and request an approval card for it. Do not merge anything yourself. His words: %q", in.PR, base.Transcript)
	if err := g.feedback(ctx, "", in.PR, brief); err != nil {
		g.say(ctx, msg, "I couldn't hand the revert to an agent: "+err.Error(), "I couldn't hand the revert to an agent.")
		return Decision{Kind: KindRevert, Outcome: store.DecisionRefused, Reason: err.Error()}
	}
	g.say(ctx, msg, fmt.Sprintf("Asked an agent to open a revert PR for #%d. It will need its own approval card.", in.PR), fmt.Sprintf("I've asked an agent to open a revert for pull request %d. It will come back for approval.", in.PR))
	return Decision{Kind: KindRevert, Outcome: store.DecisionRevert}
}

func (g *Gate) setPaused(ctx context.Context, msg matrix.Message, base store.AuditEntry, paused bool) Decision {
	kind, decision, text := KindResume, store.DecisionResumed, "Merges resumed."
	if paused {
		kind, decision, text = KindPause, store.DecisionPaused, "All merges paused. Say \"resume merges\" to continue."
	}
	if err := g.store.SetGateState(ctx, store.GateState{Paused: paused, ChangedBy: msg.Sender, ChangedAt: g.Now()}); err != nil {
		g.say(ctx, msg, "Couldn't change the pause switch: the database is unavailable. No merges happen while it is down.", "The database is unavailable, so nothing merges.")
		return Decision{Kind: kind, Outcome: store.DecisionRefused, Reason: ReasonStore}
	}
	a := base
	a.Decision = decision
	g.audit(ctx, a)
	g.say(ctx, msg, text, text)
	return Decision{Kind: kind, Outcome: decision}
}

// postList posts a list card covering every open PR card.
func (g *Gate) postList(ctx context.Context, msg matrix.Message, base store.AuditEntry) Decision {
	open, err := g.store.OpenCards(ctx, g.Now())
	if err != nil {
		g.say(ctx, msg, "Approval records are unavailable.", "Approval records are unavailable.")
		return Decision{Kind: KindList, Outcome: store.DecisionRefused, Reason: ReasonStore}
	}
	if len(open) == 0 {
		g.say(ctx, msg, "Nothing is waiting for approval.", "Nothing is waiting for approval.")
		return Decision{Kind: KindList, Outcome: store.DecisionInfo}
	}
	var items []store.ListItem
	var lines []string
	expires := g.Now().Add(g.cfg.CodeTTL)
	for _, c := range open {
		items = append(items, store.ListItem{CardID: c.ID, Repo: c.Repo, PR: c.PR, HeadSHA: c.HeadSHA})
		lines = append(lines, fmt.Sprintf("  %s PR #%d: %s (code %s)", c.Repo, c.PR, c.Title, c.Code))
		if c.ExpiresAt.Before(expires) {
			expires = c.ExpiresAt
		}
	}
	code, err := g.newCode(ctx)
	if err != nil {
		g.say(ctx, msg, "Approval records are unavailable.", "Approval records are unavailable.")
		return Decision{Kind: KindList, Outcome: store.DecisionRefused, Reason: ReasonStore}
	}
	list, err := g.store.CreateCard(ctx, store.Card{Kind: store.CardList, Code: code, Items: items, RoomID: g.cfg.Room, RequestedBy: msg.Sender, State: store.CardOpen, ExpiresAt: expires, CreatedAt: g.Now()})
	if err != nil {
		g.say(ctx, msg, "Approval records are unavailable.", "Approval records are unavailable.")
		return Decision{Kind: KindList, Outcome: store.DecisionRefused, Reason: ReasonStore}
	}
	a := base
	a.Decision, a.CardID, a.Code = store.DecisionCard, list.ID, code
	g.audit(ctx, a)
	text := fmt.Sprintf("Today's list, code %s (bound to these PRs at their current heads):\n%s\nSay \"approve all on today's list code %s\" to merge them all, or use each PR's own code.", code, strings.Join(lines, "\n"), code)
	spoken := fmt.Sprintf("%d pull requests are waiting. To approve them all, say approve all on today's list, code %s.", len(items), SpokenCode(code))
	id, _ := g.out.Post(ctx, g.cfg.Room, msg.ThreadRoot, text, spoken)
	if id != "" {
		_ = g.store.SetCardEvent(ctx, list.ID, id)
	}
	return Decision{Kind: KindList, Outcome: store.DecisionCard, Cards: []int64{list.ID}}
}

func (g *Gate) facts(ctx context.Context, repo string, n int) (Facts, error) {
	pr, err := g.forge.PullRequest(ctx, repo, n)
	if err != nil {
		return Facts{}, err
	}
	files, err := g.forge.Files(ctx, repo, n)
	if err != nil {
		return Facts{}, err
	}
	checks, err := g.forge.Checks(ctx, repo, pr.HeadSHA)
	if err != nil {
		return Facts{}, err
	}
	return Facts{PR: pr, Files: files, CI: Summarize(checks, g.cfg.StatusContext), Flags: RiskFlags(files)}, nil
}

// CardRequest is a request for an approval card.
type CardRequest struct {
	Repo string
	PR   int
	// RequestedBy names the caller (a notify token's name, or "gate").
	RequestedBy string
	// Note is the requester's own words, shown labelled as unverified.
	Note string
}

// ErrNotAllowed reports a repository outside the gate's list.
var ErrNotAllowed = errors.New("repository not covered by the approval gate")

// RequestCard posts an approval card for the PR's current head. An open
// card for the same head is reused; a card for an older head is retired.
// It never approves anything.
func (g *Gate) RequestCard(ctx context.Context, r CardRequest) (store.Card, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.requestCard(ctx, r)
}

func (g *Gate) requestCard(ctx context.Context, r CardRequest) (store.Card, error) {
	if !g.allowed(r.Repo) || r.PR <= 0 {
		return store.Card{}, ErrNotAllowed
	}
	f, err := g.facts(ctx, r.Repo, r.PR)
	if err != nil {
		return store.Card{}, fmt.Errorf("read PR: %w", err)
	}
	if f.PR.Merged || f.PR.State != "open" {
		return store.Card{}, errors.New("the PR is closed or already merged")
	}
	if old, err := g.store.OpenCardFor(ctx, r.Repo, r.PR); err == nil {
		if old.HeadSHA == f.PR.HeadSHA && g.Now().Before(old.ExpiresAt) {
			return old, nil
		}
		_ = g.store.CloseCard(ctx, old.ID, store.CardOpen, store.CardSuperseded, "new head "+short(f.PR.HeadSHA))
	} else if !errors.Is(err, store.ErrNotFound) {
		return store.Card{}, err
	}
	var card store.Card
	for attempt := 0; attempt < 5; attempt++ {
		code, err := g.newCode(ctx)
		if err != nil {
			return store.Card{}, err
		}
		now := g.Now()
		card, err = g.store.CreateCard(ctx, store.Card{Kind: store.CardPR, Code: code, Repo: r.Repo, PR: r.PR, HeadSHA: f.PR.HeadSHA, Title: truncate(f.PR.Title, 200),
			RoomID: g.cfg.Room, RequestedBy: r.RequestedBy, State: store.CardOpen, CreatedAt: now, ExpiresAt: now.Add(g.cfg.CodeTTL)})
		if err == nil {
			break
		}
		if !errors.Is(err, store.ErrConflict) {
			return store.Card{}, err
		}
	}
	if card.ID == 0 {
		return store.Card{}, errors.New("no free approval code")
	}
	g.audit(ctx, store.AuditEntry{Sender: r.RequestedBy, Repo: r.Repo, PR: r.PR, HeadSHA: card.HeadSHA, Code: card.Code, CardID: card.ID, Decision: store.DecisionCard, Reason: oneLine(r.Note, 200)})
	_ = g.forge.SetStatus(ctx, r.Repo, card.HeadSHA, Check{Context: g.cfg.StatusContext, State: "pending", Description: "Waiting for Steve's voice approval"})
	id, err := g.out.Post(ctx, g.cfg.Room, "", CardText(f, card.Code, card.ExpiresAt, r.Note), CardSpeech(f, card.Code))
	if err != nil {
		return card, fmt.Errorf("card stored but not posted: %w", err)
	}
	_ = g.store.SetCardEvent(ctx, card.ID, id)
	card.EventID = id
	return card, nil
}

// newCode picks a random 4-digit code (1000-9999, so no leading zero gets
// lost in transcription) not used by an open card or in the last week.
func (g *Gate) newCode(ctx context.Context) (string, error) {
	used, err := g.store.RecentCodes(ctx, g.Now().Add(-7*24*time.Hour))
	if err != nil {
		return "", err
	}
	for i := 0; i < 100; i++ {
		n, err := rand.Int(rand.Reader, big.NewInt(9000))
		if err != nil {
			return "", err
		}
		code := fmt.Sprintf("%04d", 1000+n.Int64())
		if !used[code] {
			return code, nil
		}
	}
	return "", errors.New("no free approval code")
}

// Recover resolves cards left approved by a crash between consuming the
// code and finishing the merge. A merged PR is recorded as merged; anything
// else is marked interrupted, its voice-approval status is withdrawn and a
// new card goes out. It never merges.
func (g *Gate) Recover(ctx context.Context) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	cards, err := g.store.CardsInState(ctx, store.CardApproved)
	if err != nil {
		return err
	}
	var errs []error
	for _, c := range cards {
		if c.Kind == store.CardList {
			// Its PR cards are resolved individually.
			_ = g.store.CloseCard(ctx, c.ID, store.CardApproved, store.CardInterrupted, "service restarted")
			continue
		}
		pr, err := g.forge.PullRequest(ctx, c.Repo, c.PR)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if pr.Merged {
			if err := g.store.CloseCard(ctx, c.ID, store.CardApproved, store.CardMerged, "merged; recorded after restart"); err != nil {
				errs = append(errs, err)
				continue
			}
			g.audit(ctx, store.AuditEntry{Sender: "gate", Repo: c.Repo, PR: c.PR, HeadSHA: c.HeadSHA, Code: c.Code, CardID: c.ID, Decision: store.DecisionMerged, Reason: "merge completed before a restart; recorded on recovery"})
			continue
		}
		if err := g.store.CloseCard(ctx, c.ID, store.CardApproved, store.CardInterrupted, "service restarted before the merge"); err != nil {
			errs = append(errs, err)
			continue
		}
		_ = g.forge.SetStatus(ctx, c.Repo, c.HeadSHA, Check{Context: g.cfg.StatusContext, State: "pending", Description: "Approval interrupted; waiting for a new voice approval"})
		g.audit(ctx, store.AuditEntry{Sender: "gate", Repo: c.Repo, PR: c.PR, HeadSHA: c.HeadSHA, Code: c.Code, CardID: c.ID, Decision: store.DecisionInterrupted, Reason: "service restarted before the merge"})
		if pr.State == "open" {
			if _, err := g.requestCard(ctx, CardRequest{Repo: c.Repo, PR: c.PR, RequestedBy: "gate", Note: "The previous approval was interrupted by a restart; approve again."}); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

func (g *Gate) audit(ctx context.Context, a store.AuditEntry) {
	if a.Time.IsZero() {
		a.Time = g.Now()
	}
	if _, err := g.store.AppendAudit(ctx, a); err != nil {
		slog.Error("approval audit row not written", "decision", a.Decision, "error", err.Error())
	}
}

func (g *Gate) say(ctx context.Context, msg matrix.Message, text, spoken string) {
	if _, err := g.out.Post(ctx, g.cfg.Room, msg.ThreadRoot, text, spoken); err != nil {
		slog.Warn("approval reply not posted", "error", err.Error())
	}
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
