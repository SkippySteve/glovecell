# Subscription provider launchers

The workspace image includes stock Grok Build 1.0.46, Cursor CLI
2026.10.19-6bbf2c4 and Muse Code 1.4.7-R6101.1. T3 uses the Grok and Cursor
launchers through its native integrations. Muse runs in the project terminal.
These are client installations, not evidence of usable provider accounts.

The trusted backend projects a `provider-access` JSON field in the existing
read-only `model-access` Secret. It contains entries named `grok`, `cursor` and
`muse`. An enabled entry has `enabled: true` and an `access` object containing
`accessToken` and the Unix `expiresAt`. Grok also requires
`issuer: https://auth.x.ai`. Revocation uses `enabled: false` without access
material. Unknown providers, refresh fields and nested credential containers
are rejected. The launchers deny account sign-in, client updates, credential
overrides, alternate endpoints and execution outside enrolled projects.

Grok consumes projected replacements through its official external-auth
contract. Cursor receives an access-only token through CURSOR_AUTH_TOKEN.
Rotation stops only its provider process; the app must resume the retained
session to use the replacement. The launcher never replays a prompt or completed
command. Session directories remain on the workspace volume. Grant expiry,
revocation or a missing projection stops the affected launcher with a status
message. No worker connection to OpenBao or credential owners is added.

The inspecting gateway also checks the current grant and bearer credential on
every provider request. Raw HTTPS cannot bypass expiry, revocation or credential
replacement. Projected replacements are consumed without restarting other
providers or the workspace. Provider connections remain bounded; the gateway
admits up to 32 simultaneous TLS tunnels for the CLI's concurrent tool messages.

Muse's accessToken must be the non-secret placeholder
`apc-muse-subscription-gateway`. Its subscription credential comes from official
device-code sign-in and remains outside worker storage. The trusted controller
reads MUSE_SUBSCRIPTION_KEY_FILE and projects it only to the inspecting proxy.
The proxy injects it only for reviewed api.meta.ai model and inference routes,
using verified upstream TLS. Missing or revoked gateway material fails closed.
This does not create or substitute a separately billed Meta API key.

The launchers require qualified HTTPS policy, trusted account authorization and
a backend that publishes fresh grants. Installation and offline tests alone do
not qualify providers. Four-provider live qualification remains a separate
acceptance requirement.

The optional Go `provider-broker` command runs on the trusted control side.
It publishes five-minute access leases every thirty seconds from explicitly
configured Codex, Grok and Cursor credential owners. It renews each enabled
owner ten minutes before the owner's access expires. Configure
`CODEX_OAUTH_CLIENT_ID` and `CURSOR_OAUTH_CLIENT_ID` with the reviewed official
public client IDs. Token destinations are fixed, and redirects are denied.
Cursor uses the official desktop client's `/oauth/token` contract. The
project proxy always denies Cursor `/auth` and `/oauth` routes, including under
broad HTTPS path grants: Cursor can use an access JWT for renewal, so network
denial is required alongside omission of refresh fields. If its
response omits a distinct refresh token, the owner stores the replacement
access token as its refresh token, as the official client does.

Codex enrollment adds a `codex` entry to `provider-access`, with `accessToken`,
`expiresAt`, `chatgptAccountId` and optional `chatgptPlanType`. Bind
`INDEPENDENT_CODEX_WORKSPACE_UID` to the chosen workbench before enrolling it.
The controller refuses independent Codex grants without that inspecting gateway
binding. Legacy Codex grants retain their existing whole-worker expiry behavior.
For the independent mode, Codex expiry, revocation and owner outage stop only
Codex. The durable lifecycle marker preserves this behavior after controller
restart and projection removal. An absent projection denies provider traffic at
the gateway. The T3 adapter and manual client consume renewed access grants,
keep account identity fixed, and answer the official external-token refresh
callback with access material only. Conversations and working files remain.

Owner outages are independent; they leave the affected lease to expire while
other owners renew. Disabled grants stay disabled after broker restarts. Renewal
must retain the enrolled Codex account and Cursor subject. The publisher does
not renew Muse's subscription credential or replay coding tasks.

Bind this broker to a specific ProjectWorkspace UID. Give its ServiceAccount
only reads of that workspace, get/patch of its model-access projection and
renewal journal, and exec on the explicitly selected owner pods. Give workers
none of these permissions. Keep the journal in the trusted control namespace.
Each rotation records intent before its OAuth request and the response before
writing the owner's credential file. Recovery completes a saved owner write
without repeating OAuth. Codex and Cursor retain their credential inode and use
a private owner-local write intent to recover a torn in-place write. Concurrent
owner changes or active owner clients block the write. An interrupted request
with no recorded response fails closed and requires owner recovery. Concurrent
revocation or replacement of the workspace or projection prevents publication.

For a supervised operational check, stop the existing publisher first and run
`provider-broker --once --force-renew=codex,cursor` on the trusted side. Forced
renewal requires `--once`; it cannot become an endless refresh loop. Refresh
credentials must never be copied into a disposable worker. Qualification must
use disposable repositories and environments. Offline tests establish denial
and transaction recovery, while live provider requests and network probes need
separate recorded evidence.

The Codex callback and managed-owner contracts are documented in the
[official app-server reference](https://learn.chatgpt.com/docs/app-server).
