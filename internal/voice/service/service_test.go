package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/approvalgate"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/voice/matrix"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/voice/speech"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/voice/store"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/voice/targets"
)

const (
	steve     = "@steve:example.com"
	inbox     = "!inbox:example.com"
	apcRoom   = "!apc:example.com"
	approvals = "!approvals:example.com"
	repo      = "steve/agent-project-controller"
)

var ctx = context.Background()

type rig struct {
	svc    *Service
	store  *store.Memory
	speech *speech.Fake
	mx     *matrix.Fake
	orca   *targets.Fake
	codex  *targets.Fake
	forge  *approvalgate.FakeForge
	gate   *approvalgate.Gate
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{store: store.NewMemory(), speech: &speech.Fake{}, mx: &matrix.Fake{}, orca: &targets.Fake{TargetName: "orca"}, codex: &targets.Fake{TargetName: "codex"}, forge: approvalgate.NewFakeForge()}
	reg := targets.NewRegistry()
	reg.Add(r.orca, "orka")
	reg.Add(r.codex, "codecs")
	reg.Add(targets.Archon{})
	svc, err := New(Config{Steve: steve, RequireEncryption: true, Rooms: []Room{
		{ID: inbox, Name: "Inbox", Target: "orca", Repo: repo, Inbox: true},
		{ID: apcRoom, Name: "APC", Target: "codex", Repo: repo},
		{ID: approvals, Name: "Approvals", Target: "orca", Repo: repo, Approvals: true},
	}}, r.store, r.speech, r.speech, r.mx, reg, nil)
	if err != nil {
		t.Fatal(err)
	}
	g, err := approvalgate.New(approvalgate.Config{Steve: steve, Room: approvals, Repos: []string{"steve/*"}}, r.store, r.forge, svc, svc.Feedback)
	if err != nil {
		t.Fatal(err)
	}
	svc.SetGate(g)
	r.svc, r.gate = svc, g
	return r
}

var n int

func (r *rig) voice(room, text string) matrix.Message {
	n++
	audio := r.speech.Voice(text)
	return matrix.Message{RoomID: room, EventID: fmt.Sprintf("$v%d", n), ThreadRoot: fmt.Sprintf("$v%d", n), Sender: steve, DeviceID: "PHONE", Kind: matrix.KindVoice,
		Audio: audio, AudioMIME: "audio/ogg", AudioSHA256: matrix.AudioHash(audio), Encrypted: true, DeviceVerified: true}
}

func (r *rig) text(room, text string) matrix.Message {
	n++
	return matrix.Message{RoomID: room, EventID: fmt.Sprintf("$t%d", n), ThreadRoot: fmt.Sprintf("$t%d", n), Sender: steve, DeviceID: "PHONE", Kind: matrix.KindText,
		Body: text, Encrypted: true, DeviceVerified: true}
}

func contains(xs []string, sub string) bool {
	for _, x := range xs {
		if strings.Contains(x, sub) {
			return true
		}
	}
	return false
}

