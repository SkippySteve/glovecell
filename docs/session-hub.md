# Session hub: shared agent sessions

Status: the image is built and scanned in CI, and the controller can add the
hub to cells. Both are off: the chart's `sessionHub.enabled` defaults to false,
and only a workspace created with `spec.sessionHub.enabled` gets a hub.

## In plain words

- **Now:** each operator app (Orca, T3 Code, Herdr, Agent Orchestrator) starts
  and owns its own agents. A chat started in one app can't be seen or answered
  in another.
- **Change:** a cell can run one *session hub*. Every chat agent in the cell
  runs once, behind the hub, and each app connects to the hub, so a chat
  started in any app shows up in the others. You can type into it and answer
  its permission prompts from any of them. Connecting each app to the hub
  (adapters) is a later change.
- **Why this way:** the apps share no session format, but each can speak ACP,
  or can run a client that does. The hub uses AHP, which is built for several
  apps sharing one live session. The alternative was writing our own hub,
  which means owning event ordering, replay and permission races.
- **Risk and undo:** nothing changes until an operator turns on
  `sessionHub.enabled` and creates a workspace with `spec.sessionHub.enabled`.
  Existing workspaces never change. To undo, remove the hub workspaces and set
  the value back to false.

**Words.**
- **ACP** ([Agent Client Protocol](https://agentclientprotocol.com/)): how an
  app drives a coding agent: start a session, send a prompt, stream the reply,
  ask for permission.
- **AHP** ([Agent Host Protocol](https://github.com/microsoft/agent-host-protocol)):
  a layer above ACP that lets several clients share live sessions. It keeps
  one ordered event stream, replays it on reconnect, and settles each
  permission prompt with the first answer.
- **[AHP-ACP Gateway](https://github.com/formulahendry/ahp-acp-gateway)**:
  an MIT-licensed AHP host that runs ACP agents as child processes and keeps
  sessions in SQLite.

## Why this project, and the maintenance exception

`AGENTS.md` asks for well-maintained projects: more than one maintainer or a
backing organisation, a security policy, real adoption. Six AHP hosts were
compared on 2026-11-15:

- **Well maintained, but not a fit:**
  - **VS Code's agent host** can only be used under the VS Code Server licence,
    which doesn't allow combining it with another product.
  - **AgentConnect** is a whole agent platform. It brings its own database,
    identity provider and sandboxes.
- **Fit, but fail the bar:**
  - **AHP-ACP Gateway** is the best fit. It is standalone, ACP to AHP,
    token-protected and small, but it has one main maintainer and no security
    policy yet.
  - **ahpd** and **hydra** also fail the bar.

The gateway is used as a **documented exception**, under these conditions:

- **Built from source.** CI builds it from a pinned upstream commit
  (`AHP_ACP_GATEWAY_COMMIT` in the Dockerfile; today the v0.2.2 release,
  `f01c188b3e24e187af0df1190a1285aca15b1bd4`), and the build checks the commit
  hash. Upstream's container image and npm tarball are never used.
- **Locked and tested.** Dependencies come from upstream's lockfile with
  install scripts disabled. Upstream's test suite must pass inside the build.
- **Can't fetch agents.** The runtime image has no npm, npx, corepack or yarn,
  so the hub can't download an agent at run time. Every built-in agent is off
  in [`config/session-hub/gateway.json`](../config/session-hub/gateway.json).
- **Scanned.** The image goes through the same Trivy gate as every other
  target.
- **Upstream first.** We asked for a security policy
  ([upstream #6](https://github.com/formulahendry/ahp-acp-gateway/issues/6)),
  and features go upstream as pull requests, not private patches.
- **Fork if upstream goes quiet.** If there is no release for six months, or
  no answer to a security report within 30 days, the pinned commit is forked
  into a Glovecell-owned repository.

To move the pin:
1. Review the upstream diff since the current commit.
2. Change `AHP_ACP_GATEWAY_COMMIT`.
3. Update the version check in the `session-hub` stage.
4. Refresh `third_party/licenses/ahp-acp-gateway/` if a runtime dependency
   changed.

## The image

| | |
| --- | --- |
| Dockerfile target | `session-hub` (built in `session-hub-build`) |
| Process | `node /opt/ahp-acp-gateway/dist/src/cli.js --config /etc/session-hub/gateway.json` |
| User | 1001:1001 (`hub`), not the workspace user 1000 |
| Listener | `ws://127.0.0.1:8765/ahp` (inside the pod only) |
| State | `/var/lib/session-hub/gateway.db`; mount a writable volume there and keep the root filesystem read-only |
| Agents | The image's own config has one placeholder, `cell`, which goes through the launcher. Outside a session-hub cell there is no launcher, so opening a session fails with a message that names this page. Upstream refuses to start with no agent enabled. In a cell, the controller's config replaces this one. |

Checked locally before the first PR:
- It starts with a read-only root, all capabilities dropped and
  `no-new-privileges`.
- An AHP `initialize` lists only the `cell` agent.
- `createSession` for `cell` fails with the missing-launcher message.
- Trivy reports no fixable HIGH or CRITICAL findings.

## In a cell

An operator turns it on in the chart and then creates a workspace that asks
for it:

```yaml
# values.yaml
sessionHub:
  enabled: true
  image: registry.example.com/glovecell-session-hub@sha256:<digest>
  agents:
    - {id: cell-codex, displayName: Codex, command: /usr/local/bin/codex-acp}
---
# the workspace
spec:
  sessionHub: {enabled: true}
```

The chart refuses a tag instead of a digest, an empty agent list, a relative
command and unknown fields. Agent ids may not reuse a gateway built-in
(`claude`, `codex` and so on), because a built-in override would inherit its
`npx` launcher.

### Agents in the workspace image

The launcher always runs Glovecell's workspace image, whichever orchestrator
class the cell uses. That image has the brokered provider launchers that work
inside cells. It also carries these ACP adapters:

| Command | Adapter | How it reaches the model |
| --- | --- | --- |
| `/usr/local/bin/codex-acp` | [`@agentclientprotocol/codex-acp`](https://github.com/agentclientprotocol/codex-acp) 2.1.1 (Apache-2.0), from the lockfile in `config/acp-adapters/` | `CODEX_PATH` is the cell's Codex bridge (`apc-codex`). It signs Codex in with the operator's lease from the cell's model grant, refuses any other sign-in, and keeps threads inside the project directory. The wrapper clears `CODEX_API_KEY` and `OPENAI_API_KEY`. The adapter's bundled Codex binaries are optional packages and are not installed. |

So `{id: cell-codex, displayName: Codex, command: /usr/local/bin/codex-acp}`
works once the workspace has a Codex grant (`spec.providers`, see
[brokered providers](brokered-providers.md)).

Version note: `codex-acp` 2.1.1 targets Codex 0.159, the Codex the workspace
image pins. Newer adapters need Codex 0.166 or later, so they move together
with the image's Codex.

No Claude adapter ships in this image. `claude-agent-acp` runs Anthropic's
Claude Agent SDK and Claude Code, which are under Anthropic's own terms, not
an open-source licence. Glovecell's images are published, so a deployment
that wants Claude over ACP builds its own launcher image:

1. `FROM` the workspace image at the digest the deployment runs.
2. Install Claude Code at `/opt/claude/claude`, pinned by checksum, and link
   `/usr/local/bin/claude` to `apc-provider`. The workspace's provider launcher
   then runs it brokered: only with the cell's stand-in token
   (`sk-ant-oat01-apc-cell-…`; it refuses any other token), with no API key or
   other endpoint, and with updates and sign-in commands refused
   ([brokered providers](brokered-providers.md)).
3. Install `@agentclientprotocol/claude-agent-acp` from a lockfile without
   optional packages, so the SDK's bundled Claude binaries are left out. Add a
   wrapper that sets `CLAUDE_CODE_EXECUTABLE=/usr/local/bin/claude`.
4. Set `sessionHub.launcherImage` to that image's digest, and allowlist the
   wrapper, for example `{id: cell-claude, displayName: Claude, command: /usr/local/bin/claude-acp}`.

The workspace container and the hub keep their own images; only the launcher
runs the deployment's image.

The controller then adds two containers to that cell's worker, plus one Secret
(`session-hub`) holding the cell's token, the gateway config and the
allowlist:

| Container | Image | User | Sees | Does |
| --- | --- | --- | --- | --- |
| `session-hub` | session-hub | 1001 | `gateway.json` (with the token), the launcher socket, its own state | Runs the gateway on `127.0.0.1:8765`. Never runs an agent: each agent is `agent-project-controller session-hub-connect --agent <id>`. No proxy settings, no ports, no workspace volume. |
| `workspace` (existing) | unchanged | 1000 | also `/run/session-hub-client/token` and `agents.json` (read-only) | Runs the operator apps; their connector (`session-hub-acp`) joins the hub with the token. Apps read the allowlist from `agents.json` to offer one provider per hub agent. A file, so an allowlist change reaches running workers without changing the pod spec. |
| `session-hub-launcher` | the workspace image | 1000 | the workspace's volumes and environment, `agents.json`, the launcher socket | `agent-project-controller session-hub-launcher`: on a request over the unix socket it starts that allowlisted agent in `/workspace/repo` and relays its standard input and output. |

**Boundaries:**

- **The token.** It stays in the hub container. Agents run in the launcher
  container, which mounts only `agents.json`; the workspace container mounts
  neither file nor the socket. Every container in a pod shares loopback, so
  the token is what stops a process in the cell from driving the hub. Without
  it, or with a wrong one, the gateway answers 401.
- **The launcher.**
  - Its socket is `0660`, reachable through the pod's fsGroup.
  - It starts only allowlisted commands, with no shell, and re-reads the
    allowlist on every request. A broken allowlist refuses every request.
  - It runs at most 8 agents at once.
  - If the hub side disconnects, the agent's whole process group is killed
    after 5 seconds.
- **Fixed at creation.** `spec.sessionHub` is fixed when the workspace is
  created (CRD rule), so a running worker never gains or loses containers.
- **Image updates.** Workspace and hub image promotions roll the hub
  containers with the worker, the same way as the workspace image.
- **The token itself.** It is generated once per cell and kept. If it is
  overwritten with an invalid value, it is regenerated. Allowlist changes
  update the Secret without a new token; the hub reads its config at start.

## Connecting an app

`agent-project-controller session-hub-acp --agent <id>` is what an operator app
starts as an ACP agent. It speaks ACP to the app on stdin and stdout, and AHP
to the hub on `ws://127.0.0.1:8765/ahp` with the cell's token from
`/run/session-hub-client/token`. It runs where the app's server runs: the
workspace container.

| App does (ACP) | Connector does (AHP) |
| --- | --- |
| `initialize` | connects; advertises `loadSession`, `sessionCapabilities.list` and `.resume` |
| `session/new` | `createSession` for the allowlisted agent, subscribes to its chat |
| `session/prompt` | dispatches `chat/turnStarted`, streams the turn, returns on its `turnComplete`, `turnCancelled` or `error` |
| `session/cancel` | dispatches `chat/turnCancelled` |
| `session/list` | `listSessions`, only this agent's sessions, from whichever app |
| `session/load` / `session/resume` | subscribes; `load` replays the chat so far |
| answers `session/request_permission` | dispatches `chat/toolCallConfirmed`; the hub keeps the first answer |

What other apps do in the same session reaches this app live. Their messages
arrive as user message chunks and their replies as agent chunks. When another
app answers a permission question first, this app's open question is
withdrawn. The connector refuses a non-loopback hub, a missing or malformed
token, and a session of a different agent.

**T3 Code** (0.1 and later) runs it as an ACP provider instance with a local
command:

```json
{"providerInstances": {"cell-codex": {"driver": "acpRegistry", "enabled": true,
  "config": {"source": "local", "commandPath": "/usr/local/bin/agent-project-controller",
             "commandArgs": ["session-hub-acp", "--agent", "cell-codex"]}}}}
```

T3 lists the hub's sessions under that provider and can import one started in
another app as a thread. Writing this into T3's settings in hub cells, and
mounting the token into the workspace container, is a separate change.

**Orca** (1.4) runs a fixed list of ACP chat agents and has no user-defined
ones yet ([orca#38083](https://github.com/stablyai/orca/issues/38083)). So in
an Orca cell (`orchestratorClassName: orca`) that has the hub, Glovecell
borrows one of them, **OMP**, which the workspace images don't ship. At start
the worker writes `~/.local/bin/omp`, a folder Orca searches for agent
programs. At each chat start that small script reads the cell's allowlist,
`/run/session-hub-client/agents.json` in the workspace container, and runs
`session-hub-acp --agent <id>` for its **first** agent. Orca then lists "OMP"
as a chat agent in the desktop and mobile apps, and each OMP chat is a hub
session. Without that file, `omp --version` fails too, so Orca offers no OMP
chat. Because the script reads the file, changing `sessionHub.agents` doesn't
change the worker's command or restart it.

- **How Orca calls it:** Orca starts an OMP chat as `omp acp` (plus
  `--plugin-dir` for its visuals skill) and first asks `omp --version`.
  The script answers `17.0.5`, the oldest OMP that Orca 1.4 accepts, and drops
  Orca's arguments.
- **What it refuses:** Orca lists OMP's models by opening a throwaway session
  with `--session-dir`. The script refuses that, so listing models never
  creates a hub session. Orca shows no model list, and the hub agent uses its
  own default. Any other use, such as typing `omp` in a terminal, is refused
  with a note.
- **What it leaves alone:** an `omp` that isn't Glovecell's script. Other
  cells' workers start exactly as before. If the script can't be written,
  Orca starts without it.

What Orca 1.4.237 does with a hub session (checked against its source and a
local `orcad` with a stand-in connector):
- **Session list:** Orca never calls `session/list`. Its chat list holds only
  the chats it started, so it can't open a session started in another app.
  The other apps can open Orca's chats.
- **Reopening:** Orca reopens its own chat with `session/load`, but keeps its
  own copy of the conversation and drops the replay. Turns other apps sent
  while that chat was closed don't appear in Orca.
- **Live turns from other apps:** their replies and tool calls appear in the
  open chat, outside any turn. The other app's prompt itself isn't shown,
  because Orca ignores live user messages.
- **Permission questions from other apps' turns:** Orca answers `cancelled` at
  once, because nobody in Orca is waiting on that turn. The connector currently
  sends any answer other than a selected option to the hub as a refusal, so
  while an Orca chat is open, it refuses those questions before the other app
  can answer. Fix this in the connector before using Orca together with other
  apps on one session.

Checked locally, against the gateway, launcher and a fake ACP agent in
separate containers:
- **Two connectors on one session.** One created a session and prompted; the
  other listed it and loaded it (history replayed).
- **Live following.** Each then saw the other's turns live.
- **Permissions.** A permission question went to both. The first answer was
  used, and the other app's question was withdrawn.

Known limits:
- Hub state is an `emptyDir`, so sessions don't survive a worker restart.
- The launcher uses the workspace's resource profile.
- Only Orca (OMP slot, above) is wired in a cell; the other apps come next.
- Orca's chats start the agent in `/workspace/repo` (the launcher's
  directory), even when the chat belongs to another worktree.
- Codex sessions need a live Codex grant, so the adapter is checked locally only
  as far as the ACP handshake; a full session is part of the qualification run.

Before production use, a qualification run in a disposable cell must show
that a process in the workspace or launcher container can't use the hub to
answer its own permission prompts. A unix-socket listener upstream would
remove the shared-loopback question altogether, and it is one of the features
to contribute.

Checked locally:
- The hub as 1001 (with the fsGroup) and the launcher as 1000 ran in separate
  read-only containers with all capabilities dropped, sharing only the socket
  volume.
- AHP clients with no token or a wrong token got 401.
- With the token, `createSession` started the allowlisted agent through
  `session-hub-connect` and the launcher.
