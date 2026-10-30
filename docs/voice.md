# glovecell-voice: voice tasks, voice replies and agent notifications

glovecell-voice lets Steve work with agents by voice through Matrix
(decision D15). He sends a voice note in Element X, an agent does the work,
and the service reads the result back as a voice note in the same thread. Agents
can also send him messages, questions and approval requests. The voice
approval gate that merges pull requests on his spoken approval is described
in [approval-gate.md](approval-gate.md).

The service is `cmd/glovecell-voice` with `internal/voice/*` and
`internal/approvalgate`. It runs in a trusted namespace and never in a
sandbox: the control namespace through the chart, or its own `glovecell-voice`
namespace in the cluster deployment. It runs as a single replica.

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
the models and `SPEECH_API_KEY_FILE`.

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
{"kind": "question", "text": "Should the chart default to one replica?", "room": "Inbox"}
{"kind": "blocked",  "text": "I need a package approved.", "thread": "$eventid"}
{"kind": "done",     "text": "Bumped Go in PR 95.", "thread": "$eventid"}
{"kind": "message",  "task": 12, "final": true, "failed": true, "text": "Tests fail on arm64."}
{"kind": "approval", "repo": "steve/agent-project-controller", "pr": 95, "text": "Ready."}
```

This is the body that `orca-pilot voice send --text … [--kind
message|question|blocked|done] [--room …] [--thread …]` posts.

| Kind | Effect |
|---|---|
| `message`, `question`, `blocked`, `done` | Posted as text plus a voice note, labelled "Question from <caller> (agent)" and so on. They go to `room` (a room ID or a configured room name), else the caller's room, else the Inbox. They can never go to the Approvals room. With `thread`, they go into that thread. Steve's replies to a new thread go to the caller's first target. |
| `done` (or `final: true`) in a task's thread, or with `task` | Finishes that task, as long as the caller speaks for its target (`NOTIFY_CALLER_TARGETS`, for example `orca-pilot=orca\|codex`). The text is read out as the result, and `failed: true` marks it failed. Without that mapping it is just an update in the thread. |
| `approval` | Asks the gate to post an approval card. The gate reads the PR from Forgejo itself. The response is `202 {"message": "approval card posted; Steve decides"}`, with no code and no way to approve. |

Successful notes return `202 {"id": "<event ID>", "message": "sent to Steve"}`.

| Status | Meaning |
|---|---|
| `400` | Invalid body. Unknown fields are refused. |
| `401` | Unknown token. |
| `403` | Kind not allowed for the caller, or repository not covered by the gate. |
| `404` | Unknown task. |
| `413` | Text over 4000 characters. |
| `429` | Rate limited, per caller: `perHour`, default 60, and 20 approvals an hour. |
| `503` | Not delivered. |

Callers come from either of two places:

- **`NOTIFY_TOKENS_DIR`**: one file per caller, named after the caller (for
  example `orca-pilot`), holding its plain token, as the
  `glovecell-voice-notify` Secret mounts them. The service keeps only the
  SHA-256 in memory. These callers may use every kind, including `approval`,
  which can only ask for a card.
- **`NOTIFY_CALLERS_FILE`**: a JSON list of
  `{"name", "tokenSHA256", "kinds", "room", "targets", "perHour"}`. Use it
  where only hashes should be stored. `glovecell-voice hash-token` prints the
  hash of a token read on stdin.

## Configuration

The service reads the environment and Secret layout of the cluster
deployment (`infra/agent-workplace/glovecell-voice` in the deployment
repository). The Helm chart (`voice.*` values) renders the same layout.
Every credential is a mounted file.

| Variable | Meaning |
|---|---|
| `VOICE_LISTEN_ADDR` | Notify API listener (`:8080`). |
| `VOICE_HEALTH_ADDR` | `/healthz`, `/readyz` and `/metrics` (`:8081`; the notify port when equal). |
| `MATRIX_HOMESERVER` | The homeserver's https URL. Plain http is accepted only for localhost. |
| `MATRIX_DEVICE_NAME` | The bot's device name. |
| `MATRIX_CREDENTIALS_DIR` | `user_id`, `access_token` and `device_id` (or `password`), plus `pickle_key`. |
| `VOICE_OWNER_DIR` | `mxid` (Steve), `master_key` (his pinned cross-signing master key, required) and `rooms`. |
| `SPEECH_BASE_URL`, `SPEECH_STT_MODEL`, `SPEECH_TTS_MODEL`, `SPEECH_VOICE`, `SPEECH_LANGUAGE`, `SPEECH_API_KEY_FILE` | Speech API. |
| `FORGEJO_URL`, `FORGEJO_TOKEN_FILE` | The gate's Forgejo API and the `approval-gate` token. HTTPS is required, except plain HTTP to an in-cluster Service (`*.svc`). The gate is on when the token exists and an Approvals room is configured. |
| `APPROVAL_REPOS` | `OWNER/*` or `OWNER/NAME`, comma-separated. Required for the gate. |
| `APPROVAL_CODE_TTL`, `APPROVAL_MAX_MERGES_PER_HOUR`, `APPROVAL_STATUS_CONTEXT` | `24h`, `10` and `voice-approval` by default. |
| `ORCA_HANDOFF_URL`, `ORCA_RUN_RECORDS_URL`, `ORCA_HANDOFF_TOKEN_FILE` | The Orca targets `orca` (claude) and `codex` (codex), and the Archon stub. |
| `ORCA_BRIEF_FOOTER` | Appended to every Orca brief. `{thread}` and `{task}` are filled in. The default tells the agent to report with `orca-pilot voice send --kind done --thread {thread}`. |
| `VOICE_DEFAULT_REPO` | `OWNER/NAME` for rooms without a repository. Orca handoffs need one. |
| `NOTIFY_TOKENS_DIR`, `NOTIFY_CALLER_TARGETS`, `NOTIFY_CALLERS_FILE` | Notify callers (see above). |
| `DATABASE_URL_FILE`, `DATABASE_CA_FILE`, `DATABASE_MAX_CONNS` | PostgreSQL, always with `sslmode=verify-full`. |
| `VOICE_CONFIG_FILE` | Optional JSON with details (below). |
| `VOICE_POLL_INTERVAL`, `VOICE_LOG_LEVEL` | Task polling (`30s`), and `debug` logging. |

The **owner `rooms`** key lists room IDs, separated by commas or newlines.
Without markers, the first room is the Inbox, the second is Approvals, and
any further rooms are task rooms. An entry can also carry words:

- `!id:server inbox`
- `!id:server approvals`
- `!id:server target=codex repo=steve/x name=APC`

Only the owner Secret decides who the bot obeys and in which rooms.

`VOICE_CONFIG_FILE` ([example](examples/glovecell-voice-config.json)) can add:

- per-room `target`, `repo` and `name` for the owner's rooms (never new
  rooms);
- targets with spoken aliases;
- `matrix.maxEventAge`;
- `speech.speed`;
- the optional summarizer (`enabled: false` by default; an
  OpenAI-compatible chat endpoint that shortens results before they are
  spoken);
- `pollInterval`.

The `matrix-pickle-key` (`pickle_key`) encrypts the Olm account and sessions
in PostgreSQL. If it is lost, so is the bot's device: sign the bot in as a
new device.

The database can be the controller's PostgreSQL (`voice.database.mode:
shared` in the chart) or its own: the cluster uses its own CloudNativePG
cluster, `glovecell-voice-db`. The voice tables are prefixed `voice_` and
have their own migration table (`glovecell_voice_schema_migrations`).
mautrix keeps its crypto and state tables (`crypto_*`, `mx_*`) in the same
database.

The image is the Dockerfile's `voice` target, built with `-tags goolm`
(mautrix's pure-Go Olm, with `CGO_ENABLED=0`). A binary built without the
tag refuses to start. The container argument may be `serve` or
`glovecell-voice`.

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
- **Credentials stay out of sandboxes.** The service runs in a trusted
  namespace with no Kubernetes token. Steve's own Matrix credentials are
  never used, because the bot is its own account. The bot's credentials and
  the gate's Forgejo token are Secret files in that namespace only. Errors
  and logs never include tokens.
- **Network.** Ingress on 8080 (notify only) is allowed from the listed
  callers. The cluster policy also restricts it to `POST /v1/notify` at L7.
  Probes and metrics use 8081. Egress is limited to DNS, the database and
  the listed destinations.
- **Logs carry transcripts.** Keep the service's pod logs out of
  agent-readable log stores; the cluster excludes them.
- **Requests sent to agents are marked as transcribed speech**, which may
  contain recognition errors. Agents are told to work through a PR and never
  merge.

### Device trust

Trust is **pinned**. A message counts as coming from a verified device only
when all of these hold:

- it arrived end-to-end encrypted, with keys sent directly by the device,
  not forwarded and not restored from backup;
- the sending device is signed by Steve's self-signing key;
- that key is signed by his master key;
- that master key is exactly the one in the owner Secret (`master_key`).

Interactive verification by the bot (or by anyone else) does not count on
its own. Unsigned devices and unknown devices are never trusted. If Steve
resets his identity, his master key changes and nothing he sends is
trusted until the pin is updated.

The bot logs the master key it sees for Steve at start, with `matchesPin`.
Read the real key with `/keys/query`, as described in the deployment
repository's docs/voice.md step 3, and compare the two before pinning.

## Steve's hand steps

The cluster side, in the deployment repository (PR #463,
`infra/agent-workplace/glovecell-voice` and its docs/voice.md), covers:

- Synapse and speaches;
- the bot user and its access token;
- the rooms;
- the owner, Forgejo, notify and handoff Secrets;
- the network policies on both ends;
- branch protection.

After this PR merges and the `voice` image is promoted:

1. Set the image digest in `deployment.yaml`.
2. Add these variables, which the deployment does not set yet:
   - `APPROVAL_REPOS`, for example `steve/*` (the gate refuses to start
     without it once the Forgejo token exists);
   - `VOICE_DEFAULT_REPO`, the repository Orca works in for the Inbox;
   - `NOTIFY_CALLER_TARGETS=orca-pilot=orca|codex`, so that
     `orca-pilot voice send --kind done --thread …` finishes the task.
3. List `deployment.yaml` in the kustomization.

To install with Helm instead, set:

- `voice.enabled`, `voice.image` (by digest), `voice.homeserver` and
  `voice.speech.baseURL`;
- `voice.orca.*` and `voice.approval.repos`;
- the egress and notify peers under `voice.networkPolicy`.

## Not in this version

- Live conversation (Element Call with LiveKit) is phase 2.
- Archon targets are a stub until Archon has an API. Until then, Orca can run
  Archon workflows on request.
- Claude Code and Codex headless in a cell (D14 cell-tools channel), and T3
  and Herdr targets (OrchestratorClass adapters) come later.
- Room aliases are not resolved: configure room IDs.
- No speaker verification: identity is the pinned, cross-signed Matrix device.
- The waveform is approximate (from packet sizes, not decoded audio).