func TestVoiceTaskEndToEnd(t *testing.T) {
	r := newRig(t)
	msg := r.voice(inbox, "Bump the Go toolchain to 1.27.4 in the controller.")
	r.svc.HandleMessage(ctx, msg)
	texts := r.mx.Texts()
	if !contains(texts, "Heard: Bump the Go toolchain") || !contains(texts, "Sent to orca as task 1") {
		t.Fatalf("texts %q", texts)
	}
	sent := r.orca.Tasks()
	if len(sent) != 1 || sent[0].Brief != "Bump the Go toolchain to 1.27.4 in the controller." || sent[0].Repo != repo || sent[0].Actor != steve {
		t.Fatalf("sent %+v", sent)
	}
	for _, p := range r.mx.Sent {
		if p.ThreadRoot != msg.ThreadRoot {
			t.Fatalf("reply outside the thread: %+v", p)
		}
	}
	r.svc.Poll(ctx)
	if r.mx.Voices() != 0 {
		t.Fatal("voice before the result")
	}
	r.orca.Finish("orca-1", targets.StateSucceeded, "Bumped Go to 1.27.4 in PR 95. See https://git.example.com/pr/95 for details.")
	r.svc.Poll(ctx)
	if !contains(r.mx.Texts(), "Task 1 (orca) is done:\nBumped Go") || r.mx.Voices() != 1 {
		t.Fatalf("result not delivered: %q voices %d", r.mx.Texts(), r.mx.Voices())
	}
	spoken := r.speech.Spoken[len(r.speech.Spoken)-1]
	if strings.Contains(spoken, "https://") || !strings.Contains(spoken, "Bumped Go") {
		t.Fatalf("spoken %q", spoken)
	}
	v := r.mx.Sent[len(r.mx.Sent)-1]
	if v.Voice == nil || v.Voice.MIMEType != "audio/ogg" || len(v.Voice.Waveform) != speech.WaveformBins || v.ThreadRoot != msg.ThreadRoot {
		t.Fatalf("voice note %+v", v)
	}
	// Delivered once.
	before := len(r.mx.Sent)
	r.svc.Poll(ctx)
	if len(r.mx.Sent) != before {
		t.Fatal("result delivered twice")
	}
	task, _ := r.store.GetTask(ctx, 1)
	if task.State != store.TaskSucceeded || !task.Delivered || task.AudioSHA256 != msg.AudioSHA256 || task.Origin != "voice" {
		t.Fatalf("task %+v", task)
	}
}

func TestOverridesAndThreadTargets(t *testing.T) {
	r := newRig(t)
	r.svc.HandleMessage(ctx, r.voice(inbox, "Send this to codecs: fix the flaky login test"))
	if len(r.codex.Tasks()) != 1 || r.codex.Tasks()[0].Brief != "Fix the flaky login test" || len(r.orca.Tasks()) != 0 {
		t.Fatalf("override: codex %v orca %v", r.codex.Tasks(), r.orca.Tasks())
	}
	// The APC room defaults to codex; a thread switched to Orca stays there.
	m := r.text(apcRoom, "use orca")
	r.svc.HandleMessage(ctx, m)
	if !contains(r.mx.Texts(), "this thread now goes to orca") {
		t.Fatal("thread switch not confirmed")
	}
	follow := r.text(apcRoom, "tidy the README")
	follow.ThreadRoot, follow.InThread = m.ThreadRoot, true
	r.svc.HandleMessage(ctx, follow)
	if len(r.orca.Tasks()) != 1 || r.orca.Tasks()[0].Brief != "tidy the README" {
		t.Fatalf("thread target not used: %v", r.orca.Tasks())
	}
	r.svc.HandleMessage(ctx, r.text(apcRoom, "Use the cache for builds"))
	if len(r.codex.Tasks()) != 2 {
		t.Fatal("unknown name treated as an override")
	}
}

func TestErrorsComeBackAsVoice(t *testing.T) {
	r := newRig(t)
	r.speech.STTErr = errors.New("speech server returned HTTP 503")
	r.svc.HandleMessage(ctx, r.voice(inbox, "anything"))
	if !contains(r.mx.Texts(), "couldn't transcribe") || r.mx.Voices() != 1 {
		t.Fatalf("transcription error: %q voices %d", r.mx.Texts(), r.mx.Voices())
	}
	r.speech.STTErr = nil
	r.orca.SendErr = errors.New("orca unreachable")
	r.svc.HandleMessage(ctx, r.voice(inbox, "do the thing"))
	if !contains(r.mx.Texts(), "failed: orca didn't take the task: orca unreachable") || r.mx.Voices() != 2 {
		t.Fatalf("send error: %q voices %d", r.mx.Texts(), r.mx.Voices())
	}
	r.orca.SendErr = nil
	r.svc.HandleMessage(ctx, r.voice(inbox, "send this to archon: work on issue 12"))
	if !contains(r.mx.Texts(), "Archon has no API yet") {
		t.Fatalf("archon: %q", r.mx.Texts())
	}
	// Speech down: the text still arrives.
	r.speech.TTSErr = errors.New("tts down")
	r.orca.SendErr = errors.New("again")
	r.svc.HandleMessage(ctx, r.text(inbox, "x"))
	if !contains(r.mx.Texts(), "failed: orca didn't take the task: again") {
		t.Fatal("text lost when speech failed")
	}
}

