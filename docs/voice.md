# glovecell-voice: voice tasks, voice replies and agent notifications

glovecell-voice lets Steve work with agents by voice through Matrix
(decision D15). He sends a voice note in Element X, an agent does the work,
and the service reads the result back as a voice note in the same thread. Agents
can also send him messages, questions and approval requests. The voice
approval gate that merges pull requests on his spoken approval is described
in [approval-gate.md](approval-gate.md).

The service is `cmd/glovecell-voice` with `internal/voice/*` and
`internal/approvalgate`. It runs in the trusted control namespace, next to
the controller and never in a sandbox, as a single replica.

## What happens to a voice note

1. Steve records a voice note in a task room (Inbox, or a room per project).
2. The bot decrypts it. Messages from anyone except the configured Steve
   account are dropped, and so are the bot's own messages and other bots.
   Edits, forwards and unencrypted messages are ignored.
3. The audio goes unchanged to the speech server's
   `/v1/audio/transcriptions` endpoint. Element X records Ogg/Opus, which
   speaches decodes directly with PyAV, so the service does no conversion.
4. The transcript is posted back as text in the thread (`Heard: …`), so
   Steve can see what the service understood.
5. The service picks a target: a spoken override first, then the thread's
   target, then the room's default. Next it creates a task in PostgreSQL and
   sends the task to that target.
6. The task is polled. When it finishes, the target's own result text is
   posted in the thread, and a voice note of it follows (`/v1/audio/speech`,
   `response_format=opus`).
7. Failures come back the same way, as text plus a voice note: transcription,
   an unreachable target or a failed task.

Replies always go into the thread of the message that started them. Voice
notes are Ogg/Opus with `org.matrix.msc3245.voice` and
`org.matrix.msc1767.audio` (duration and waveform), so Element X shows them
as voice messages. The waveform is estimated from Opus packet sizes, which
keeps the binary pure Go.

