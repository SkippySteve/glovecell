# Voice control and voice approval (decision D15)

The operator no longer works hands-on at a keyboard. They want to:
1. **Send a voice recording** to whichever agent or provider he's using (Claude, Codex; orchestrators Orca, T3, Herdr
   or others). The agent listens (transcribes), does the work, and **sends back a voice recording**, a spoken summary
   he can play later. It's asynchronous by default.
2. **Optionally join a live conversation** with an agent (phase 2).
3. **Receive voice messages that agents start:** questions, blocked reports, approval requests.
4. **Approve PRs by voice.** His voice approval replaces clicking Merge in Forgejo, for every PR including risky
   one-way doors. After approval, a trusted gate merges, and agents carry on, fixing anything that breaks through new
   PRs. Everything still goes through PRs.

## Architecture (defaults chosen; each is pluggable)
- **Messaging: Matrix.** Self-hosted **Synapse** with federation off, PostgreSQL through CloudNativePG (deployment-specific), and the **Element X** app on the operator's phone.
  - Why: native voice messages with push notifications on iOS and Android, end-to-end encryption, threads, open
    source and self-hosted, bot SDKs, and Element Call (MatrixRTC on LiveKit) for phase-2 live calls.
  - Rooms: one **Inbox** room (general), one room per project or cell, and one **Approvals** room. Each task is a
    thread.
- **Speech: speaches** (formerly faster-whisper-server), OpenAI-compatible `/v1/audio/transcriptions` and
  `/v1/audio/speech`, self-hosted on CPU.
  - STT is faster-whisper; start with `Systran/faster-distil-whisper-small.en` and make the model configurable.
  - TTS is Kokoro (`hexgrad/Kokoro-82M`), with Piper as the fallback.
  - The service only depends on the OpenAI-compatible API, so a cloud provider (OpenAI, Groq and others) can be
    configured instead. The default is self-hosted, so there's zero added cost.
- **Voice service `glovecell-voice`:** Go, in this repository as `cmd/glovecell-voice` plus
  `internal/voice`. It runs in the trusted system namespace, never in a sandbox.
  - A Matrix bot (`maunium.net/go/mautrix`, with E2EE through its crypto helper) receives voice and text messages.
  - It transcribes, routes the message to an **agent target**, tracks the task, and sends back the result as a voice
    note plus a text transcript, in the same thread.
  - It exposes a small authenticated HTTP API, **`POST /v1/notify`**, that agents use to send the operator a message
    (text that becomes a voice note): questions, blocked reports, "done". Each cell or orchestrator gets its own
    token, and notify can never approve anything.
  - It stores threads, tasks and approvals in PostgreSQL, using the store from the PostgreSQL store (D13) where it fits.
- **Agent targets (provider-neutral):** a small interface, `Send(task) → id`, `Status(id)`, `Result(id)`, with
  adapters for:
  1. **Orca**: the existing orca-pilot handoff intake (`:8791`, controller bearer) plus run records (`:8790`). This
     is what runs today.
  2. **Archon factory**: start `archon-ship`/`archon-triage`-style workflows for "work on issue N" style requests.
  3. **Claude Code / Codex headless in a cell** (`claude -p`, `codex exec`): later, through the D14 cell-tools
     channel.
  4. **T3, Herdr and others**: through the OrchestratorClass adapters (D12), later.

  The room or thread decides the target, and the operator can say "use Codex" or "send this to Orca" to override it. A
  default target is set per room.
- **Approval gate (inside `glovecell-voice`, package `internal/approvalgate`):**
  - **Approval card.** When a PR is ready, the gate posts to the Approvals room: a voice summary plus a text card.
    The card has the repository, PR number and title, what changes, risk, one-way or two-way door, rollback, hand
    steps, the CI state, and a **4-digit approval code** bound to (repository, PR, head SHA). Agents can ask for a
    card through the notify API with `kind=approval`. The gate itself reads the PR from Forgejo and writes the
    summary from the PR body and diff stats, so an agent can't put misleading text on the card unchallenged; the
    card always shows the PR's real title, files changed and checks.
  - **the operator replies** by voice or text: "approve 4721", "approve PR 459 code 4721", "reject 4721 because …",
    "hold", "tell me more about 4721", or "approve all on today's list code 8830" (a list code bound to that exact
    set of PRs and SHAs).
  - **The gate verifies, and every check must pass:**
    1. The message comes from **the operator's Matrix user** (configured MXID), sent from a **cross-signed, verified
       device**, in an encrypted room. Bots, other users, edited messages and forwarded messages are refused.
    2. The transcript contains an approve intent and a code that matches an open card.
    3. The code isn't expired (24h by default) and hasn't been used; codes are single-use.
    4. The PR head SHA still equals the SHA on the card. If the PR changed, a new card goes out.
    5. CI is green, unless the operator explicitly says "approve 4721 despite failing checks".
    6. The PR is mergeable.
  - **Then the gate:**
    1. sets a Forgejo commit status `voice-approval` = success on the head SHA;
    2. merges with a merge commit as the **`approval-gate`** Forgejo bot;
    3. comments on the PR: "Approved by the operator by voice at <time>, code, transcript", with a link to the audio hash;
    4. replies with a voice confirmation;
    5. writes an audit row (transcript, audio SHA-256, device ID, SHA, decision).
  - **Rejections and holds** are recorded and passed back to the authoring agent as a task.
  - **Reverts:** "revert PR 459" by voice makes an agent open a revert PR, which needs its own approval card. The gate
    never reverts directly.
  - **Forgejo setup (the operator's hand steps):**
    1. Create the `approval-gate` user and token, stored in OpenBao or a Secret in the trusted namespace, never in a
       sandbox.
    2. In branch protection on each repository, add `approval-gate` to the merge whitelist and require the
       `voice-approval` status check.
    3. Keep agents (orca-agent) unable to merge.
  - **Agents still never merge.** The gate is the only automated merger, and it acts only on the operator's verified
    approval. That keeps design 12.1's rule ("agents never merge") true in spirit: the human gate is now spoken.
- **Live conversation (phase 2):** Element Call on a self-hosted LiveKit SFU, with a LiveKit Agents worker that joins
  as the agent. It uses the same STT and TTS and the same agent targets. It is optional, and is not in the first PRs.

## Security notes, which must be in the docs
- Voice alone is not identity: cloned voices exist, and transcription makes mistakes. Identity comes from the
  verified Matrix device. The spoken code ties the decision to what the operator was told. Optional speaker verification
  (e.g. SpeechBrain ECAPA) can be added later as a risk signal, never as the only factor.
- Agent output and PR text are untrusted. Only messages from the operator's account count as instructions or approvals.
  Bots run as separate Matrix users.
- the operator's Matrix credentials and the gate's Forgejo token never enter a sandbox.
- Rate limits: at most N merges per hour (default 10), configurable, plus an emergency "pause all merges" voice
  command.

Network placement: voice is the operator's channel into the system. It is not a
path into any cell: tasks reach cells only through orchestrator adapters and
the cell gateway (D14). Live calls (phase 2) use MatrixRTC on LiveKit behind
the edge.

