# Session hub: shared agent sessions

Status: the image is built and scanned in CI. No cell runs it yet: the cell
wiring and the `sessionHub.enabled` chart value come in a later change.

## In plain words

- **Now:** each operator app (Orca, T3 Code, Herdr, Agent Orchestrator) starts
  and owns its own agents. A chat started in one app can't be seen or answered
  in another.
- **Change:** a cell can run one *session hub*. Every chat agent in the cell
  runs once, behind the hub, and each app connects to the hub, so a chat
  started in any app shows up in the others. You can type into it and answer
  its permission prompts from any of them. This page covers the hub image.
  Adding it to cells comes later.
- **Why this way:** the apps share no session format, but each can speak ACP,
  or can run a client that does. The hub uses AHP, which is built for several
  apps sharing one live session. The alternative was writing our own hub,
  which means owning event ordering, replay and permission races.
- **Risk and undo:** the image alone changes nothing, because no workload uses
  it. Remove the Dockerfile target and the CI matrix entry to undo.

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
| Agents | One placeholder, `cell`, pointing at `/usr/local/bin/glovecell-acp-launch`, which isn't in the image. Upstream refuses to start with no agent enabled, and agents start only when a session is created, so opening one fails with a message that names this page. |

Checked locally before the first PR:
- It starts with a read-only root, all capabilities dropped and
  `no-new-privileges`.
- An AHP `initialize` lists only the `cell` agent.
- `createSession` for `cell` fails with the missing-launcher message.
- Trivy reports no fixable HIGH or CRITICAL findings.

## Cell wiring (next change, not in this image)

- **Hub container.** It runs in the cell's worker pod beside the workspace
  container. It holds sessions only and never runs agents itself.
- **Agents.** Each chat agent runs in the **workspace** container. That is
  where the repository, proxy settings, CA trust and provider sign-ins already
  are. The hub's `cell` agent is a small launcher that asks the workspace
  container, over a unix socket, to start the agent and relay its standard
  input and output.
- **Access.** v0.2.2 listens only on TCP, and every container in a pod shares
  loopback. So the wiring gives each cell a generated token and puts it in the
  hub's config. Upstream reads the token only from that config file.
  `listener.host` stays `127.0.0.1`, and no Service, Ingress or route exposes
  the port.
- **Before `sessionHub.enabled` is switched on**, a qualification run proves
  that a process in the workspace container can't use the hub to answer its
  own permission prompts. A unix-socket listener upstream would make that
  boundary simpler; that is one of the features to contribute.