func TestIgnoredMessages(t *testing.T) {
	r := newRig(t)
	for name, m := range map[string]matrix.Message{
		"other user":  func() matrix.Message { m := r.text(inbox, "x"); m.Sender = "@mallory:example.com"; return m }(),
		"edited":      func() matrix.Message { m := r.text(inbox, "x"); m.Edited = true; return m }(),
		"forwarded":   func() matrix.Message { m := r.text(inbox, "x"); m.Forwarded = true; return m }(),
		"unencrypted": func() matrix.Message { m := r.text(inbox, "x"); m.Encrypted = false; return m }(),
		"other room":  func() matrix.Message { m := r.text("!other:example.com", "x"); return m }(),
	} {
		r.svc.HandleMessage(ctx, m)
		if len(r.orca.Tasks()) != 0 {
			t.Fatalf("%s started a task", name)
		}
	}
	// The same event twice is handled once.
	m := r.text(inbox, "once")
	r.svc.HandleMessage(ctx, m)
	r.svc.HandleMessage(ctx, m)
	if len(r.orca.Tasks()) != 1 {
		t.Fatalf("duplicate event: %d tasks", len(r.orca.Tasks()))
	}
}

func TestDatabaseDownRefusesWork(t *testing.T) {
	r := newRig(t)
	r.store.Down = true
	r.svc.HandleMessage(ctx, r.text(inbox, "do it"))
	if len(r.orca.Tasks()) != 0 || !contains(r.mx.Texts(), "can't reach my database") {
		t.Fatalf("worked without a database: %q", r.mx.Texts())
	}
}

func TestCrashBeforeSendResends(t *testing.T) {
	r := newRig(t)
	now := time.Now()
	r.svc.Now = func() time.Time { return now }
	task, _ := r.store.CreateTask(ctx, store.Task{RoomID: inbox, ThreadRoot: "$x", Origin: "voice", Target: "orca", State: store.TaskReceived, Transcript: "left over", CreatedAt: now.Add(-2 * time.Minute)})
	r.svc.Poll(ctx)
	if len(r.orca.Tasks()) != 1 || r.orca.Tasks()[0].ID != task.ID {
		t.Fatalf("not resent: %v", r.orca.Tasks())
	}
}

func TestVoiceApprovalThroughTheService(t *testing.T) {
	r := newRig(t)
	r.forge.AddPR(repo, 92, strings.Repeat("a", 40), "Postgres store")
	if err := r.svc.RequestCard(ctx, Caller{Name: "orca", Targets: []string{"orca"}}, repo, 92, "ready"); err != nil {
		t.Fatal(err)
	}
	c, _ := r.store.OpenCardFor(ctx, repo, 92)
	if r.mx.Voices() != 1 || !contains(r.mx.Texts(), "Approval card, code "+c.Code) {
		t.Fatalf("card not posted with voice: %q", r.mx.Texts())
	}
	// An agent posting the code through notify is a bot message: it never
	// reaches the gate as Steve, and notify cannot post to Approvals.
	if _, err := r.svc.NotifyMessage(ctx, Caller{Name: "orca", Room: approvals}, Note{Kind: "message", Text: "approve " + c.Code}); err == nil {
		t.Fatal("notify posted into the Approvals room")
	}
	if _, err := r.svc.NotifyMessage(ctx, Caller{Name: "orca"}, Note{Kind: "message", Text: "approve " + c.Code, Room: "Approvals"}); err == nil {
		t.Fatal("notify posted into the Approvals room")
	}
	if r.forge.MergedCount() != 0 {
		t.Fatal("merged by notify")
	}
	msg := r.voice(approvals, "Approve pull request 92, code "+approvalgate.SpokenCode(c.Code))
	r.svc.HandleMessage(ctx, msg)
	if r.forge.MergedCount() != 1 || !contains(r.mx.Texts(), "Heard: Approve pull request 92") || !contains(r.mx.Texts(), "Merged "+repo+" PR #92") {
		t.Fatalf("not merged: %q", r.mx.Texts())
	}
	audit, _ := r.store.ListAudit(ctx, 1)
	if audit[0].Decision != store.DecisionMerged || audit[0].AudioSHA256 != msg.AudioSHA256 {
		t.Fatalf("audit %+v", audit[0])
	}
}