Speech goes only through the OpenAI-compatible API. The default is a
self-hosted [speaches](https://github.com/speaches-ai/speaches) instance,
with `Systran/faster-distil-whisper-small.en` for speech to text and Kokoro
(`speaches-ai/Kokoro-82M-v1.0-ONNX`, voice `af_heart`) for text to speech.
To use OpenAI, Groq or another compatible provider, set `speech.baseURL`,
the models and the `speech-api-key` Secret key.

### Targets and spoken overrides

A target is anything with `Send(task) → id`, `Status(id)` and `Result(id)`
(`internal/voice/targets`).

| Kind | What it does |
|---|---|
| `orca` | Creates an orca-pilot handoff (`POST /handoffs` on the intake, port 8791, with the controller bearer token). The handoff clones the room's repository, creates a worktree and starts one agent: `claude`, or `codex` when the target sets `agent: codex`. Each task gets a stable handoff name (`voice-<target>-<task id>`), so a retry after a restart finds the existing handoff instead of starting a second agent. |
| `archon` | Stub. Archon is CLI-only today: there is no HTTP API in the cluster. Requests are refused with "ask Orca to run the workflow instead". |

How an Orca task finishes:

- **Completion is not reported by the handoff.** The intake only reports
  `preparing`, `started` or `failed`, and `started` just means the agent was
  launched.
- **From a run record.** If an Orca run record (`GET /runs` on the publisher,
  port 8790) has a task on the handoff's branch with a final attempt
  outcome, that outcome and its summary are the result.
- **From the agent.** The agent can report its own outcome through notify:
  `kind=message` with `task=<id>` and `final=true`.
- **Neither yet:** the task stays "running" and Steve can ask the agent
  directly.

The configuration names each target and gives it spoken aliases, for
example `codex` with `codecs` and `code x`, which are common transcription
mistakes. Overrides are matched only at the start or end of a request, and
only for known names:

- "Send this to Codex: fix the flaky test"
- "Use Orca. Bump the toolchain"
- "Ask Codex to tidy the README"
- "…, send this to Orca."
- "use codex" on its own switches the thread.

"Use the cache" or "send this to the printer" never redirect anything,
because those names are not targets.

## The notify API

```
POST /v1/notify
Authorization: Bearer <caller token>
Content-Type: application/json

{"kind": "message",  "text": "Build is green on PR 95."}
{"kind": "question", "text": "Should the chart default to one replica?"}
{"kind": "message",  "task": 12, "final": true, "text": "Bumped Go in PR 95."}
{"kind": "message",  "task": 12, "final": true, "failed": true, "text": "Tests fail on arm64."}
{"kind": "approval", "repo": "steve/agent-project-controller", "pr": 95, "text": "Ready."}
```

| Kind | Effect |
|---|---|
| `message`, `question` | Posted as text plus a voice note, labelled "Message from <caller> (agent)". They go to the caller's room, or to the Inbox if the caller has no room. They can never go to the Approvals room. Steve's replies in that thread go to the caller's target. |
| `message` with `task` | An update on that task. With `final: true` it finishes the task and its result is read out. A caller may only report on tasks sent to its own target. |
| `approval` | Asks the gate to post an approval card. The gate reads the PR from Forgejo itself. The response is `202 {"status": "card posted; Steve decides"}`, with no code and no way to approve. |

Responses: `202` delivered, `400` invalid body (unknown fields are refused),
`401` unknown token, `403` kind not allowed for the caller or repository not
covered by the gate, `404` unknown task, `413` text over 4000 characters,
`429` rate limited (per caller: `perHour`, default 60; approvals 20 an hour),
`503` not delivered.

Callers are listed in the `notify-callers.json` key of the Secret, with
token hashes only:

```json
[
  {"name": "orca", "tokenSHA256": "<sha256 hex>", "kinds": ["message", "question", "approval"], "target": "orca"},
  {"name": "cell-apc", "tokenSHA256": "<sha256 hex>", "room": "!apc:matrix.example.com"}
]
```

Create a token with `openssl rand -hex 32`, and get its hash with
`glovecell-voice hash-token` (reads the token on stdin) or `sha256sum`. The
plain token goes only into the caller's own Secret. A caller without `kinds`
may send `message` and `question`.

## Configuration

Non-secret settings are one JSON file. Helm renders it from
`voice.config` into a ConfigMap. See
[examples/glovecell-voice-config.json](examples/glovecell-voice-config.json).

| Field | Meaning |
|---|---|
| `steve` | Steve's MXID. Only his messages are handled. |
| `matrix.homeserver`, `matrix.userID` | The bot account, a separate Matrix user. |
| `matrix.deviceID` | Required with an access token. With a password, the first login creates the device and the crypto store remembers it. |
| `matrix.pinnedMasterKey` | Steve's cross-signing master key. See [Device trust](#device-trust). |
| `matrix.maxEventAge` | Older events are ignored, for example history on the first sync. Default `1h`. |
| `rooms[]` | `id` (a room ID, `!…`), `name`, `target`, `repo`, `inbox: true` on one room and `approvals: true` on one room. |
| `speech` | `baseURL`, `sttModel`, `ttsModel`, `voice`, `language`, `speed`. |
| `targets` | Name → `{kind: orca \| archon, handoffURL, runRecordsURL, agent, defaultRepo, base, briefFooter, aliases}`. |
| `gate` | See [approval-gate.md](approval-gate.md). |
| `summarizer` | Optional, and `enabled: false` by default. Set `baseURL` and `model` of an OpenAI-compatible chat endpoint to shorten results before they are spoken. Without it the target's own text is read out. |

Secrets are files from one existing Secret (`voice.existingSecret`). The
chart never creates it.

| Key | Needed for |
|---|---|
| `matrix-password` or `matrix-access-token` | The bot's sign-in. |
| `matrix-pickle-key` | Encrypts the Olm account and sessions in PostgreSQL. 32 or more random bytes (`openssl rand -hex 32`). If it is lost, so is the bot's device: log in as a new device and verify again. |
| `forgejo-token` | The `approval-gate` user's token, when the gate is on. |
| `orca-handoff-token` | The orca-pilot handoff intake token, for `orca` targets. It is the same value as `controller-secrets/handoff-token`. |
| `notify-callers.json` | Notify caller hashes. Without it, every notify request is refused. |
| `speech-api-key`, `summarizer-api-key` | Cloud providers only. |

The database is the controller's PostgreSQL by default
(`voice.database.mode: shared`). The voice tables are prefixed `voice_` and
have their own migration table (`glovecell_voice_schema_migrations`).
mautrix keeps its crypto and state tables (`crypto_*`, `mx_*`) in the same
database. Set `mode: external` with `existingSecret`, `uriKey` and `caKey` to
use a separate database. TLS is always `sslmode=verify-full`.

The image is the Dockerfile's `voice` target. It is built with
`-tags goolm` (mautrix's pure-Go Olm, with `CGO_ENABLED=0`). A binary built
without the tag refuses to start.

## Operations

- `GET /healthz` reports that the process is up. `GET /readyz` reports that
  the schema is migrated, the database answers and Matrix has synced.
- `GET /metrics` exposes:
  - `glovecell_voice_notify_requests_total{caller,kind,outcome}`
  - `glovecell_voice_gate_decisions_total{kind,outcome}`
  - `glovecell_voice_tasks_finished_total{target,state}`
  - `glovecell_voice_ready`
- **Restarts:**
  - A handled event is recorded before it is acted on, so it is never
    handled twice.
  - A task that was created but not sent is sent again. Its handoff name is
    stable, so the second send finds the first handoff.
  - Results that were not yet delivered are posted on the next poll.
  - The approval gate resolves interrupted merges. See
    [approval-gate.md](approval-gate.md#restarts).
- **While the database is down,** nothing is acted on. Steve gets "I can't
  reach my database", and the gate refuses every approval.

## Security model

- **Identity comes from Matrix, not from the voice.** Cloned voices exist and
  transcription makes mistakes. Only messages from the configured Steve
  account count as instructions. Bots run as separate Matrix users and are
  ignored, as is everyone else. For approvals, the message must also come
  from a cross-signed, verified device in an encrypted room. Optional speaker
  verification (for example SpeechBrain ECAPA) could later add a risk signal,
  never the only factor.
- **Agent output and PR text are untrusted.**
  - Results and notifications are read to Steve, and are labelled with the
    agent's name.
  - They are never executed or treated as approvals.
  - The optional summarizer prompt treats the result as data.
  - Notify cannot post to the Approvals room. An approval request makes the
    gate read the PR itself.
- **Credentials stay out of sandboxes.** The service runs in the control
  namespace with no Kubernetes token. Steve's own Matrix credentials are
  never used, because the bot is its own account. The bot's credentials and
  the gate's Forgejo token are Secret files in the control namespace only.
  Errors and logs never include tokens.
- **Network.** The chart's NetworkPolicy allows ingress on 8080 only from the
  listed notify callers and metrics scrapers. Egress is limited to DNS, the
  database and the destinations listed in values.
- **Requests sent to agents are marked as transcribed speech**, which may
  contain recognition errors. Agents are told to work through a PR and never
  merge.

### Device trust

Each decrypted event carries mautrix's trust state for the sending device.
A device counts as verified when:

- it is **cross-signed and verified**: signed by Steve's self-signing key,
  under a master key the bot has verified with its own user-signing key; or
- `matrix.pinnedMasterKey` is set, the device is cross-signed, and Steve's
  current master key equals the pinned one. This avoids interactive
  verification of the bot. A reset or replaced identity is never trusted.

Unverified, unknown and uncross-signed devices are never trusted. Keys
restored from backup or forwarded are not trusted either, and neither are
unencrypted messages.

To pin the key, read Steve's master key in Element (Settings → Encryption,
the cross-signing "Master key" entry, or Element Web's Settings → Security &
Privacy → Cryptography). Put it in `matrix.pinnedMasterKey` and restart. If
Steve ever resets his cross-signing identity, the gate refuses everything
until the pin is updated.

## Steve's hand steps

1. **Matrix.** Deploy Synapse (federation off, PostgreSQL through
   CloudNativePG) and install Element X on the phone. Then:
   - Create the bot user, for example `@glovecell:<server>`, with a strong
     password.
   - Create the rooms (Inbox, one per project, Approvals). Turn on encryption
     and invite the bot. It joins invitations from Steve to configured rooms.
   - Cross-sign Steve's phone (Element X does this at sign-in), then pin his
     master key as described above.
2. **Speech.** Deploy speaches with the STT and TTS models above, or choose a
   cloud provider.
3. **Secret.** Create the `glovecell-voice` Secret in the control namespace
   with the keys above.
4. **Orca.** Allow the voice pod to reach the orca-pilot intake (8791) and
   run records (8790). The `orca-pilot-handoff` and `orca-pilot-run-records`
   CiliumNetworkPolicies admit only `app.kubernetes.io/name:
   agent-project-controller` in `agent-project-controller`, so they need
   `app.kubernetes.io/name: glovecell-voice` added. These policies live in
   `infra/agent-workplace/orca-pilot` (`render.go`) in the cluster
   repository.
5. **Helm values.** Set:
   - `voice.enabled: true`, `voice.image` (by digest), `voice.existingSecret`
     and `voice.config`;
   - `voice.networkPolicy.egress` for the homeserver, speaches, Forgejo and
     Orca;
   - `voice.networkPolicy.notifyFrom` for the callers.
6. **Notify callers.** Give each caller a token and add its hash to
   `notify-callers.json`.

## Not in this version

- Live conversation (Element Call with LiveKit) is phase 2.
- Archon targets are a stub until Archon has an API. Until then, Orca can run
  Archon workflows on request.
- Claude Code and Codex headless in a cell (D14 cell-tools channel), and T3
  and Herdr targets (OrchestratorClass adapters) come later.
- Room aliases are not resolved: configure room IDs.
- The waveform is approximate (from packet sizes, not decoded audio).