func TestRejectionGoesBackToAnAgent(t *testing.T) {
	r := newRig(t)
	r.forge.AddPR(repo, 93, strings.Repeat("b", 40), "Chart")
	if err := r.svc.RequestCard(ctx, Caller{Name: "orca"}, repo, 93, ""); err != nil {
		t.Fatal(err)
	}
	c, _ := r.store.OpenCardFor(ctx, repo, 93)
	r.svc.HandleMessage(ctx, r.voice(approvals, "Reject "+c.Code+" because the NetworkPolicy is too wide"))
	sent := r.orca.Tasks()
	if len(sent) != 1 || !strings.Contains(sent[0].Brief, "the networkpolicy is too wide") || sent[0].Repo != repo {
		t.Fatalf("feedback task %+v", sent)
	}
}

func TestNotifyMessagesAndTaskReports(t *testing.T) {
	r := newRig(t)
	orca := Caller{Name: "orca-pilot", Targets: []string{"orca", "codex"}}
	id, err := r.svc.NotifyMessage(ctx, orca, Note{Kind: "question", Text: "Should the chart default to one replica?"})
	if err != nil || !contains(r.mx.Texts(), "Question from orca-pilot (agent):") || r.mx.Voices() != 1 {
		t.Fatalf("question %v %q", err, r.mx.Texts())
	}
	// Steve's reply in that thread goes to the caller's first target.
	reply := r.voice(inbox, "Yes, one replica")
	reply.ThreadRoot, reply.InThread = id, true
	r.svc.HandleMessage(ctx, reply)
	if len(r.orca.Tasks()) != 1 || r.orca.Tasks()[0].Brief != "Yes, one replica" {
		t.Fatalf("reply not routed: %v", r.orca.Tasks())
	}
	// A caller that does not speak for the target cannot finish the task.
	if _, err := r.svc.NotifyMessage(ctx, Caller{Name: "cell"}, Note{Kind: "done", Text: "done", Task: 1}); !errors.Is(err, ErrUnknownTask) {
		t.Fatalf("another caller finished the task: %v", err)
	}
	// In the thread, without a target match, it is only an update.
	if _, err := r.svc.NotifyMessage(ctx, Caller{Name: "cell"}, Note{Kind: "done", Text: "I think it's done", Thread: id}); err != nil {
		t.Fatal(err)
	}
	if task, _ := r.store.GetTask(ctx, 1); task.State != store.TaskRunning {
		t.Fatalf("stranger finished the task: %+v", task)
	}
	// orca-pilot voice send --kind done --thread <Steve's event>.
	if _, err := r.svc.NotifyMessage(ctx, orca, Note{Kind: "done", Text: "Set replicas to 1 in PR 96.", Thread: id}); err != nil {
		t.Fatal(err)
	}
	task, _ := r.store.GetTask(ctx, 1)
	if task.State != store.TaskSucceeded || !task.Delivered || !contains(r.mx.Texts(), "Set replicas to 1 in PR 96.") {
		t.Fatalf("task %+v", task)
	}
	if _, err := r.svc.NotifyMessage(ctx, orca, Note{Kind: "done", Text: "again", Task: 1}); err == nil {
		t.Fatal("finished task finished again")
	}
	// Blocked reports by name of room; unknown kinds and rooms refused.
	if _, err := r.svc.NotifyMessage(ctx, orca, Note{Kind: "blocked", Text: "Need a package approved", Room: "apc"}); err != nil || !contains(r.mx.Texts(), "Blocked from orca-pilot") {
		t.Fatalf("blocked %v", err)
	}
	for _, n := range []Note{{Kind: "approve", Text: "x"}, {Kind: "message", Text: "x", Room: "!nowhere:example.com"}, {Kind: "message"}} {
		if _, err := r.svc.NotifyMessage(ctx, orca, n); !errors.Is(err, ErrBadRequest) {
			t.Errorf("%+v: %v", n, err)
		}
	}
}

func TestTaskVoiceRetriesWithoutRepeatingText(t *testing.T) {
	r := newRig(t)
	msg := r.voice(inbox, "Check the build")
	r.svc.HandleMessage(ctx, msg)
	r.orca.Finish("orca-1", targets.StateSucceeded, "The build passed.")
	r.speech.TTSErr = errors.New("speech temporarily unavailable")
	r.svc.Poll(ctx)
	task, _ := r.store.GetTask(ctx, 1)
	if task.Delivered || task.ResultEventID == "" || !contains(r.mx.Texts(), "The build passed") {
		t.Fatalf("speech failure lost the pending voice: %+v", task)
	}
	before := len(r.mx.Texts())
	// A fresh service using the same durable state retries the voice only.
	r.speech.TTSErr = nil
	restarted, err := New(r.svc.cfg, r.store, r.speech, r.speech, r.mx, r.svc.targets, nil)
	if err != nil {
		t.Fatal(err)
	}
	restarted.Poll(ctx)
	task, _ = r.store.GetTask(ctx, 1)
	if !task.Delivered || r.mx.Voices() != 1 || len(r.mx.Texts()) != before {
		t.Fatalf("restart did not deliver voice once: %+v", task)
	}
	restarted.Poll(ctx)
	if r.mx.Voices() != 1 {
		t.Fatal("voice delivered twice")
	}
}

func TestExactTaskCompletionWithinSharedThread(t *testing.T) {
	r := newRig(t)
	first := r.text(inbox, "First task")
	r.svc.HandleMessage(ctx, first)
	second := r.text(inbox, "Second task")
	second.ThreadRoot = first.ThreadRoot
	r.svc.HandleMessage(ctx, second)
	caller := Caller{Name: "orca-pilot", Targets: []string{"orca"}}
	if _, err := r.svc.NotifyMessage(ctx, caller, Note{Kind: "done", Task: 1, Thread: first.ThreadRoot, Text: "First finished"}); err != nil {
		t.Fatal(err)
	}
	one, _ := r.store.GetTask(ctx, 1)
	two, _ := r.store.GetTask(ctx, 2)
	if one.State != store.TaskSucceeded || two.State != store.TaskRunning {
		t.Fatalf("completion applied to wrong task: %s %s", one.State, two.State)
	}
	if _, err := r.svc.NotifyMessage(ctx, caller, Note{Kind: "done", Task: 1, Text: "duplicate"}); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("duplicate completion accepted: %v", err)
	}
}

func TestAsyncModeCannotRequestOrActOnApproval(t *testing.T) {
	r := newRig(t)
	r.svc.SetGate(nil)
	if err := r.svc.RequestCard(ctx, Caller{Name: "orca-pilot"}, repo, 93, "ready"); err == nil {
		t.Fatal("async-only service accepted an approval card request")
	}
	r.svc.HandleMessage(ctx, r.voice(approvals, "Approve 4721"))
	if r.forge.MergedCount() != 0 || len(r.orca.Tasks()) != 0 {
		t.Fatal("approval in disabled room merged or launched an agent")
	}
}
